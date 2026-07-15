package maintenance

import (
	"context"
	"log/slog"
	"time"

	"github.com/watchbell/watchbell/internal/store"
)

type HistoryCleaner interface {
	CleanupHistory(context.Context, store.HistoryRetentionPolicy, time.Time) (store.HistoryCleanupResult, error)
}

type HistoryOptions struct {
	Policy     store.HistoryRetentionPolicy
	Interval   time.Duration
	MaxBatches int
	Logger     *slog.Logger
}

type HistoryWorker struct {
	cleaner    HistoryCleaner
	policy     store.HistoryRetentionPolicy
	interval   time.Duration
	maxBatches int
	logger     *slog.Logger
}

func NewHistoryWorker(cleaner HistoryCleaner, options HistoryOptions) *HistoryWorker {
	if options.Interval <= 0 {
		options.Interval = 6 * time.Hour
	}
	if options.MaxBatches <= 0 {
		options.MaxBatches = 10
	}
	if options.Logger == nil {
		options.Logger = slog.Default()
	}
	return &HistoryWorker{
		cleaner: cleaner, policy: options.Policy, interval: options.Interval,
		maxBatches: options.MaxBatches, logger: options.Logger,
	}
}

// Run performs cleanup immediately and then on every interval until the
// context is cancelled. Callers normally start it in its own goroutine.
func (worker *HistoryWorker) Run(ctx context.Context) {
	if worker.cleaner == nil || !retentionEnabled(worker.policy) {
		return
	}
	worker.cleanup(ctx)
	ticker := time.NewTicker(worker.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			worker.cleanup(ctx)
		}
	}
}

func (worker *HistoryWorker) cleanup(ctx context.Context) {
	var total store.HistoryCleanupResult
	for batch := 0; batch < worker.maxBatches; batch++ {
		result, err := worker.cleaner.CleanupHistory(ctx, worker.policy, time.Now().UTC())
		if err != nil {
			if ctx.Err() == nil {
				worker.logger.Error("cleanup history", "error", err)
			}
			return
		}
		total.EventsDeleted += result.EventsDeleted
		total.CheckRunsDeleted += result.CheckRunsDeleted
		total.RuleEvaluationsDeleted += result.RuleEvaluationsDeleted
		total.NotificationAttemptsDeleted += result.NotificationAttemptsDeleted
		total.AuditLogsDeleted += result.AuditLogsDeleted
		if result.TotalDeleted() == 0 {
			break
		}
	}
	if total.TotalDeleted() > 0 {
		worker.logger.Info("history cleanup complete",
			"events", total.EventsDeleted,
			"check_runs", total.CheckRunsDeleted,
			"rule_evaluations", total.RuleEvaluationsDeleted,
			"notification_attempts", total.NotificationAttemptsDeleted,
			"audit_logs", total.AuditLogsDeleted,
		)
	}
}

func retentionEnabled(policy store.HistoryRetentionPolicy) bool {
	return policy.EventAge > 0 || policy.CheckRunAge > 0 || policy.NotificationAttemptAge > 0 || policy.AuditLogAge > 0
}
