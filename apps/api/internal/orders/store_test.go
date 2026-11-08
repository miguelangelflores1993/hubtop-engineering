//go:build integration

package orders

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Runs the real SQL against the local Supabase database with the fake seed
// (supabase/seed.sql). Each test works inside a transaction that is rolled
// back, so the seed never changes. They sit behind a build tag, so
// asking for them without a database fails instead of passing silently.
//
//	TEST_DATABASE_URL=$(supabase status -o env | sed -n 's/^DB_URL=//p' | tr -d '"') \
//	  go test -tags integration ./...
var (
	carla = uuid.MustParse("33333333-3333-4333-8333-333333333333") // plumbing
	diego = uuid.MustParse("44444444-4444-4444-8444-444444444444") // electrical
)

func txStore(t *testing.T) (*PGStore, pgx.Tx) {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Fatal("TEST_DATABASE_URL is empty: start the local stack (supabase start) and set it")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = tx.Rollback(ctx)
		_ = conn.Close(ctx)
	})
	return NewPGStore(tx), tx
}

func contains(orders []Order, id uuid.UUID) bool {
	for _, o := range orders {
		if o.ID == id {
			return true
		}
	}
	return false
}

func TestStoreFlow(t *testing.T) {
	store, _ := txStore(t)
	ctx := context.Background()

	order, err := store.Create(ctx, ana, "plumbing", "The bathroom tap drips all night")
	if err != nil || order.Status != "open" || order.CreatedAt.Location() != time.UTC {
		t.Fatalf("Create: %+v, %v; want open, created_at in UTC", order, err)
	}

	// Story 2.1: a plumbing professional sees it first (newest first).
	category, page, err := store.Feed(ctx, carla)
	feed := page.Orders
	if err != nil || category == nil || *category != "plumbing" || len(feed) == 0 || feed[0].ID != order.ID || page.HasMore {
		t.Fatalf("Feed(carla) = %v, %d orders, %v; want plumbing with the new order first", category, len(feed), err)
	}
	for _, o := range feed {
		if o.Category != "plumbing" || o.Status != "open" || o.CreatedAt.Location() != time.UTC {
			t.Errorf("Feed(carla) returned %+v", o)
		}
	}
	// Story 2.2: an electrical professional doesn't.
	if _, page, _ := store.Feed(ctx, diego); contains(page.Orders, order.ID) {
		t.Error("an electrical professional sees a plumbing order")
	}
	// A client has no category: empty feed, no error.
	if category, page, err := store.Feed(ctx, ana); category != nil || len(page.Orders) != 0 || page.HasMore || err != nil {
		t.Errorf("Feed(ana) = %v, %v, %v; want nil, [], nil", category, page, err)
	}
	// Story 1.1 and 2.3: the owner sees it in "my orders", another client doesn't.
	if mine, _ := store.Mine(ctx, ana); !contains(mine.Orders, order.ID) {
		t.Error("the owner doesn't see their order")
	}
	if theirs, _ := store.Mine(ctx, bruno); contains(theirs.Orders, order.ID) {
		t.Error("another client sees the order")
	}
}

func TestStoreConstraints(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name                  string
		client                uuid.UUID
		category, description string
		want                  error
	}{
		{"unknown category", ana, "gardening", "Trim the hedge by the gate", ErrUnknownCategory},
		{"no account", uuid.New(), "plumbing", "The kitchen sink is leaking", ErrNoAccount},
		{"description the handler would reject", ana, "plumbing", "  short  ", ErrInvalidDescription},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, _ := txStore(t)
			if _, err := store.Create(ctx, tc.client, tc.category, tc.description); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

// A caller who doesn't exist is ErrNoAccount on every read, like on Create.
func TestMineWithoutAccount(t *testing.T) {
	store, _ := txStore(t)
	if _, err := store.Mine(context.Background(), uuid.New()); !errors.Is(err, ErrNoAccount) {
		t.Fatalf("Mine(unknown) err = %v, want ErrNoAccount", err)
	}
	if _, _, err := store.Feed(context.Background(), uuid.New()); !errors.Is(err, ErrNoAccount) {
		t.Fatalf("Feed(unknown) err = %v, want ErrNoAccount", err)
	}
}

// The seed alone fits in one page.
func TestFeedHasMoreIsFalseOnTheSeed(t *testing.T) {
	store, _ := txStore(t)
	_, page, err := store.Feed(context.Background(), carla)
	if err != nil || page.HasMore {
		t.Fatalf("Feed(carla) on the seed: HasMore %v, %v; want false", page.HasMore, err)
	}
}

// The schema only allows status 'open' today, so this test drops the check
// (inside its transaction, rolled back) to prove the feed filters on status
// itself and doesn't lean on the constraint.
func TestFeedExcludesOtherStatuses(t *testing.T) {
	store, tx := txStore(t)
	ctx := context.Background()
	if _, err := tx.Exec(ctx, `alter table app.orders drop constraint orders_status_check`); err != nil {
		t.Fatal(err)
	}
	var closedID uuid.UUID
	if err := tx.QueryRow(ctx,
		`insert into app.orders (client_id, category, description, status)
		 values ($1, 'plumbing', 'A plumbing order that is closed', 'closed') returning id`, ana).Scan(&closedID); err != nil {
		t.Fatal(err)
	}
	_, page, err := store.Feed(ctx, carla)
	if err != nil {
		t.Fatal(err)
	}
	if contains(page.Orders, closedID) {
		t.Error("the feed returned a closed order")
	}
}

// FR-003: at most 50, a visible cut, and a stable order when created_at ties
// (every insert in one transaction gets the same now()).
func TestPageLimitAndTies(t *testing.T) {
	store, tx := txStore(t)
	ctx := context.Background()
	if _, err := tx.Exec(ctx,
		`insert into app.orders (client_id, category, description)
		 select $1, 'plumbing', 'Generated order number ' || n from generate_series(1, 60) n`, ana); err != nil {
		t.Fatal(err)
	}
	_, feedPage, err := store.Feed(ctx, carla)
	feed := feedPage.Orders
	if err != nil || len(feed) != PageSize || !feedPage.HasMore {
		t.Fatalf("Feed returned %d orders, HasMore %v, %v; want %d and true", len(feed), feedPage.HasMore, err, PageSize)
	}
	minePage, err := store.Mine(ctx, ana)
	if err != nil || len(minePage.Orders) != PageSize || !minePage.HasMore {
		t.Fatalf("Mine returned %d orders, HasMore %v, %v; want %d and true", len(minePage.Orders), minePage.HasMore, err, PageSize)
	}
	for _, list := range [][]Order{feed, minePage.Orders} {
		for i := 1; i < len(list); i++ {
			a, b := list[i-1], list[i]
			if a.CreatedAt.Before(b.CreatedAt) ||
				(a.CreatedAt.Equal(b.CreatedAt) && strings.Compare(a.ID.String(), b.ID.String()) < 0) {
				t.Fatalf("list out of order at %d: %v/%v before %v/%v", i, a.CreatedAt, a.ID, b.CreatedAt, b.ID)
			}
		}
		if list[0].CreatedAt.Location() != time.UTC {
			t.Errorf("created_at not in UTC: %v", list[0].CreatedAt.Location())
		}
	}
}
