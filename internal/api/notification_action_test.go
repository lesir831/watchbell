package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/watchbell/watchbell/internal/checker"
	"github.com/watchbell/watchbell/internal/model"
	"github.com/watchbell/watchbell/internal/notifier"
	"github.com/watchbell/watchbell/internal/scheduler"
	"github.com/watchbell/watchbell/internal/store"
)

type failingAPITestNotifier struct{}

func (failingAPITestNotifier) Type() string { return "api_test" }
func (failingAPITestNotifier) Send(context.Context, model.NotifyChannel, notifier.Message) error {
	return errors.New("provider unavailable")
}

func TestNotificationActionErrorStatusMapping(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, t.TempDir()+"/watchbell.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	channel, err := db.CreateNotifyChannel(ctx, model.NotifyChannelInput{
		Name: "Failing provider", Type: "api_test", Enabled: true, Config: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	sched := scheduler.New(db, checker.NewRegistry(), notifier.NewRegistry(failingAPITestNotifier{}), scheduler.Options{})
	server := httptest.NewServer(NewServer(db, sched, "", slog.New(slog.NewTextHandler(io.Discard, nil)), nil).Routes())
	t.Cleanup(server.Close)

	post := func(path string) (int, string, model.NotificationAttempt) {
		t.Helper()
		request, err := http.NewRequest(http.MethodPost, server.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Content-Type", "application/json")
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		var payload struct {
			Code    string                    `json:"code"`
			Attempt model.NotificationAttempt `json:"attempt"`
		}
		if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		return response.StatusCode, payload.Code, payload.Attempt
	}

	status, code, _ := post("/api/channels/999999/test")
	if status != http.StatusNotFound || code != "channel_not_found" {
		t.Fatalf("missing channel = %d/%q", status, code)
	}
	status, code, failedTest := post("/api/channels/" + strconv.FormatInt(channel.ID, 10) + "/test")
	if status != http.StatusBadGateway || code != "channel_test_failed" || failedTest.ID == 0 {
		t.Fatalf("provider test failure = %d/%q attempt=%#v", status, code, failedTest)
	}

	sent, err := db.CreateNotificationAttempt(ctx, model.NotificationAttemptInput{
		ChannelID: &channel.ID, ChannelName: channel.Name, ChannelType: channel.Type,
		Kind: "test", Status: "sent", Subject: "done", AttemptNo: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	status, code, _ = post("/api/notification-attempts/" + strconv.FormatInt(sent.ID, 10) + "/retry")
	if status != http.StatusUnprocessableEntity || code != "notification_retry_not_failed" {
		t.Fatalf("sent attempt retry = %d/%q", status, code)
	}

	status, code, retried := post("/api/notification-attempts/" + strconv.FormatInt(failedTest.ID, 10) + "/retry")
	if status != http.StatusBadGateway || code != "notification_retry_failed" || retried.ID == 0 {
		t.Fatalf("retry provider failure = %d/%q attempt=%#v", status, code, retried)
	}
	status, code, _ = post("/api/notification-attempts/" + strconv.FormatInt(failedTest.ID, 10) + "/retry")
	if status != http.StatusConflict || code != "notification_retry_conflict" {
		t.Fatalf("superseded retry = %d/%q", status, code)
	}

	status, code, _ = post("/api/notification-attempts/999999/retry")
	if status != http.StatusNotFound || code != "notification_attempt_not_found" {
		t.Fatalf("missing attempt = %d/%q", status, code)
	}
	missingTarget, err := db.CreateNotificationAttempt(ctx, model.NotificationAttemptInput{
		ChannelName: "Gone", ChannelType: "api_test", Kind: "test", Status: "failed", Subject: "retry", AttemptNo: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	status, code, _ = post("/api/notification-attempts/" + strconv.FormatInt(missingTarget.ID, 10) + "/retry")
	if status != http.StatusConflict || code != "notification_retry_target_unavailable" {
		t.Fatalf("missing retry target = %d/%q", status, code)
	}
}
