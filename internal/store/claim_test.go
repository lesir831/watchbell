package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/watchbell/watchbell/internal/model"
)

func TestOutboxClaimIsCompareAndSwap(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, t.TempDir()+"/watchbell.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	monitor, err := db.CreateMonitor(ctx, model.MonitorInput{Name: "Claim", Type: model.MonitorTypeRSS, Enabled: true, IntervalSeconds: 300, Config: json.RawMessage(`{"url":"https://example.com/feed.xml"}`)})
	if err != nil {
		t.Fatal(err)
	}
	event, created, err := db.CreateEvent(ctx, monitor.ID, model.EventData{Type: "rss.item", Fingerprint: "claim", Payload: map[string]any{}})
	if err != nil || !created {
		t.Fatalf("create event: created=%v err=%v", created, err)
	}

	var successes atomic.Int32
	var group sync.WaitGroup
	for range 8 {
		group.Add(1)
		go func() {
			defer group.Done()
			claimed, err := db.ClaimOutbox(ctx, event.ID, time.Now().UTC(), false)
			if err != nil {
				t.Errorf("claim outbox: %v", err)
				return
			}
			if claimed {
				successes.Add(1)
			}
		}()
	}
	group.Wait()
	if got := successes.Load(); got != 1 {
		t.Fatalf("successful claims = %d, want 1", got)
	}
}

func TestNotificationRetryClaimExpiresAndCanBeReleased(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, t.TempDir()+"/watchbell.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	now := time.Date(2026, time.July, 15, 12, 0, 0, 0, time.UTC)
	dueAt := now.Add(-time.Minute)
	attempt, err := db.CreateNotificationAttempt(ctx, model.NotificationAttemptInput{
		ChannelName: "Provider", ChannelType: model.ChannelTypeWebhook, Kind: "delivery", Status: "failed",
		Subject: "subject", Body: "body", Error: "offline", AttemptNo: 1, NextRetryAt: &dueAt,
	})
	if err != nil {
		t.Fatal(err)
	}

	due, err := db.ListDueNotificationAttempts(ctx, 10, now)
	if err != nil || len(due) != 1 {
		t.Fatalf("initial due attempts = %#v err=%v", due, err)
	}
	claimed, err := db.ClaimNotificationAttempt(ctx, attempt.ID, now)
	if err != nil || !claimed {
		t.Fatalf("claim = %v err=%v", claimed, err)
	}
	if claimedAgain, err := db.ClaimNotificationAttempt(ctx, attempt.ID, now); err != nil || claimedAgain {
		t.Fatalf("duplicate claim = %v err=%v", claimedAgain, err)
	}
	due, err = db.ListDueNotificationAttempts(ctx, 10, now.Add(4*time.Minute))
	if err != nil || len(due) != 0 {
		t.Fatalf("leased attempt became due too early: %#v err=%v", due, err)
	}
	due, err = db.ListDueNotificationAttempts(ctx, 10, now.Add(6*time.Minute))
	if err != nil || len(due) != 1 {
		t.Fatalf("stale claim was not recovered: %#v err=%v", due, err)
	}
	claimed, err = db.ClaimNotificationAttempt(ctx, attempt.ID, now.Add(6*time.Minute))
	if err != nil || !claimed {
		t.Fatalf("reclaim = %v err=%v", claimed, err)
	}
	next := now.Add(8 * time.Minute)
	if err := db.ReleaseNotificationRetry(ctx, attempt.ID, next); err != nil {
		t.Fatal(err)
	}
	due, _ = db.ListDueNotificationAttempts(ctx, 10, next.Add(-time.Second))
	if len(due) != 0 {
		t.Fatalf("released retry ran early: %#v", due)
	}
	due, _ = db.ListDueNotificationAttempts(ctx, 10, next)
	if len(due) != 1 {
		t.Fatalf("released retry not due: %#v", due)
	}
}

func TestNotificationRetryChainExposesLeafContractAndRejectsSecondSuccessor(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, t.TempDir()+"/watchbell.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	now := time.Now().UTC()
	next := now.Add(-time.Minute)
	source, err := db.CreateNotificationAttempt(ctx, model.NotificationAttemptInput{
		ChannelName: "Provider", ChannelType: model.ChannelTypeWebhook, Kind: "delivery", Status: "failed", NextRetryAt: &next,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !source.Retriable || source.Resolved {
		t.Fatalf("failed leaf contract = retriable:%v resolved:%v", source.Retriable, source.Resolved)
	}
	if due, err := db.ListDueNotificationAttempts(ctx, 10, now); err != nil || len(due) != 1 || due[0].ID != source.ID {
		t.Fatalf("failed leaf was not due for automatic retry: %#v err=%v", due, err)
	}
	child, err := db.CreateNotificationAttempt(ctx, model.NotificationAttemptInput{
		RetryOfID: &source.ID, ChannelName: "Provider", ChannelType: model.ChannelTypeWebhook, Kind: "delivery", Status: "sent", AttemptNo: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if child.Retriable || child.Resolved {
		t.Fatalf("sent leaf contract = retriable:%v resolved:%v", child.Retriable, child.Resolved)
	}
	source, err = db.GetNotificationAttempt(ctx, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if source.Retriable || !source.Resolved || source.NextRetryAt != nil {
		t.Fatalf("superseded failure contract = retriable:%v resolved:%v", source.Retriable, source.Resolved)
	}
	if claimed, err := db.ClaimNotificationAttemptNow(ctx, source.ID, time.Now().UTC()); err != nil || claimed {
		t.Fatalf("superseded source claim = %v err=%v", claimed, err)
	}
	if due, err := db.ListDueNotificationAttempts(ctx, 10, now); err != nil || len(due) != 0 {
		t.Fatalf("superseded source remained due: %#v err=%v", due, err)
	}
	_, err = db.CreateNotificationAttempt(ctx, model.NotificationAttemptInput{
		RetryOfID: &source.ID, ChannelName: "Provider", ChannelType: model.ChannelTypeWebhook, Kind: "delivery", Status: "sent", AttemptNo: 2,
	})
	if !errors.Is(err, ErrNotificationRetryConflict) {
		t.Fatalf("second successor error = %v, want ErrNotificationRetryConflict", err)
	}
}

func TestNotificationRetrySuccessorAndSourceFinalizationAreAtomic(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, t.TempDir()+"/watchbell.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	now := time.Now().UTC()
	next := now.Add(time.Minute)
	source, err := db.CreateNotificationAttempt(ctx, model.NotificationAttemptInput{
		ChannelName: "Provider", ChannelType: model.ChannelTypeWebhook, Kind: "delivery", Status: "failed", NextRetryAt: &next,
	})
	if err != nil {
		t.Fatal(err)
	}
	if claimed, err := db.ClaimNotificationAttemptNow(ctx, source.ID, now); err != nil || !claimed {
		t.Fatalf("claim source = %v err=%v", claimed, err)
	}
	trigger := fmt.Sprintf(`CREATE TRIGGER fail_retry_finalize
		BEFORE UPDATE OF next_retry_at, retry_claimed_at ON notification_attempts
		WHEN OLD.id = %d
		BEGIN SELECT RAISE(ABORT, 'forced retry finalize failure'); END`, source.ID)
	if _, err := db.db.ExecContext(ctx, trigger); err != nil {
		t.Fatal(err)
	}
	_, err = db.CreateNotificationAttempt(ctx, model.NotificationAttemptInput{
		RetryOfID: &source.ID, ChannelName: "Provider", ChannelType: model.ChannelTypeWebhook, Kind: "delivery", Status: "sent", AttemptNo: 2,
	})
	if err == nil {
		t.Fatal("retry successor unexpectedly committed when source finalization failed")
	}
	attempts, err := db.ListNotificationAttempts(ctx, 10)
	if err != nil || len(attempts) != 1 || attempts[0].ID != source.ID {
		t.Fatalf("failed transaction left a successor: %#v err=%v", attempts, err)
	}
	if _, err := db.db.ExecContext(ctx, `DROP TRIGGER fail_retry_finalize`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateNotificationAttempt(ctx, model.NotificationAttemptInput{
		RetryOfID: &source.ID, ChannelName: "Provider", ChannelType: model.ChannelTypeWebhook, Kind: "delivery", Status: "sent", AttemptNo: 2,
	}); err != nil {
		t.Fatal(err)
	}
	source, err = db.GetNotificationAttempt(ctx, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	var stillClaimed bool
	if err := db.db.QueryRowContext(ctx, `SELECT retry_claimed_at IS NOT NULL FROM notification_attempts WHERE id = ?`, source.ID).Scan(&stillClaimed); err != nil {
		t.Fatal(err)
	}
	if source.NextRetryAt != nil || stillClaimed || source.Retriable || !source.Resolved {
		t.Fatalf("committed retry did not atomically finalize source: %#v claimed=%v", source, stillClaimed)
	}
}
