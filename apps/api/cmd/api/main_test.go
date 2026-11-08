package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/miguelangelflores1993/hubtop-engineering/apps/api/internal/auth/authtest"
)

func TestLoadJWKSFailsWithoutKeys(t *testing.T) {
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer broken.Close()
	if _, err := loadJWKS(context.Background(), broken.URL); err == nil {
		t.Fatal("loadJWKS succeeded against a server that answers 500; want an error")
	}

	empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"keys":[]}`))
	}))
	defer empty.Close()
	if _, err := loadJWKS(context.Background(), empty.URL); err == nil {
		t.Fatal("loadJWKS succeeded with an empty key set; want an error")
	}
}

func TestLoadJWKSLoadsAKey(t *testing.T) {
	key := authtest.NewKey(t)
	b64 := base64.RawURLEncoding.EncodeToString
	set, err := json.Marshal(map[string]any{"keys": []map[string]string{{
		"kty": "EC", "crv": "P-256", "alg": "ES256", "use": "sig", "kid": "test-key",
		"x": b64(key.PublicKey.X.FillBytes(make([]byte, 32))),
		"y": b64(key.PublicKey.Y.FillBytes(make([]byte, 32))),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(set)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel() // stops the background refresh
	jwks, err := loadJWKS(ctx, srv.URL)
	if err != nil || jwks == nil {
		t.Fatalf("loadJWKS = %v, %v; want a key set and no error", jwks, err)
	}
}

func TestWithTimeoutSetsADeadline(t *testing.T) {
	const d = 2 * time.Second
	called := false
	h := withTimeout(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		called = true
		deadline, ok := r.Context().Deadline()
		if !ok {
			t.Error("the request has no deadline")
			return
		}
		if left := time.Until(deadline); left <= 0 || left > d {
			t.Errorf("deadline is %v from now; want within (0, %v]", left, d)
		}
	}), d)
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if !called {
		t.Fatal("the inner handler was not called")
	}
}
