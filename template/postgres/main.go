package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"strconv"
	"syscall"
	"time"

	"github.com/17xande-dev/gowebkit/template/postgres/internal/config"
	"github.com/17xande-dev/gowebkit/template/postgres/internal/db"
	"github.com/17xande-dev/gowebkit/template/postgres/internal/handler"
	"github.com/17xande-dev/gowebkit/template/postgres/internal/mail"
	"github.com/17xande-dev/gowebkit/template/postgres/internal/store"
	"github.com/17xande-dev/password"
)

// setPasswordConcurrency sizes the hashing cap for the container's memory. It
// is a variable so tests can see that every mode that hashes calls it.
var setPasswordConcurrency = password.SetConcurrency

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	return execute(args, os.Stdin, stdout, stderr)
}

func execute(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("server", flag.ContinueOnError)
	flags.SetOutput(stderr)
	check := flags.Bool("check-config", false, "validate configuration and exit")
	up := flags.Bool("migrate", false, "apply pending database migrations and exit")
	status := flags.Bool("migrate-status", false, "read database migration status and exit")
	down := flags.Bool("migrate-down", false, "roll back one database migration and exit")
	reset := flags.String("reset-password", "", "set a new password for the account with this `email`, read from standard input, and exit")
	passwordFile := flags.String("password-file", "", "with -reset-password, read the new password from this `file` instead of standard input")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return errors.New("invalid arguments")
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	modes := 0
	for _, set := range []bool{*check, *up, *status, *down, *reset != ""} {
		if set {
			modes++
		}
	}
	if modes > 1 {
		return errors.New("choose only one command mode")
	}
	if *passwordFile != "" && *reset == "" {
		return errors.New("-password-file is only used with -reset-password")
	}

	// All modes share this check, before building services or binding a socket.
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("configuration: %w", err)
	}
	if *check {
		_, err := fmt.Fprintln(stdout, "Configuration OK")
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch {
	case *up:
		if err := db.Migrate(ctx, cfg.DB); err != nil {
			return err
		}
		_, err := fmt.Fprintln(stdout, "Database migrations applied")
		return err
	case *status:
		return printMigrationStatus(ctx, cfg.DB, stdout)
	case *down:
		if err := db.MigrateDown(ctx, cfg.DB); err != nil {
			return err
		}
		_, err := fmt.Fprintln(stdout, "Rolled back one database migration")
		return err
	case *reset != "":
		return resetPassword(ctx, cfg, *reset, *passwordFile, stdin, stdout, stderr)
	default:
		return runServer(ctx, cfg, newLogger(stdout, cfg), db.Open, time.Hour)
	}
}

func printMigrationStatus(ctx context.Context, cfg config.DB, out io.Writer) error {
	status, err := db.Status(ctx, cfg)
	if err != nil {
		return err
	}
	for _, migration := range status {
		state := "pending"
		if migration.Applied {
			state = "applied " + migration.AppliedAt.UTC().Format(time.RFC3339)
		}
		if _, err := fmt.Fprintf(out, "%04d %s\n", migration.Version, state); err != nil {
			return err
		}
	}
	return nil
}

func runServer(ctx context.Context, cfg config.Config, log *slog.Logger, open func(context.Context, config.DB) (*db.DB, error), maintenanceInterval time.Duration) (err error) {
	d, err := open(ctx, cfg.DB)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, d.Close()) }()
	accounts := store.New(d)
	sender, delivering, err := mail.New(cfg.SMTP, log)
	if err != nil {
		return fmt.Errorf("initialize mail: %w", err)
	}
	opts := []handler.Option{handler.WithMail(sender, delivering)}
	backups := d.Backups(cfg.DB)
	if backups != nil {
		opts = append(opts, handler.WithBackups(backups.Newest))
	} else {
		opts = append(opts, handler.WithoutBackups(db.BackupsNote))
	}
	srv, err := newServer(cfg, log, accounts, d.Ping, opts...)
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	setPasswordConcurrency(cfg.PasswordConcurrency)
	if err := ensureSetupToken(ctx, accounts, log, cfg.SetupToken); err != nil {
		return errors.Join(fmt.Errorf("initialize first-account setup: %w", err), ln.Close())
	}
	log.Info("server listening", "address", ln.Addr().String(), "version", buildVersion())

	sweepCtx, stopSweep := context.WithCancel(ctx)
	sweepTicker := time.NewTicker(maintenanceInterval)
	sweepDone := make(chan struct{})
	go func() {
		defer close(sweepDone)
		runSessionSweep(sweepCtx, log, accounts.SweepSessions, cfg.SessionLifetime, sweepTicker.C)
	}()
	defer func() {
		stopSweep()
		sweepTicker.Stop()
		// A server error can stop the loop even before the signal context ends.
		<-sweepDone
	}()

	if backups != nil {
		backupCtx, cancel := context.WithCancel(ctx)
		ticker := time.NewTicker(maintenanceInterval)
		done := make(chan struct{})
		go func() {
			defer close(done)
			runBackups(backupCtx, log, backups.Run, time.Now(), ticker.C)
		}()
		defer func() {
			// Stop and join background work before closing its database pool.
			cancel()
			ticker.Stop()
			<-done
		}()
	} else {
		log.Info("the application makes no database backups", "note", db.BackupsNote)
	}
	return serve(ctx, srv, ln, cfg.ShutdownTimeout)
}

func newLogger(out io.Writer, cfg config.Config) *slog.Logger {
	opts := &slog.HandlerOptions{Level: cfg.LogLevel}
	if cfg.LogFormat == "text" {
		return slog.New(slog.NewTextHandler(out, opts))
	}
	return slog.New(slog.NewJSONHandler(out, opts))
}

func buildVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "devel"
	}
	if v := info.Main.Version; v != "" && v != "(devel)" {
		return v
	}
	for _, setting := range info.Settings {
		if setting.Key == "vcs.revision" {
			return setting.Value
		}
	}
	return "devel"
}

func newServer(cfg config.Config, log *slog.Logger, accounts *store.Store, ping func(context.Context) error, opts ...handler.Option) (*http.Server, error) {
	h, _, err := handler.New(cfg, log, accounts, ping, opts...)
	if err != nil {
		return nil, fmt.Errorf("initialize pages: %w", err)
	}
	return &http.Server{
		Addr:              ":" + strconv.Itoa(cfg.Port),
		Handler:           h,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    64 << 10,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelError),
	}, nil
}

func serve(ctx context.Context, srv *http.Server, ln net.Listener, timeout time.Duration) error {
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ln) }()
	select {
	case err := <-done:
		return serverError(err)
	case <-ctx.Done():
	}

	// The signal has canceled ctx already. In-flight requests get a separate
	// deadline, not the cancellation that stops new work.
	drain, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	err := srv.Shutdown(drain)
	if err != nil {
		_ = srv.Close()
	}
	return errors.Join(err, serverError(<-done))
}

func serverError(err error) error {
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
