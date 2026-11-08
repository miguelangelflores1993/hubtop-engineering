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

// FeedLimit is FR-003's "at most 50".
const FeedLimit = 50

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

// Feed returns the caller's category (nil if they aren't a professional) and
// the newest open orders in it, in one round trip: no row means no account,
// and a caller without a category matches no orders.
func (s *PGStore) Feed(ctx context.Context, accountID uuid.UUID) (*string, []Order, error) {
	var category *string
	var orders []Order
	// order by matches orders_feed_idx; id breaks ties so the order is stable
	err := s.db.QueryRow(ctx, `
		select a.professional_category, (
		  select coalesce(json_agg(f order by f.created_at desc, f.id desc), '[]')
		  from (select `+orderColumns+` from app.orders
		        where status = 'open' and category = a.professional_category
		        order by created_at desc, id desc limit $2) f)
		from app.accounts a where a.id = $1`, accountID, FeedLimit).Scan(&category, &orders)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, ErrNoAccount
	}
	for i := range orders {
		orders[i].CreatedAt = orders[i].CreatedAt.UTC()
	}
	return category, orders, err
}

// Mine returns the caller's own orders, newest first.
func (s *PGStore) Mine(ctx context.Context, clientID uuid.UUID) ([]Order, error) {
	rows, err := s.db.Query(ctx, `select `+orderColumns+` from app.orders
		where client_id = $1 order by created_at desc, id desc limit $2`, clientID, FeedLimit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanOrder)
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
