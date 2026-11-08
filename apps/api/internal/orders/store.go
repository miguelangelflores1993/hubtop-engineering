package orders

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Order is what the API returns. It has no client_id: a professional sees
// the job, not who asked for it.
type Order struct {
	ID          uuid.UUID `json:"id" db:"id"`
	Category    string    `json:"category" db:"category"`
	Description string    `json:"description" db:"description"`
	Status      string    `json:"status" db:"status"`
	CreatedAt   time.Time `json:"created_at" db:"created_at"`
}

// PageSize is FR-003's "at most 50".
const PageSize = 50

var (
	ErrUnknownCategory    = errors.New("unknown category")
	ErrNoAccount          = errors.New("no account for this user")
	ErrInvalidDescription = errors.New("description out of range")
)

// DB is what PGStore needs from Postgres. A *pgxpool.Pool and a pgx.Tx both
// satisfy it, so tests run the real SQL inside a transaction they roll back.
type DB interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// PGStore talks to schema app as the API's own role. That role bypasses RLS,
// so every query here filters by the caller it was given: these WHERE
// clauses are the authorization.
type PGStore struct{ db DB }

func NewPGStore(db DB) *PGStore { return &PGStore{db: db} }

const orderColumns = `id, category, description, status, created_at`

func (s *PGStore) Create(ctx context.Context, clientID uuid.UUID, category, description string) (Order, error) {
	rows, err := s.db.Query(ctx,
		`insert into app.orders (client_id, category, description) values ($1, $2, $3)
		 returning `+orderColumns, clientID, category, description)
	if err != nil {
		return Order{}, err
	}
	o, err := pgx.CollectExactlyOneRow(rows, scanOrder)
	return o, constraintError(err)
}

// Page is one screen of orders. HasMore says the list was cut at PageSize.
type Page struct {
	Orders  []Order
	HasMore bool
}

// The only two WHERE clauses page accepts. They are constants: every value
// reaches the query as a parameter. `a` is the caller's account row.
const (
	feedFilter = `status = 'open' and category = a.professional_category`
	mineFilter = `client_id = a.id`
)

// Feed returns the caller's category (nil if they aren't a professional) and
// the newest open orders in it. No account is ErrNoAccount; a caller without a
// category matches no orders.
func (s *PGStore) Feed(ctx context.Context, accountID uuid.UUID) (*string, Page, error) {
	return s.page(ctx, feedFilter, accountID)
}

// Mine returns the caller's own orders, newest first. An unknown account is
// ErrNoAccount, as in Feed.
func (s *PGStore) Mine(ctx context.Context, clientID uuid.UUID) (Page, error) {
	_, page, err := s.page(ctx, mineFilter, clientID)
	return page, err
}

// page runs one query: the account row, left-joined to its orders. Zero rows
// is no account; a row with a null order id is an account without orders. It
// fetches PageSize+1 rows to learn whether the list was cut. The order by
// matches orders_feed_idx and orders_client_idx; id breaks ties so the order
// is stable. Columns are
// scanned by position, so a JSON tag can't change what is read.
func (s *PGStore) page(ctx context.Context, where string, accountID uuid.UUID) (*string, Page, error) {
	rows, err := s.db.Query(ctx, `
		select a.professional_category, o.id, o.category, o.description, o.status, o.created_at
		from app.accounts a
		left join lateral (
		  select `+orderColumns+` from app.orders
		  where `+where+`
		  order by created_at desc, id desc limit $2) o on true
		where a.id = $1
		order by o.created_at desc, o.id desc`, accountID, PageSize+1)
	if err != nil {
		return nil, Page{}, err
	}
	defer rows.Close()

	var category *string
	var page Page
	found := false
	for rows.Next() {
		var (
			id          *uuid.UUID
			ocategory   *string
			description *string
			status      *string
			createdAt   *time.Time
		)
		if err := rows.Scan(&category, &id, &ocategory, &description, &status, &createdAt); err != nil {
			return nil, Page{}, err
		}
		found = true
		if id == nil {
			continue // the account has no orders
		}
		if ocategory == nil || description == nil || status == nil || createdAt == nil {
			return nil, Page{}, errors.New("order row with null columns")
		}
		page.Orders = append(page.Orders, Order{
			ID: *id, Category: *ocategory, Description: *description, Status: *status,
			CreatedAt: createdAt.UTC(), // pgx would use the server's local zone
		})
	}
	if err := rows.Err(); err != nil {
		return nil, Page{}, err
	}
	if !found {
		return nil, Page{}, ErrNoAccount
	}
	if len(page.Orders) > PageSize {
		page.Orders, page.HasMore = page.Orders[:PageSize], true
	}
	return category, page, nil
}

// scanOrder reads a row and normalizes created_at to UTC: pgx would use the
// server's local zone, and the JSON would change with the machine it runs on.
func scanOrder(row pgx.CollectableRow) (Order, error) {
	o, err := pgx.RowToStructByName[Order](row)
	o.CreatedAt = o.CreatedAt.UTC()
	return o, err
}

// constraintError turns the constraints the API relies on into errors the
// handler can answer with a 4xx. The database is the final judge; the
// handler's own checks only give a faster, clearer message.
func constraintError(err error) error {
	var pg *pgconn.PgError
	if !errors.As(err, &pg) {
		return err
	}
	switch pg.ConstraintName {
	case "orders_category_fkey":
		return ErrUnknownCategory
	case "orders_client_id_fkey":
		return ErrNoAccount
	case "orders_description_check":
		return ErrInvalidDescription
	}
	return err
}
