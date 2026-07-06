package config

import (
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/watchbell/watchbell/internal/auth"
)

type Config struct {
	Addr          string
	DBPath        string
	WebDir        string
	SchedulerTick time.Duration
	WorkerCount   int
	LogLevel      slog.Level
	Auth          auth.Config
}

func FromEnv() Config {
	return Config{
		Addr:          getenv("WATCHBELL_ADDR", ":8080"),
		DBPath:        getenv("WATCHBELL_DB", "data/watchbell.db"),
		WebDir:        getenv("WATCHBELL_WEB_DIR", "web/dist"),
		SchedulerTick: getDuration("WATCHBELL_SCHEDULER_TICK", 10*time.Second),
		WorkerCount:   getInt("WATCHBELL_WORKERS", 4),
		LogLevel:      getLogLevel("WATCHBELL_LOG_LEVEL", slog.LevelInfo),
		Auth: auth.Config{
			Enabled:       !getBool("WATCHBELL_AUTH_DISABLED", false),
			Username:      getenv("WATCHBELL_ADMIN_USERNAME", "admin"),
			Password:      os.Getenv("WATCHBELL_ADMIN_PASSWORD"),
			PasswordHash:  os.Getenv("WATCHBELL_ADMIN_PASSWORD_HASH"),
			SessionSecret: os.Getenv("WATCHBELL_SESSION_SECRET"),
			SessionTTL:    getDuration("WATCHBELL_SESSION_TTL", 7*24*time.Hour),
			CookieName:    getenv("WATCHBELL_SESSION_COOKIE", "watchbell_session"),
		},
	}
}

func getenv(key, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}

func getInt(key string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

func getDuration(key string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

func getBool(key string, fallback bool) bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	if value == "" {
		return fallback
	}
	switch value {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}

func getLogLevel(key string, fallback slog.Level) slog.Level {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	case "info":
		return slog.LevelInfo
	default:
		return fallback
	}
}
