package main

import (
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
	"github.com/watchbell/watchbell/internal/notifier"
	"github.com/watchbell/watchbell/internal/scheduler"
	"github.com/watchbell/watchbell/internal/store"
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

	authManager, err := auth.NewManager(cfg.Auth, logger)
	if err != nil {
		logger.Error("configure auth", "error", err)
		os.Exit(1)
	}

	checkers := checker.NewRegistry(
		checker.NewRSSChecker(),
		checker.NewTestFlightChecker(),
		checker.NewWebpageChecker(),
		checker.NewGitHubReleaseChecker(),
	)
	notifiers := notifier.NewRegistry(
		notifier.NewBarkNotifier(),
		notifier.NewEmailNotifier(),
	)

	sched := scheduler.New(db, checkers, notifiers, scheduler.Options{
		Tick:        cfg.SchedulerTick,
		WorkerCount: cfg.WorkerCount,
		Logger:      logger,
	})
	go sched.Start(ctx)

	server := &http.Server{
		Addr:              cfg.Addr,
		Handler:           api.NewServer(db, sched, cfg.WebDir, logger, authManager).Routes(),
		ReadHeaderTimeout: 10 * time.Second,
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
