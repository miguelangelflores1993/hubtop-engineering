package orders

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/miguelangelflores1993/hubtop-engineering/apps/api/internal/auth"
	"github.com/miguelangelflores1993/hubtop-engineering/apps/api/internal/auth/authtest"
)

var (
	ana   = uuid.MustParse("11111111-1111-4111-8111-111111111111")
	bruno = uuid.MustParse("22222222-2222-4222-8222-222222222222")
)

// fakeStore records what the handler asked for and answers with canned data.
type fakeStore struct {
	calls       int
	gotClient   uuid.UUID
	gotCategory string
	gotDesc     string
	category    *string
	orders      []Order
	hasMore     bool
	err         error
}

func (f *fakeStore) Create(_ context.Context, client uuid.UUID, category, desc string) (Order, error) {
	f.calls++
	f.gotClient, f.gotCategory, f.gotDesc = client, category, desc
	return Order{ID: uuid.New(), Category: category, Description: desc, Status: "open", CreatedAt: time.Now()}, f.err
}

func (f *fakeStore) Feed(_ context.Context, id uuid.UUID) (*string, Page, error) {
	f.calls++
	f.gotClient = id
	return f.category, Page{Orders: f.orders, HasMore: f.hasMore}, f.err
}

func (f *fakeStore) Mine(_ context.Context, id uuid.UUID) (Page, error) {
	f.calls++
	f.gotClient = id
	return Page{Orders: f.orders, HasMore: f.hasMore}, f.err
}

type server struct {
	t     *testing.T
	mux   *http.ServeMux
	key   *ecdsa.PrivateKey
	store *fakeStore
}

func newServer(t *testing.T, store *fakeStore) *server {
	key := authtest.NewKey(t)
	verifier := auth.NewVerifier(func(*jwt.Token) (any, error) { return &key.PublicKey, nil }, authtest.Issuer)
	mux := http.NewServeMux()
	NewHandler(store).Routes(mux, verifier.Middleware)
	return &server{t: t, mux: mux, key: key, store: store}
}

func (s *server) token(sub uuid.UUID) string {
	return authtest.Sign(s.t, jwt.SigningMethodES256, s.key, authtest.Claims(sub))
}

// do sends a request as `as` (uuid.Nil: no token) and decodes the JSON reply.
func (s *server) do(method, path string, as uuid.UUID, body string) (int, map[string]any) {
	s.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if as != uuid.Nil {
		req.Header.Set("Authorization", "Bearer "+s.token(as))
	}
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		s.t.Fatalf("%s %s: reply is not JSON: %q", method, path, rec.Body.String())
	}
	return rec.Code, out
}

func TestEveryRouteRequiresAToken(t *testing.T) {
	for _, route := range []struct{ method, path string }{
		{http.MethodPost, "/orders"}, {http.MethodGet, "/orders/feed"}, {http.MethodGet, "/orders/mine"},
	} {
		store := &fakeStore{}
		code, _ := newServer(t, store).do(route.method, route.path, uuid.Nil,
			`{"category":"plumbing","description":"The kitchen sink is leaking"}`)
		if code != http.StatusUnauthorized || store.calls != 0 {
			t.Errorf("%s %s without a token: status %d, store calls %d; want 401 and none",
				route.method, route.path, code, store.calls)
		}
	}
}

// FR-002 / spec scenario 1.3: the owner is the account in the JWT.
func TestCreateIgnoresAnOwnerInTheBody(t *testing.T) {
	store := &fakeStore{}
	code, body := newServer(t, store).do(http.MethodPost, "/orders", ana,
		`{"category":"plumbing","description":"The kitchen sink is leaking",
		  "client_id":"`+bruno.String()+`","owner":"`+bruno.String()+`"}`)
	if code != http.StatusCreated {
		t.Fatalf("status %d (%v), want 201", code, body)
	}
	if store.gotClient != ana {
		t.Fatalf("order stored for %v, want the token's account %v", store.gotClient, ana)
	}
	if body["status"] != "open" || body["client_id"] != nil {
		t.Errorf("reply %v: want status open and no client_id", body)
	}
}

func TestCreateValidatesTheDescription(t *testing.T) {
	cases := []struct {
		name, description string
		ok                bool
		stored            string
	}{
		{"9 characters", strings.Repeat("a", 9), false, ""},
		{"10 characters", strings.Repeat("a", 10), true, strings.Repeat("a", 10)},
		{"500 characters", strings.Repeat("a", 500), true, strings.Repeat("a", 500)},
		{"501 characters", strings.Repeat("a", 501), false, ""},
		{"counted in characters, not bytes", strings.Repeat("ñ", 500), true, strings.Repeat("ñ", 500)},
		{"padding does not count", "   short   ", false, ""},
		{"a NUL byte (Postgres can't store it)", "The kitchen sink\x00 is leaking", false, ""},
		{"stored without padding", "\n  The kitchen sink is leaking  \n", true, "The kitchen sink is leaking"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeStore{}
			payload, _ := json.Marshal(map[string]string{"category": "plumbing", "description": tc.description})
			code, body := newServer(t, store).do(http.MethodPost, "/orders", ana, string(payload))
			if !tc.ok {
				if code != http.StatusUnprocessableEntity || body["error"] != "invalid_description" || store.calls != 0 {
					t.Fatalf("status %d %v, store calls %d; want 422 invalid_description and no store call", code, body, store.calls)
				}
				return
			}
			if code != http.StatusCreated || store.gotDesc != tc.stored {
				t.Fatalf("status %d, stored %q; want 201 and %q", code, store.gotDesc, tc.stored)
			}
		})
	}
}

func TestCreateMapsStoreErrors(t *testing.T) {
	cases := []struct {
		err      error
		status   int
		errorKey string
	}{
		{ErrUnknownCategory, http.StatusUnprocessableEntity, "unknown_category"},
		{ErrInvalidDescription, http.StatusUnprocessableEntity, "invalid_description"},
		{ErrNoAccount, http.StatusForbidden, "no_account"},
		{errors.New("connection refused to 10.0.0.7"), http.StatusInternalServerError, "internal"},
		{fmt.Errorf("query: %w", context.DeadlineExceeded), http.StatusServiceUnavailable, "timeout"},
	}
	for _, tc := range cases {
		code, body := newServer(t, &fakeStore{err: tc.err}).do(http.MethodPost, "/orders", ana,
			`{"category":"plumbing","description":"The kitchen sink is leaking"}`)
		if code != tc.status || body["error"] != tc.errorKey || len(body) != 1 {
			t.Errorf("store error %q: got %d %v; want %d {error: %s} and nothing else", tc.err, code, body, tc.status, tc.errorKey)
		}
	}
}

func TestCreateRejectsBadBodies(t *testing.T) {
	valid := `{"category":"plumbing","description":"The kitchen sink is leaking"}`
	cases := []struct {
		name, body string
		status     int
		errorKey   string
	}{
		{"truncated JSON", `{"category":`, http.StatusBadRequest, "invalid_json"},
		{"data after the object", valid + `{"category":"electrical"}`, http.StatusBadRequest, "invalid_json"},
		{"small trailing value", valid + ` 1`, http.StatusBadRequest, "invalid_json"},
		{"object then 20 KiB of spaces", valid + strings.Repeat(" ", 20<<10),
			http.StatusRequestEntityTooLarge, "body_too_large"},
		{"object then 20 KiB of other data", valid + strings.Repeat(`{"a":1}`, 3<<10),
			http.StatusRequestEntityTooLarge, "body_too_large"},
		{"over 16 KiB", `{"category":"plumbing","description":"` + strings.Repeat("a", maxBody) + `"}`,
			http.StatusRequestEntityTooLarge, "body_too_large"},
		{"NUL in the category", `{"category":"plumbing\u0000","description":"The kitchen sink is leaking"}`,
			http.StatusUnprocessableEntity, "unknown_category"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeStore{}
			code, body := newServer(t, store).do(http.MethodPost, "/orders", ana, tc.body)
			if code != tc.status || body["error"] != tc.errorKey || store.calls != 0 {
				t.Fatalf("got %d %v, store calls %d; want %d %s and no store call", code, body, store.calls, tc.status, tc.errorKey)
			}
		})
	}
}

func TestFeed(t *testing.T) {
	plumbing := "plumbing"
	t.Run("a professional gets their category's orders", func(t *testing.T) {
		store := &fakeStore{category: &plumbing, orders: []Order{{ID: uuid.New(), Category: plumbing, Status: "open"}}}
		code, body := newServer(t, store).do(http.MethodGet, "/orders/feed", ana, "")
		if code != http.StatusOK || body["category"] != plumbing || len(body["orders"].([]any)) != 1 || store.gotClient != ana {
			t.Fatalf("got %d %v for %v", code, body, store.gotClient)
		}
	})
	t.Run("no category is an empty feed, not an error", func(t *testing.T) {
		code, body := newServer(t, &fakeStore{}).do(http.MethodGet, "/orders/feed", ana, "")
		orders, isList := body["orders"].([]any)
		if code != http.StatusOK || body["category"] != nil || !isList || len(orders) != 0 {
			t.Fatalf("got %d %v; want 200, category null, orders []", code, body)
		}
	})
	t.Run("no account", func(t *testing.T) {
		code, body := newServer(t, &fakeStore{err: ErrNoAccount}).do(http.MethodGet, "/orders/feed", ana, "")
		if code != http.StatusForbidden || body["error"] != "no_account" {
			t.Fatalf("got %d %v; want 403 no_account", code, body)
		}
	})
	t.Run("has_more is passed through", func(t *testing.T) {
		for _, more := range []bool{true, false} {
			_, body := newServer(t, &fakeStore{category: &plumbing, hasMore: more}).do(http.MethodGet, "/orders/feed", ana, "")
			if body["has_more"] != more {
				t.Errorf("has_more = %v, want %v (%v)", body["has_more"], more, body)
			}
		}
	})
}

func TestMine(t *testing.T) {
	t.Run("no account", func(t *testing.T) {
		code, body := newServer(t, &fakeStore{err: ErrNoAccount}).do(http.MethodGet, "/orders/mine", ana, "")
		if code != http.StatusForbidden || body["error"] != "no_account" || len(body) != 1 {
			t.Fatalf("got %d %v; want 403 no_account", code, body)
		}
	})
	t.Run("has_more is passed through", func(t *testing.T) {
		for _, more := range []bool{true, false} {
			_, body := newServer(t, &fakeStore{hasMore: more}).do(http.MethodGet, "/orders/mine", ana, "")
			if body["has_more"] != more {
				t.Errorf("has_more = %v, want %v (%v)", body["has_more"], more, body)
			}
		}
	})
}

func TestMineAsksForTheCaller(t *testing.T) {
	store := &fakeStore{}
	code, body := newServer(t, store).do(http.MethodGet, "/orders/mine?client_id="+bruno.String(), ana, "")
	orders, isList := body["orders"].([]any)
	if code != http.StatusOK || store.gotClient != ana || !isList || len(orders) != 0 {
		t.Fatalf("got %d %v for %v; want 200, [] and the token's account", code, body, store.gotClient)
	}
}

func TestRouteWithoutMiddlewareFailsClosed(t *testing.T) {
	store := &fakeStore{}
	mux := http.NewServeMux()
	NewHandler(store).Routes(mux, func(h http.Handler) http.Handler { return h }) // wiring mistake
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/orders/mine", nil))
	if rec.Code != http.StatusInternalServerError || store.calls != 0 {
		t.Fatalf("status %d, store calls %d; want 500 and no data access", rec.Code, store.calls)
	}
}
