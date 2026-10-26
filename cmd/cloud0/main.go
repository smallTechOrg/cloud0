// Command cloud0 is the single binary: it serves the embedded dashboard and
// the JSON API, and manages the SQLite schema.
//
//	cloud0 serve            start the HTTP server on CLOUD0_BIND:CLOUD0_PORT
//	cloud0 migrate up       apply pending migrations to CLOUD0_DB_PATH
//	cloud0 migrate status   list migrations with their applied time
//	cloud0 version          print the version
//
// serve and migrate load ./.env from the working directory when present
// (never overriding variables already set).
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/smallTechOrg/cloud0/internal/config"
	"github.com/smallTechOrg/cloud0/internal/httpapi"
	"github.com/smallTechOrg/cloud0/internal/logging"
	"github.com/smallTechOrg/cloud0/internal/store"
	"github.com/smallTechOrg/cloud0/internal/webui"
)

// version is overridable at build time: -ldflags "-X main.version=1.2.3".
var version = "0.1.0"

const (
	exitUsage   = 1
	exitConfig  = 2
	exitPending = 3
	exitRuntime = 4
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		usage()
		return exitUsage
	}
	switch args[0] {
	case "version", "--version", "-v":
		fmt.Printf("cloud0 %s\n", version)
		return 0
	case "serve":
		return serve()
	case "migrate":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "cloud0: migrate needs a subcommand: up | status")
			return exitUsage
		}
		switch args[1] {
		case "up":
			return migrateUp()
		case "status":
			return migrateStatus()
		}
		fmt.Fprintf(os.Stderr, "cloud0: unknown migrate subcommand %q (want up | status)\n", args[1])
		return exitUsage
	case "help", "-h", "--help":
		usage()
		return 0
	}
	fmt.Fprintf(os.Stderr, "cloud0: unknown command %q\n", args[0])
	usage()
	return exitUsage
}

func usage() {
	fmt.Fprintf(os.Stderr, `cloud0 %s — GCP weekly cost review dashboard

Usage:
  cloud0 serve            start the server (CLOUD0_BIND:CLOUD0_PORT, default 127.0.0.1:8087)
  cloud0 migrate up       apply pending migrations to CLOUD0_DB_PATH
  cloud0 migrate status   list migrations and when they were applied
  cloud0 version          print the version

Configuration comes from the environment; ./.env is loaded when present.
See .env.example for every variable.
`, version)
}

// loadConfig loads ./.env and builds the validated config, or prints the
// offending variables and returns false.
func loadConfig() (config.Config, bool) {
	if err := config.LoadDotEnv(); err != nil {
		fmt.Fprintf(os.Stderr, "cloud0: %v\n", err)
		return config.Config{}, false
	}
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cloud0: configuration is invalid:\n%v\nCopy .env.example to .env and fill the variables named above.\n", err)
		return config.Config{}, false
	}
	return cfg, true
}

func openStore(ctx context.Context, cfg config.Config) (*store.Store, bool) {
	st, err := store.Open(ctx, cfg.DBPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cloud0: %v\n", err)
		return nil, false
	}
	return st, true
}

func migrateUp() int {
	cfg, ok := loadConfig()
	if !ok {
		return exitConfig
	}
	ctx := context.Background()
	st, ok := openStore(ctx, cfg)
	if !ok {
		return exitRuntime
	}
	defer st.Close()
	applied, err := st.Migrate(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cloud0: %v\n", err)
		return exitRuntime
	}
	if len(applied) == 0 {
		fmt.Printf("cloud0: no pending migrations (%s)\n", cfg.DBPath)
	}
	for _, m := range applied {
		fmt.Printf("cloud0: applied %s\n", m.Source)
	}
	return 0
}

func migrateStatus() int {
	cfg, ok := loadConfig()
	if !ok {
		return exitConfig
	}
	ctx := context.Background()
	st, ok := openStore(ctx, cfg)
	if !ok {
		return exitRuntime
	}
	defer st.Close()
	infos, err := st.MigrationStatus(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cloud0: %v\n", err)
		return exitRuntime
	}
	fmt.Printf("cloud0: migrations for %s\n", cfg.DBPath)
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 4, ' ', 0)
	fmt.Fprintln(tw, "Applied At\tMigration")
	pending := 0
	for _, m := range infos {
		at := "Pending"
		if m.Applied {
			at = m.AppliedAt.Format(time.RFC3339)
		} else {
			pending++
		}
		fmt.Fprintf(tw, "%s\t%s\n", at, m.Source)
	}
	_ = tw.Flush()
	if pending > 0 {
		fmt.Printf("cloud0: %d pending migration(s) — run ./bin/cloud0 migrate up\n", pending)
	}
	return 0
}

func serve() int {
	cfg, ok := loadConfig()
	if !ok {
		return exitConfig
	}
	logger := logging.New(cfg.LogLevel)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger.Info("config", slog.Any("config", cfg.Redacted()), slog.String("version", version))

	st, ok := openStore(ctx, cfg)
	if !ok {
		return exitRuntime
	}
	defer st.Close()
	pending, err := st.Pending(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cloud0: %v\n", err)
		return exitRuntime
	}
	if pending > 0 {
		fmt.Fprintf(os.Stderr, "cloud0: %d pending migration(s) — run `./bin/cloud0 migrate up`\n", pending)
		return exitPending
	}

	// Step 3 of the startup sequence (BigQuery dry-run) arrives with Phase 1
	// slice S3; the scaffold reports bigquery: null in /api/v1/health.

	deps := httpapi.Deps{Config: cfg, Store: st, Logger: logger, Version: version}
	srv := httpapi.New(deps)

	ln, err := net.Listen("tcp", cfg.Addr())
	if err != nil {
		fmt.Fprintf(os.Stderr, "cloud0: listen %s: %v (is another process on the port? change CLOUD0_PORT)\n", cfg.Addr(), err)
		return exitRuntime
	}
	logger.Info("listening",
		slog.String("addr", "http://"+ln.Addr().String()+"/"),
		slog.String("ui", string(webui.CurrentMode())),
		slog.String("version", version))

	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()

	select {
	case <-ctx.Done():
		logger.Info("shutdown", slog.String("reason", "signal"))
	case err := <-errc:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("serve", slog.String("error", err.Error()))
			return exitRuntime
		}
		return 0
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("shutdown", slog.String("error", err.Error()))
		return exitRuntime
	}
	return 0
}
