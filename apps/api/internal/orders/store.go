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
	o, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByName[Order])
	return o, constraintError(err)
}

// Feed returns the caller's category (nil if they aren't a professional) and
// the newest open orders in it.
func (s *PGStore) Feed(ctx context.Context, accountID uuid.UUID) (*string, []Order, error) {
	var category *string
	err := s.db.QueryRow(ctx,
		`select professional_category from app.accounts where id = $1`, accountID).Scan(&category)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, ErrNoAccount
	}
	if err != nil || category == nil {
		return nil, []Order{}, err
	}
	// order by matches orders_feed_idx; id breaks ties so the order is stable
	orders, err := s.list(ctx, `where status = 'open' and category = $1`, *category)
	return category, orders, err
}

// Mine returns the caller's own orders, newest first.
func (s *PGStore) Mine(ctx context.Context, clientID uuid.UUID) ([]Order, error) {
	return s.list(ctx, `where client_id = $1`, clientID)
}

// list only ever receives the constant WHERE clauses above; every value goes
// in as a parameter, never into the SQL text.
func (s *PGStore) list(ctx context.Context, where string, arg any) ([]Order, error) {
	rows, err := s.db.Query(ctx,
		`select `+orderColumns+` from app.orders `+where+
			` order by created_at desc, id desc limit $2`, arg, FeedLimit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[Order])
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
