package auth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

const issuer = "http://auth.example.test/auth/v1"

var ana = uuid.MustParse("11111111-1111-4111-8111-111111111111")

func newKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// validClaims is what Supabase Auth puts in a signed-in user's token.
func validClaims() jwt.MapClaims {
	return jwt.MapClaims{
		"iss":  issuer,
		"aud":  "authenticated",
		"sub":  ana.String(),
		"role": "authenticated",
		"exp":  time.Now().Add(time.Hour).Unix(),
	}
}

func sign(t *testing.T, method jwt.SigningMethod, key any, claims jwt.MapClaims) string {
	t.Helper()
	s, err := jwt.NewWithClaims(method, claims).SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestMiddleware(t *testing.T) {
	trusted := newKey(t)
	pubDER, err := x509.MarshalPKIXPublicKey(&trusted.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	// A naive key lookup that hands the public key's bytes to whoever asks for
	// HMAC. Only the pinned algorithm stops an attacker from signing with the
	// (public) key: without it, the HS256 case below passes verification.
	naive := func(tok *jwt.Token) (any, error) {
		if _, ok := tok.Method.(*jwt.SigningMethodHMAC); ok {
			return pubDER, nil
		}
		return &trusted.PublicKey, nil
	}
	verifier := NewVerifier(naive, issuer)

	with := func(change func(jwt.MapClaims)) jwt.MapClaims {
		c := validClaims()
		change(c)
		return c
	}
	bearer := func(token string) string { return "Bearer " + token }

	cases := []struct {
		name   string
		header string
		want   int
	}{
		{"valid token", bearer(sign(t, jwt.SigningMethodES256, trusted, validClaims())), http.StatusOK},
		{"scheme is case-insensitive", "bearer " + sign(t, jwt.SigningMethodES256, trusted, validClaims()), http.StatusOK},
		{"no header", "", http.StatusUnauthorized},
		{"not a bearer token", "Basic YW5hOmRlbW8=", http.StatusUnauthorized},
		{"empty bearer", "Bearer ", http.StatusUnauthorized},
		{"garbage", bearer("not.a.jwt"), http.StatusUnauthorized},
		{"expired", bearer(sign(t, jwt.SigningMethodES256, trusted,
			with(func(c jwt.MapClaims) { c["exp"] = time.Now().Add(-time.Hour).Unix() }))), http.StatusUnauthorized},
		{"no expiry", bearer(sign(t, jwt.SigningMethodES256, trusted,
			with(func(c jwt.MapClaims) { delete(c, "exp") }))), http.StatusUnauthorized},
		{"forged: signed by another key", bearer(sign(t, jwt.SigningMethodES256, newKey(t), validClaims())), http.StatusUnauthorized},
		{"forged: alg none", bearer(sign(t, jwt.SigningMethodNone, jwt.UnsafeAllowNoneSignatureType, validClaims())), http.StatusUnauthorized},
		{"forged: HS256 keyed with the public key", bearer(sign(t, jwt.SigningMethodHS256, pubDER, validClaims())), http.StatusUnauthorized},
		{"another issuer", bearer(sign(t, jwt.SigningMethodES256, trusted,
			with(func(c jwt.MapClaims) { c["iss"] = "http://evil.example.test/auth/v1" }))), http.StatusUnauthorized},
		{"anon audience", bearer(sign(t, jwt.SigningMethodES256, trusted,
			with(func(c jwt.MapClaims) { c["aud"] = "anon" }))), http.StatusUnauthorized},
		{"no subject (an API key, not a person)", bearer(sign(t, jwt.SigningMethodES256, trusted,
			with(func(c jwt.MapClaims) { delete(c, "sub") }))), http.StatusUnauthorized},
		{"subject is not a uuid", bearer(sign(t, jwt.SigningMethodES256, trusted,
			with(func(c jwt.MapClaims) { c["sub"] = "ana" }))), http.StatusUnauthorized},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reached := false
			h := verifier.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				reached = true
				if id, ok := AccountID(r.Context()); !ok || id != ana {
					t.Errorf("AccountID = %v, %v; want %v", id, ok, ana)
				}
			}))
			req := httptest.NewRequest(http.MethodGet, "/orders/feed", nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d", rec.Code, tc.want)
			}
			if reached != (tc.want == http.StatusOK) {
				t.Fatalf("handler reached = %v; a rejected request must not reach it", reached)
			}
			if tc.want == http.StatusUnauthorized && rec.Header().Get("WWW-Authenticate") == "" {
				t.Error("401 without WWW-Authenticate")
			}
		})
	}
}

func TestAccountIDWithoutMiddleware(t *testing.T) {
	if _, ok := AccountID(httptest.NewRequest(http.MethodGet, "/", nil).Context()); ok {
		t.Fatal("AccountID reported a caller on a request that never went through Middleware")
	}
}
