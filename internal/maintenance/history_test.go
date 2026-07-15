package maintenance

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/watchbell/watchbell/internal/store"
)

type fakeHistoryCleaner struct {
	mu     sync.Mutex
	calls  int
	called chan struct{}
}

func (cleaner *fakeHistoryCleaner) CleanupHistory(context.Context, store.HistoryRetentionPolicy, time.Time) (store.HistoryCleanupResult, error) {
	cleaner.mu.Lock()
	cleaner.calls++
	cleaner.mu.Unlock()
	select {
	case cleaner.called <- struct{}{}:
	default:
	}
	return store.HistoryCleanupResult{}, nil
}

func TestHistoryWorkerRunsImmediatelyAndStopsWithContext(t *testing.T) {
	cleaner := &fakeHistoryCleaner{called: make(chan struct{}, 1)}
	worker := NewHistoryWorker(cleaner, HistoryOptions{
		Policy:   store.UniformHistoryRetention(24*time.Hour, 10),
		Interval: time.Hour,
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		worker.Run(ctx)
		close(done)
	}()
	select {
	case <-cleaner.called:
	case <-time.After(time.Second):
		t.Fatal("history worker did not run immediately")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("history worker did not stop after cancellation")
	}
}

func TestHistoryWorkerDoesNotRunWhenRetentionDisabled(t *testing.T) {
	cleaner := &fakeHistoryCleaner{called: make(chan struct{}, 1)}
	NewHistoryWorker(cleaner, HistoryOptions{}).Run(context.Background())
	cleaner.mu.Lock()
	defer cleaner.mu.Unlock()
	if cleaner.calls != 0 {
		t.Fatalf("disabled worker made %d cleanup calls", cleaner.calls)
	}
}
