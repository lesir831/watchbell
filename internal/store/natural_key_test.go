package store

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/watchbell/watchbell/internal/model"
)

func TestNaturalKeyCreatesAreAtomic(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, t.TempDir()+"/watchbell.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	monitor, err := db.CreateMonitor(ctx, model.MonitorInput{Name: "Rule owner", Type: "rss", Enabled: true, IntervalSeconds: 60, Config: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		create func() error
	}{
		{
			name: "monitor",
			create: func() error {
				_, err := db.CreateMonitor(ctx, model.MonitorInput{Name: "Same monitor", Type: "rss", Enabled: true, IntervalSeconds: 60, Config: json.RawMessage(`{}`)})
				return err
			},
		},
		{
			name: "rule",
			create: func() error {
				_, err := db.CreateRule(ctx, model.RuleInput{MonitorID: monitor.ID, Name: "Same rule", Enabled: true, Condition: json.RawMessage(`{}`)})
				return err
			},
		},
		{
			name: "channel",
			create: func() error {
				_, err := db.CreateNotifyChannel(ctx, model.NotifyChannelInput{Name: "Same channel", Type: "webhook", Enabled: true, Config: json.RawMessage(`{}`)})
				return err
			},
		},
		{
			name: "template",
			create: func() error {
				_, err := db.CreateNotificationTemplate(ctx, model.NotificationTemplateInput{Name: "Same template", SubjectTemplate: "subject", BodyTemplate: "body"})
				return err
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			const workers = 12
			start := make(chan struct{})
			var successes atomic.Int32
			var group sync.WaitGroup
			for range workers {
				group.Add(1)
				go func() {
					defer group.Done()
					<-start
					err := test.create()
					switch {
					case err == nil:
						successes.Add(1)
					case errors.Is(err, ErrDuplicateNaturalKey):
					default:
						t.Errorf("create returned unexpected error: %v", err)
					}
				}()
			}
			close(start)
			group.Wait()
			if successes.Load() != 1 {
				t.Fatalf("successful creates = %d, want 1", successes.Load())
			}
		})
	}
}

func TestNaturalKeyUpdateRejectsExistingActiveKey(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, t.TempDir()+"/watchbell.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	first, err := db.CreateMonitor(ctx, model.MonitorInput{Name: "First", Type: "rss", Enabled: true, IntervalSeconds: 60, Config: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	second, err := db.CreateMonitor(ctx, model.MonitorInput{Name: "Second", Type: "rss", Enabled: true, IntervalSeconds: 60, Config: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.UpdateMonitor(ctx, second.ID, model.MonitorInput{Name: first.Name, Type: first.Type, Enabled: true, IntervalSeconds: 60, Config: json.RawMessage(`{}`)})
	if !errors.Is(err, ErrDuplicateNaturalKey) {
		t.Fatalf("UpdateMonitor() error = %v, want ErrDuplicateNaturalKey", err)
	}
	unchanged, err := db.GetMonitor(ctx, second.ID)
	if err != nil || unchanged.Name != "Second" {
		t.Fatalf("conflicting update changed row: %#v err=%v", unchanged, err)
	}
}

func TestLegacyNaturalKeyDuplicatesDoNotBlockStartup(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir() + "/watchbell.db"
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	now := nowString()
	for range 2 {
		if _, err := db.db.ExecContext(ctx, `INSERT INTO monitors (name, type, enabled, interval_seconds, config_json, state_json, created_at, updated_at) VALUES ('Legacy duplicate', 'rss', 1, 60, '{}', '{}', ?, ?)`, now, now); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	db, err = Open(ctx, path)
	if err != nil {
		t.Fatalf("Open() rejected legacy duplicates: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.CreateMonitor(ctx, model.MonitorInput{Name: "Legacy duplicate", Type: "rss", Enabled: true, IntervalSeconds: 60, Config: json.RawMessage(`{}`)})
	if !errors.Is(err, ErrDuplicateNaturalKey) {
		t.Fatalf("CreateMonitor() error = %v, want ErrDuplicateNaturalKey", err)
	}
	if _, err := db.CreateMonitor(ctx, model.MonitorInput{Name: "Unrelated", Type: "rss", Enabled: true, IntervalSeconds: 60, Config: json.RawMessage(`{}`)}); err != nil {
		t.Fatalf("unrelated create failed: %v", err)
	}
}
