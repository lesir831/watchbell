package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/watchbell/watchbell/internal/model"
)

func TestSoftArchiveRepairsActiveConfigurationReferences(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, t.TempDir()+"/watchbell.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	firstChannel, err := db.CreateNotifyChannel(ctx, model.NotifyChannelInput{Name: "First", Type: model.ChannelTypeBark, Enabled: true, Config: json.RawMessage(`{"deviceKey":"one"}`)})
	if err != nil {
		t.Fatal(err)
	}
	secondChannel, err := db.CreateNotifyChannel(ctx, model.NotifyChannelInput{Name: "Second", Type: model.ChannelTypeBark, Enabled: true, Config: json.RawMessage(`{"deviceKey":"two"}`)})
	if err != nil {
		t.Fatal(err)
	}
	monitor, err := db.CreateMonitor(ctx, model.MonitorInput{
		Name: "Feed", Type: model.MonitorTypeRSS, Enabled: true, IntervalSeconds: 300,
		Config: json.RawMessage(`{"url":"https://example.com/feed.xml"}`), FailureAlertAfter: 2,
		FailureNotifyChannelIDs: []int64{firstChannel.ID, secondChannel.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	template, err := db.CreateNotificationTemplate(ctx, model.NotificationTemplateInput{Name: "Custom", SubjectTemplate: "subject", BodyTemplate: "body"})
	if err != nil {
		t.Fatal(err)
	}
	ruleItem, err := db.CreateRule(ctx, model.RuleInput{
		MonitorID: monitor.ID, Name: "Rule", Enabled: true, Condition: json.RawMessage(`{}`),
		NotifyChannelIDs: []int64{firstChannel.ID, secondChannel.ID}, TemplateID: &template.ID,
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := db.DeleteNotificationTemplate(ctx, template.ID); err != nil {
		t.Fatal(err)
	}
	storedRule, err := db.GetRule(ctx, ruleItem.ID)
	if err != nil || storedRule.TemplateID != nil {
		t.Fatalf("archived template reference was not reset: rule=%#v err=%v", storedRule, err)
	}

	if err := db.DeleteNotifyChannel(ctx, firstChannel.ID); err != nil {
		t.Fatal(err)
	}
	storedRule, err = db.GetRule(ctx, ruleItem.ID)
	if err != nil || len(storedRule.NotifyChannelIDs) != 1 || storedRule.NotifyChannelIDs[0] != secondChannel.ID {
		t.Fatalf("remaining rule channels = %#v err=%v", storedRule.NotifyChannelIDs, err)
	}
	storedMonitor, err := db.GetMonitor(ctx, monitor.ID)
	if err != nil || storedMonitor.FailureAlertAfter != 2 || len(storedMonitor.FailureNotifyChannelIDs) != 1 || storedMonitor.FailureNotifyChannelIDs[0] != secondChannel.ID {
		t.Fatalf("remaining failure channels = %#v err=%v", storedMonitor, err)
	}

	if err := db.DeleteNotifyChannel(ctx, secondChannel.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetRule(ctx, ruleItem.ID); !IsNotFound(err) {
		t.Fatalf("rule without channels should be archived, got %v", err)
	}
	storedMonitor, err = db.GetMonitor(ctx, monitor.ID)
	if err != nil || storedMonitor.FailureAlertAfter != 0 || storedMonitor.FailureAlertActive || len(storedMonitor.FailureNotifyChannelIDs) != 0 {
		t.Fatalf("empty failure alert was not disabled: %#v err=%v", storedMonitor, err)
	}

	thirdChannel, err := db.CreateNotifyChannel(ctx, model.NotifyChannelInput{Name: "Third", Type: model.ChannelTypeBark, Enabled: true, Config: json.RawMessage(`{"deviceKey":"three"}`)})
	if err != nil {
		t.Fatal(err)
	}
	secondRule, err := db.CreateRule(ctx, model.RuleInput{MonitorID: monitor.ID, Name: "Second rule", Enabled: true, Condition: json.RawMessage(`{}`), NotifyChannelIDs: []int64{thirdChannel.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteMonitor(ctx, monitor.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetRule(ctx, secondRule.ID); !IsNotFound(err) {
		t.Fatalf("monitor rules should be archived, got %v", err)
	}
}

func TestArchivedRuleCannotBeMutated(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, t.TempDir()+"/watchbell.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	monitor, err := db.CreateMonitor(ctx, model.MonitorInput{Name: "Feed", Type: model.MonitorTypeRSS, Enabled: true, IntervalSeconds: 300, Config: json.RawMessage(`{"url":"https://example.com/feed.xml"}`)})
	if err != nil {
		t.Fatal(err)
	}
	channel, err := db.CreateNotifyChannel(ctx, model.NotifyChannelInput{Name: "Phone", Type: model.ChannelTypeBark, Enabled: true, Config: json.RawMessage(`{"deviceKey":"key"}`)})
	if err != nil {
		t.Fatal(err)
	}
	ruleItem, err := db.CreateRule(ctx, model.RuleInput{MonitorID: monitor.ID, Name: "Original", Enabled: true, Condition: json.RawMessage(`{}`), NotifyChannelIDs: []int64{channel.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteRule(ctx, ruleItem.ID); err != nil {
		t.Fatal(err)
	}
	_, err = db.UpdateRule(ctx, ruleItem.ID, model.RuleInput{MonitorID: monitor.ID, Name: "Mutated", Enabled: true, Condition: json.RawMessage(`{}`), NotifyChannelIDs: []int64{channel.ID}})
	if !IsNotFound(err) {
		t.Fatalf("UpdateRule() error = %v, want not found", err)
	}
	if err := db.UpdateRuleFiredAt(ctx, ruleItem.ID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	var name string
	var firedAt sql.NullString
	if err := db.db.QueryRowContext(ctx, `SELECT name, last_fired_at FROM rules WHERE id = ?`, ruleItem.ID).Scan(&name, &firedAt); err != nil {
		t.Fatal(err)
	}
	if name != "Original" || firedAt.Valid {
		t.Fatalf("archived rule was changed: name=%q firedAt=%#v", name, firedAt)
	}
}

func TestConfigSnapshotRepairsLegacyDanglingReferences(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir() + "/watchbell.db"
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	channel, err := db.CreateNotifyChannel(ctx, model.NotifyChannelInput{Name: "Legacy", Type: model.ChannelTypeBark, Enabled: true, Config: json.RawMessage(`{"deviceKey":"secret"}`)})
	if err != nil {
		t.Fatal(err)
	}
	monitor, err := db.CreateMonitor(ctx, model.MonitorInput{
		Name: "Legacy feed", Type: model.MonitorTypeRSS, Enabled: true, IntervalSeconds: 300,
		Config: json.RawMessage(`{"url":"https://example.com/feed.xml"}`), FailureAlertAfter: 2, FailureNotifyChannelIDs: []int64{channel.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.CreateRule(ctx, model.RuleInput{MonitorID: monitor.ID, Name: "Legacy rule", Enabled: true, Condition: json.RawMessage(`{}`), NotifyChannelIDs: []int64{channel.ID}})
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a database produced by the older soft-delete implementation,
	// which did not repair JSON references.
	if _, err := db.db.ExecContext(ctx, `UPDATE notify_channels SET deleted_at = '2026-01-01T00:00:00Z' WHERE id = ?`, channel.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	rules, err := db.ListRules(ctx)
	if err != nil || len(rules) != 0 {
		t.Fatalf("startup repair retained dangling rules: %#v err=%v", rules, err)
	}
	repairedMonitor, err := db.GetMonitor(ctx, monitor.ID)
	if err != nil || repairedMonitor.FailureAlertAfter != 0 || len(repairedMonitor.FailureNotifyChannelIDs) != 0 {
		t.Fatalf("startup repair retained dangling failure alert: %#v err=%v", repairedMonitor, err)
	}

	snapshot, err := db.ReadConfigSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Channels) != 0 || len(snapshot.Rules) != 0 || len(snapshot.Monitors) != 1 {
		t.Fatalf("snapshot did not exclude dangling configuration: %#v", snapshot)
	}
	if snapshot.Monitors[0].FailureAlertAfter != 0 || len(snapshot.Monitors[0].FailureNotifyChannelIDs) != 0 {
		t.Fatalf("snapshot retained dangling failure channels: %#v", snapshot.Monitors[0])
	}
	if _, err := db.db.ExecContext(ctx, `UPDATE monitors SET deleted_at = '2026-01-01T00:00:00Z' WHERE id = ?`, monitor.ID); err != nil {
		t.Fatal(err)
	}
	snapshot, err = db.ReadConfigSnapshot(ctx)
	if err != nil || len(snapshot.Monitors) != 0 || len(snapshot.Rules) != 0 {
		t.Fatalf("deleted monitor leaked into snapshot: %#v err=%v", snapshot, err)
	}
}
