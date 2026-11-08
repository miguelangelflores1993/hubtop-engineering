// Package auth turns a bearer token into the caller's account id.
//
// Identity comes only from a JWT that this process verified itself
// (AGENTS.md: never from a request body or a query parameter). Supabase Auth
// signs tokens with ES256 and publishes the public key as a JWKS, so the API
// holds no secret: it can check a signature but never mint one.
package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// Verifier checks a token and returns the account id in its `sub` claim.
type Verifier struct {
	parser  *jwt.Parser
	keyFunc jwt.Keyfunc
}

// NewVerifier accepts only tokens that are signed with ES256 by a key that
// keyFunc returns, issued by issuer for the `authenticated` audience, and not
// expired. Pinning the algorithm stops "alg: none" and the HS256-with-the-
// public-key confusion attack; requiring exp stops tokens that never expire.
func NewVerifier(keyFunc jwt.Keyfunc, issuer string) *Verifier {
	return &Verifier{
		parser: jwt.NewParser(
			jwt.WithValidMethods([]string{"ES256"}),
			jwt.WithIssuer(issuer),
			jwt.WithAudience("authenticated"),
			jwt.WithExpirationRequired(),
			jwt.WithLeeway(30*time.Second), // clock skew between Auth and the API
		),
		keyFunc: keyFunc,
	}
}

// Verify returns the account id of a valid token. The `sub` must be a uuid:
// it is the primary key of app.accounts, and anything else (an anon or
// service key has no sub) is not a person.
func (v *Verifier) Verify(token string) (uuid.UUID, error) {
	var claims jwt.RegisteredClaims
	if _, err := v.parser.ParseWithClaims(token, &claims, v.keyFunc); err != nil {
		return uuid.Nil, err
	}
	return uuid.Parse(claims.Subject)
}

type ctxKey struct{}

// Middleware rejects any request without a valid bearer token with 401
// before the handler runs, so no handler can touch data for an anonymous
// caller. It stores the account id in the request context.
func (v *Verifier) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
		if !ok || !strings.EqualFold(scheme, "Bearer") || token == "" {
			unauthorized(w)
			return
		}
		id, err := v.Verify(token)
		if err != nil {
			unauthorized(w) // never echo why: it would help a forger
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, id)))
	})
}

// AccountID is the verified caller. Handlers behind Middleware always have it;
// ok is false only if a route was wired without the middleware.
func AccountID(ctx context.Context) (id uuid.UUID, ok bool) {
	id, ok = ctx.Value(ctxKey{}).(uuid.UUID)
	return id, ok
}

func unauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="hubtop-lite"`)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized"})
}
