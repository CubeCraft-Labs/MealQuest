// Command server runs the MealQuest HTTP service.
package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/auth"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/config"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/frontend"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/server"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/migrations"
)

// version is set at build time with -ldflags "-X main.version=<version>".
var version = "dev"

func main() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(cfg, logger); err != nil {
		logger.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

func run(cfg config.Config, logger *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := openStore(ctx, cfg.DataDir)
	if err != nil {
		return err
	}
	defer func() {
		if err := db.Close(); err != nil {
			logger.Error("close database", "error", err)
		}
	}()

	authFlow, err := auth.New(ctx, auth.Config{
		Issuer:       cfg.OIDCIssuer.String(),
		ClientID:     cfg.OIDCClientID,
		ClientSecret: cfg.OIDCClientSecret.Reveal(),
		PublicURL:    cfg.PublicURL,
		SecretKey:    []byte(cfg.SecretKey.Reveal()),
	}, sessionsPending{}, logger)
	if err != nil {
		return err
	}

	router, err := server.NewRouter(logger, version, authFlow, frontend.Assets())
	if err != nil {
		return err
	}
	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelError),
	}

	errc := make(chan error, 1)
	go func() {
		logListening(logger, cfg)
		errc <- srv.ListenAndServe()
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}

	logger.Info("shutting down", "timeout", cfg.ShutdownTimeout.String())
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	if err := <-errc; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// bootMigrations is the migration set applied at boot. Tests replace it to
// prove a failing migration stops the boot.
var bootMigrations fs.FS = migrations.FS

// openStore opens the database in dataDir and applies bootMigrations. On a
// migration failure it closes the database and returns the error.
func openStore(ctx context.Context, dataDir string) (*store.Store, error) {
	db, err := store.Open(ctx, dataDir)
	if err != nil {
		return nil, err
	}
	if err := store.Migrate(ctx, db.Writer, bootMigrations); err != nil {
		return nil, errors.Join(err, db.Close())
	}
	return db, nil
}

// sessionsPending refuses every login until the session layer (Feature 03
// task 2) replaces it.
type sessionsPending struct{}

func (sessionsPending) StartSession(http.ResponseWriter, *http.Request, auth.Claims) error {
	return errors.New("session layer not implemented")
}

// logListening logs the startup line. Only the public URL's origin is logged
// so no credential-bearing URL component can reach the log.
func logListening(logger *slog.Logger, cfg config.Config) {
	logger.Info("listening",
		"addr", cfg.HTTPAddr,
		"public_origin", cfg.PublicOrigin(),
		"data_dir", cfg.DataDir,
		"version", version,
	)
}
