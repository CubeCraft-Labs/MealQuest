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

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/aisettings"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/harness"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/skills"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/auth"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/config"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/dinners"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/forwarded"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/frontend"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/household"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/jobs"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/recipes"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/secretbox"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/server"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/suggest"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/migrations"
)

// version is set at build time with -ldflags "-X main.version=<version>".
var version = "dev"

// jobWorkers is the number of background jobs run at once.
const jobWorkers = 2

func main() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	logger := slog.New(forwarded.LogHandler(slog.NewJSONHandler(os.Stdout, nil)))
	if err := run(cfg, logger); err != nil {
		logger.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

func run(cfg config.Config, logger *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	box, err := openSecretBox(cfg.SecretKey)
	if err != nil {
		return err
	}

	db, err := openStore(ctx, cfg.DataDir)
	if err != nil {
		return err
	}
	defer func() {
		if err := db.Close(); err != nil {
			logger.Error("close database", "error", err)
		}
	}()

	if err := seedDefaults(ctx, db, cfg.DefaultTZ); err != nil {
		return err
	}
	settings := household.NewSettingsService(db)
	runner, _, err := newJobRunner(db, logger)
	if err != nil {
		return err
	}

	// One fetcher serves pages and photos, so its per-host spacing and
	// page cache cover both.
	fetcher := recipes.NewFetcher()
	photos, err := recipes.NewPhotoCache(fetcher, db, cfg.DataDir)
	if err != nil {
		return err
	}
	search, err := recipes.NewSearchSettingsService(db, box, cfg.SearXNGURL)
	if err != nil {
		return fmt.Errorf("%s: %w", config.EnvSearXNGURL, err)
	}

	ai, err := aisettings.New(db, box, cfg.PublicURL.String())
	if err != nil {
		return err
	}
	guard, err := harness.NewGuard(harness.DefaultMaxCalls)
	if err != nil {
		return err
	}
	grounder, err := recipes.NewGrounder(db, search, fetcher, photos, normalizer(ai, guard))
	if err != nil {
		return err
	}
	dinnerSvc, err := dinners.NewService(db, grounder, runner, logger)
	if err != nil {
		return err
	}
	suggestSvc, err := suggest.NewService(db, grounder, sideQuests(ai, guard), runner, logger)
	if err != nil {
		return err
	}

	sessions, err := auth.NewSessionManager(db, cfg.SessionIdle, cfg.SessionMax)
	if err != nil {
		return err
	}
	authFlow, err := auth.New(ctx, auth.Config{
		Issuer:        cfg.OIDCIssuer.String(),
		ClientID:      cfg.OIDCClientID,
		ClientSecret:  cfg.OIDCClientSecret.Reveal(),
		PublicURL:     cfg.PublicURL,
		SecretKey:     []byte(cfg.SecretKey.Reveal()),
		AllowedGroups: cfg.OIDCAllowedGroups,
	}, sessions, logger)
	if err != nil {
		return err
	}

	router, err := server.NewRouter(logger, version, cfg.TrustedProxy, authFlow, sessions, settings, search, ai, dinnerSvc, suggestSvc, runner, photos, frontend.Assets())
	if err != nil {
		return err
	}

	// Start fails the jobs a previous process left running before any
	// worker runs. The workers stop, and are waited for, before the
	// database closes.
	jobsCtx, stopJobs := context.WithCancel(ctx)
	defer func() {
		stopJobs()
		runner.Wait()
	}()
	if err := runner.Start(jobsCtx); err != nil {
		return err
	}
	// With those jobs failed, a dinner still pending with no grounding job
	// queued or running is failed as interrupted, before any request can
	// create one.
	if _, err := dinnerSvc.FailInterruptedGroundings(ctx); err != nil {
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

// openSecretBox builds the secret box from the configured key, naming the
// variable whose file holds a bad key. The key itself is never reported.
func openSecretBox(key config.Secret) (*secretbox.Box, error) {
	box, err := secretbox.New(key.Reveal())
	if err != nil {
		return nil, fmt.Errorf("%s: %w", config.EnvSecretKeyFile, err)
	}
	return box, nil
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

// seedSettings creates the household settings on first boot, naming the
// variable that supplies the zone when it is missing or invalid. Once the
// settings exist the variable is ignored.
func seedSettings(ctx context.Context, db *store.Store, defaultTZ string) error {
	err := household.SeedSettings(ctx, db, defaultTZ)
	if errors.Is(err, household.ErrNoDefaultZone) || errors.Is(err, household.ErrInvalidDefaultZone) {
		return fmt.Errorf("%s: a valid IANA zone is required on first boot to create the household settings: %w", config.EnvDefaultTZ, err)
	}
	return err
}

// seedDefaults creates the household settings, AI settings and notification
// preferences that do not exist yet. Existing rows are never changed.
func seedDefaults(ctx context.Context, db *store.Store, defaultTZ string) error {
	if err := seedSettings(ctx, db, defaultTZ); err != nil {
		return err
	}
	if err := household.SeedAISettings(ctx, db); err != nil {
		return err
	}
	return household.SeedNotificationPrefs(ctx, db)
}

// jobKinds enqueues the framework's job kinds.
type jobKinds struct {
	builtins *jobs.Builtins
}

// newJobRunner returns the job runner with every job kind registered, ready
// to Start.
func newJobRunner(db *store.Store, logger *slog.Logger) (*jobs.Runner, *jobKinds, error) {
	runner, err := jobs.NewRunner(db, logger, jobWorkers)
	if err != nil {
		return nil, nil, err
	}
	kinds, err := registerJobKinds(runner)
	if err != nil {
		return nil, nil, err
	}
	return runner, kinds, nil
}

// registerJobKinds registers the framework's job kinds on runner. A feature
// service registers its own kinds when it is built, before the runner
// starts (dinners.NewService registers dinners.KindGroundDinner and
// suggest.NewService suggest.KindSuggestionBatch).
func registerJobKinds(runner *jobs.Runner) (*jobKinds, error) {
	builtins, err := jobs.RegisterBuiltins(runner)
	if err != nil {
		return nil, err
	}
	return &jobKinds{builtins: builtins}, nil
}

// normalizer runs normalize_recipe with the AI settings current at each
// run, sharing guard.
func normalizer(ai *aisettings.Service, guard *harness.Guard) recipes.Normalizer {
	return func(ctx context.Context, scope string, in skills.RecipeText) (skills.NormalizedRecipe, error) {
		cfg, err := ai.HarnessConfig(ctx, guard)
		if err != nil {
			return skills.NormalizedRecipe{}, err
		}
		return skills.NormalizeRecipe(ctx, cfg, scope, in)
	}
}

// sideQuests runs suggest_side_quests with the AI settings current at each
// run, sharing guard.
func sideQuests(ai *aisettings.Service, guard *harness.Guard) suggest.Suggester {
	return func(ctx context.Context, scope string, in skills.SideQuestsRequest) (skills.SideQuests, error) {
		cfg, err := ai.HarnessConfig(ctx, guard)
		if err != nil {
			return skills.SideQuests{}, err
		}
		return skills.SuggestSideQuests(ctx, cfg, scope, in)
	}
}

// logListening logs the startup line. Only the public URL's origin is logged
// so no credential-bearing URL component can reach the log.
func logListening(logger *slog.Logger, cfg config.Config) {
	logger.Info("listening",
		"addr", cfg.HTTPAddr,
		"public_origin", cfg.PublicOrigin(),
		"trusted_proxy", cfg.TrustedProxy,
		"data_dir", cfg.DataDir,
		"version", version,
	)
}
