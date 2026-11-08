// Package authtest signs tokens shaped like Supabase Auth's, so every test
// of code behind auth.Middleware uses the same claims. Only tests import it.
package authtest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// Issuer is the `iss` of every token signed here.
const Issuer = "http://auth.example.test/auth/v1"

// NewKey returns a fresh P-256 key, the curve ES256 uses.
func NewKey(t testing.TB) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// Claims is what Supabase Auth puts in a signed-in user's access token.
func Claims(sub uuid.UUID) jwt.MapClaims {
	return jwt.MapClaims{
		"iss":  Issuer,
		"aud":  "authenticated",
		"sub":  sub.String(),
		"role": "authenticated",
		"exp":  time.Now().Add(time.Hour).Unix(),
	}
}

// Sign signs claims with method and key.
func Sign(t testing.TB, method jwt.SigningMethod, key any, claims jwt.MapClaims) string {
	t.Helper()
	s, err := jwt.NewWithClaims(method, claims).SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
