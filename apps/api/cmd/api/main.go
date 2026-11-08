// Command api serves HubTop Lite's HTTP API (spec 001).
//
// Configuration comes from the environment, never from flags or files that
// could be committed:
//
//	DATABASE_URL  Postgres connection string (required)
//	SUPABASE_URL  base URL of Supabase, e.g. http://127.0.0.1:55321 (required);
//	              tokens must be issued by $SUPABASE_URL/auth/v1, and its public
//	              keys are read from that issuer's JWKS
//	ADDR          listen address (default :8080)
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/MicahParks/keyfunc/v3"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/miguelangelflores1993/hubtop-engineering/apps/api/internal/auth"
	"github.com/miguelangelflores1993/hubtop-engineering/apps/api/internal/orders"
)

func main() {
	if err := run(); err != nil {
		slog.Error("api stopped", "err", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	dbURL, supabaseURL := os.Getenv("DATABASE_URL"), os.Getenv("SUPABASE_URL")
	if dbURL == "" || supabaseURL == "" {
		return errors.New("DATABASE_URL and SUPABASE_URL are required")
	}
	addr := os.Getenv("ADDR")
	if addr == "" {
		addr = ":8080"
	}

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		return fmt.Errorf("database config: %w", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		return fmt.Errorf("database: %w", err)
	}

	// The JWKS is fetched now and refreshed in the background, so a key
	// rotation in Auth reaches the API without a restart.
	issuer := strings.TrimRight(supabaseURL, "/") + "/auth/v1"
	jwks, err := keyfunc.NewDefaultCtx(ctx, []string{issuer + "/.well-known/jwks.json"})
	if err != nil {
		return fmt.Errorf("jwks: %w", err)
	}
	verifier := auth.NewVerifier(jwks.Keyfunc, issuer)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	orders.NewHandler(orders.NewPGStore(pool)).Routes(mux, verifier.Middleware)

	// Timeouts so a slow or idle client can't hold a connection forever.
	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	slog.Info("api listening", "addr", addr, "issuer", issuer)

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	stop() // a second Ctrl-C now kills the process instead of waiting
	// Finish the requests in flight, then stop.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}
