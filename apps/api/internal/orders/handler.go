// Package orders serves spec 001: a client publishes an order and a
// professional of the same category sees it.
package orders

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/miguelangelflores1993/hubtop-engineering/apps/api/internal/auth"
)

// Store is the data the handlers need; PGStore is the real one.
type Store interface {
	Create(ctx context.Context, clientID uuid.UUID, category, description string) (Order, error)
	Feed(ctx context.Context, accountID uuid.UUID) (*string, []Order, error)
	Mine(ctx context.Context, clientID uuid.UUID) ([]Order, error)
}

type Handler struct{ store Store }

func NewHandler(store Store) *Handler { return &Handler{store: store} }

// Routes registers every route behind requireAuth: none of them is public.
func (h *Handler) Routes(mux *http.ServeMux, requireAuth func(http.Handler) http.Handler) {
	mux.Handle("POST /orders", requireAuth(http.HandlerFunc(h.create)))
	mux.Handle("GET /orders/feed", requireAuth(http.HandlerFunc(h.feed)))
	mux.Handle("GET /orders/mine", requireAuth(http.HandlerFunc(h.mine)))
}

// createRequest has no owner field on purpose (FR-002): a body that names
// another account is decoded into this type, and the extra field is dropped.
// The owner can only come from the token.
type createRequest struct {
	Category    string `json:"category"`
	Description string `json:"description"`
}

const maxBody = 16 << 10 // a 500-character description fits many times over

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	caller, ok := callerOrFail(w, r)
	if !ok {
		return
	}
	var in createRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody)).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json")
		return
	}
	// FR-005 is the app's job, but the API never trusts a client: same rule,
	// counted in characters (not bytes) and without surrounding whitespace.
	description := strings.TrimSpace(in.Description)
	if n := utf8.RuneCountInString(description); n < 10 || n > 500 {
		writeError(w, http.StatusUnprocessableEntity, "invalid_description")
		return
	}
	order, err := h.store.Create(r.Context(), caller, in.Category, description)
	switch {
	case errors.Is(err, ErrUnknownCategory):
		writeError(w, http.StatusUnprocessableEntity, "unknown_category")
	case errors.Is(err, ErrInvalidDescription):
		writeError(w, http.StatusUnprocessableEntity, "invalid_description")
	case errors.Is(err, ErrNoAccount):
		writeError(w, http.StatusForbidden, "no_account")
	case err != nil:
		internalError(w, r, err)
	default:
		writeJSON(w, http.StatusCreated, order)
	}
}

func (h *Handler) feed(w http.ResponseWriter, r *http.Request) {
	caller, ok := callerOrFail(w, r)
	if !ok {
		return
	}
	category, orders, err := h.store.Feed(r.Context(), caller)
	switch {
	case errors.Is(err, ErrNoAccount):
		writeError(w, http.StatusForbidden, "no_account")
	case err != nil:
		internalError(w, r, err)
	default:
		// category null + no orders: the app explains "you have no category"
		writeJSON(w, http.StatusOK, map[string]any{"category": category, "orders": nonNil(orders)})
	}
}

func (h *Handler) mine(w http.ResponseWriter, r *http.Request) {
	caller, ok := callerOrFail(w, r)
	if !ok {
		return
	}
	orders, err := h.store.Mine(r.Context(), caller)
	if err != nil {
		internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"orders": nonNil(orders)})
}

// callerOrFail fails closed: a route wired without the middleware has no
// caller, and answering it as anyone would be worse than a 500.
func callerOrFail(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, ok := auth.AccountID(r.Context())
	if !ok {
		internalError(w, r, errors.New("route served without auth middleware"))
	}
	return id, ok
}

func nonNil(o []Order) []Order {
	if o == nil {
		return []Order{} // JSON [] rather than null
	}
	return o
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]string{"error": code})
}

// internalError logs the cause and tells the caller nothing about it.
func internalError(w http.ResponseWriter, r *http.Request, err error) {
	slog.ErrorContext(r.Context(), "request failed", "method", r.Method, "path", r.URL.Path, "err", err)
	writeError(w, http.StatusInternalServerError, "internal")
}
