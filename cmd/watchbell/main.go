package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/watchbell/watchbell/internal/api"
	"github.com/watchbell/watchbell/internal/auth"
	"github.com/watchbell/watchbell/internal/checker"
	"github.com/watchbell/watchbell/internal/config"
	"github.com/watchbell/watchbell/internal/maintenance"
	"github.com/watchbell/watchbell/internal/notifier"
	"github.com/watchbell/watchbell/internal/scheduler"
	"github.com/watchbell/watchbell/internal/store"
	"golang.org/x/term"
)

var (
	version   = "dev"
	commit    = "unknown"
	buildDate = "unknown"
)

func main() {
	if len(os.Args) == 3 && os.Args[1] == "hash-password" {
		hash, err := auth.HashPassword(os.Args[2])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Println(hash)
		return
	}
	if len(os.Args) >= 2 && os.Args[1] == "set-password" {
		if len(os.Args) != 2 {
			fmt.Fprintln(os.Stderr, "usage: watchbell set-password (the password is read from standard input)")
			os.Exit(2)
		}
		password, err := readConfirmedPassword()
		if err == nil {
			err = setPersistedPassword(password)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Println("administrator password updated; a running WatchBell server will reload it shortly")
		return
	}
	if len(os.Args) == 2 && os.Args[1] == "version" {
		fmt.Printf("watchbell %s (commit %s, built %s)\n", version, commit, buildDate)
		return
	}
	if len(os.Args) == 2 && os.Args[1] == "healthcheck" {
		if err := runHealthcheck(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	cfg := config.FromEnv()
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := store.Open(ctx, cfg.DBPath)
	if err != nil {
		logger.Error("open store", "error", err)
		os.Exit(1)
	}
	defer db.Close()
	retention := config.RetentionFromEnv()
	runtimeDefaults := store.RuntimeSettings{
		SessionTTL: cfg.Auth.SessionTTL,
		HistoryRetention: store.HistoryRetentionPolicy{
			EventAge: retention.EventAge, CheckRunAge: retention.CheckRunAge,
			NotificationAttemptAge: retention.NotificationAttemptAge,
			AuditLogAge:            retention.AuditLogAge, BatchSize: retention.BatchSize,
		},
	}
	runtimeSettings, err := db.GetRuntimeSettings(ctx, runtimeDefaults)
	if err != nil {
		logger.Error("load runtime settings", "error", err)
		os.Exit(1)
	}
	cfg.Auth.SessionTTL = runtimeSettings.SessionTTL
	if persistedHash, exists, err := db.GetAuthPasswordHash(ctx); err != nil {
		logger.Error("load persisted admin password", "error", err)
		os.Exit(1)
	} else if exists {
		cfg.Auth.PasswordHash = persistedHash
		cfg.Auth.Password = ""
	}

	authManager, err := auth.NewManager(cfg.Auth, logger)
	if err != nil {
		logger.Error("configure auth", "error", err)
		os.Exit(1)
	}
	go watchPersistedPassword(ctx, db, authManager, logger)

	checkers := checker.NewRegistry(
		checker.NewRSSChecker(),
		checker.NewTestFlightChecker(),
		checker.NewWebpageChecker(),
		checker.NewGitHubReleaseChecker(),
	)
	notifiers := notifier.NewRegistry(
		notifier.NewBarkNotifier(),
		notifier.NewEmailNotifier(),
		notifier.NewWebhookNotifier(),
	)

	sched := scheduler.New(db, checkers, notifiers, scheduler.Options{
		Tick:        cfg.SchedulerTick,
		WorkerCount: cfg.WorkerCount,
		Logger:      logger,
	})
	go sched.Start(ctx)

	historyWorker := maintenance.NewHistoryWorker(db, maintenance.HistoryOptions{
		Policy: runtimeSettings.HistoryRetention,
		PolicyProvider: func(ctx context.Context) (store.HistoryRetentionPolicy, error) {
			current, err := db.GetRuntimeSettings(ctx, runtimeDefaults)
			return current.HistoryRetention, err
		},
		Interval: retention.CleanupInterval,
		Logger:   logger,
	})
	go historyWorker.Run(ctx)

	server := &http.Server{
		Addr:              cfg.Addr,
		Handler:           api.NewServer(db, sched, cfg.WebDir, logger, authManager, api.WithRuntimeDefaults(runtimeDefaults)).Routes(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	go func() {
		logger.Info("watchbell listening", "addr", cfg.Addr, "db", cfg.DBPath, "web_dir", cfg.WebDir)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("http server", "error", err)
			stop()
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("shutdown http server", "error", err)
	}
}

func setPersistedPassword(password string) error {
	if err := auth.ValidatePassword(password); err != nil {
		return err
	}
	passwordHash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	cfg := config.FromEnv()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db, err := store.Open(ctx, cfg.DBPath)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer db.Close()
	if err := db.SetAuthPasswordHashAudited(ctx, passwordHash, "cli"); err != nil {
		return fmt.Errorf("set administrator password: %w", err)
	}
	return nil
}

func readConfirmedPassword() (string, error) {
	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		password, err := readHiddenPassword(fd, "New administrator password: ")
		if err != nil {
			return "", err
		}
		confirmation, err := readHiddenPassword(fd, "Repeat new administrator password: ")
		if err != nil {
			return "", err
		}
		return validateConfirmedPassword(password, confirmation)
	}

	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 1024), 2048)
	fmt.Fprint(os.Stderr, "New administrator password: ")
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return "", fmt.Errorf("read password: %w", err)
		}
		return "", errors.New("password input ended unexpectedly")
	}
	password := strings.TrimSuffix(scanner.Text(), "\r")
	fmt.Fprint(os.Stderr, "Repeat new administrator password: ")
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return "", fmt.Errorf("read password confirmation: %w", err)
		}
		return "", errors.New("password confirmation ended unexpectedly")
	}
	confirmation := strings.TrimSuffix(scanner.Text(), "\r")
	return validateConfirmedPassword(password, confirmation)
}

func readHiddenPassword(fd int, prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	value, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", fmt.Errorf("read password: %w", err)
	}
	return strings.TrimSuffix(string(value), "\r"), nil
}

func validateConfirmedPassword(password, confirmation string) (string, error) {
	if password != confirmation {
		return "", errors.New("passwords do not match")
	}
	if err := auth.ValidatePassword(password); err != nil {
		return "", err
	}
	return password, nil
}

func watchPersistedPassword(ctx context.Context, db *store.Store, manager *auth.Manager, logger *slog.Logger) {
	if manager == nil || !manager.Enabled() {
		return
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	lastError := ""
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_, err := manager.SyncPasswordHash(func() (string, bool, error) {
				return db.GetAuthPasswordHash(ctx)
			})
			if err != nil {
				if message := err.Error(); message != lastError {
					logger.Error("reload persisted administrator password", "error", err)
					lastError = message
				}
				continue
			}
			lastError = ""
		}
	}
}

func runHealthcheck() error {
	endpoint := strings.TrimSpace(os.Getenv("WATCHBELL_HEALTHCHECK_URL"))
	if endpoint == "" {
		endpoint = "http://127.0.0.1:8080/api/health"
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(endpoint)
	if err != nil {
		return fmt.Errorf("healthcheck request: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthcheck failed: http %d", resp.StatusCode)
	}
	return nil
}
