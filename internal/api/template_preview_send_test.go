package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/watchbell/watchbell/internal/checker"
	"github.com/watchbell/watchbell/internal/model"
	"github.com/watchbell/watchbell/internal/notifier"
	"github.com/watchbell/watchbell/internal/scheduler"
	"github.com/watchbell/watchbell/internal/store"
)

type previewRecordingNotifier struct {
	messages []notifier.Message
}

func (n *previewRecordingNotifier) Type() string { return model.ChannelTypeWebhook }
func (n *previewRecordingNotifier) Send(_ context.Context, _ model.NotifyChannel, message notifier.Message) error {
	n.messages = append(n.messages, message)
	return nil
}

func TestSendTemplatePreviewUsesChannelAndRecordsAttempt(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, t.TempDir()+"/watchbell.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	channel, err := db.CreateNotifyChannel(ctx, model.NotifyChannelInput{
		Name: "Preview webhook", Type: model.ChannelTypeWebhook, Enabled: true, Config: json.RawMessage(`{"url":"https://example.com/hook"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	template, err := db.CreateNotificationTemplate(ctx, model.NotificationTemplateInput{
		Name: "Preview", SubjectTemplate: "${monitor.name}", BodyTemplate: "${event.type}",
	})
	if err != nil {
		t.Fatal(err)
	}
	recorder := &previewRecordingNotifier{}
	sched := scheduler.New(db, checker.NewRegistry(), notifier.NewRegistry(recorder), scheduler.Options{})
	handler := NewServer(db, sched, "", slog.New(slog.NewTextHandler(io.Discard, nil)), nil).Routes()
	body, _ := json.Marshal(map[string]any{"templateId": template.ID, "channelId": channel.ID})
	request := httptest.NewRequest(http.MethodPost, "http://watchbell.test/api/templates/send-preview", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var attempt model.NotificationAttempt
	if err := json.Unmarshal(response.Body.Bytes(), &attempt); err != nil {
		t.Fatal(err)
	}
	if attempt.Kind != "preview" || attempt.ChannelID == nil || *attempt.ChannelID != channel.ID || attempt.Status != "sent" {
		t.Fatalf("unexpected attempt: %#v", attempt)
	}
	if len(recorder.messages) != 1 || recorder.messages[0].Subject != "Example Monitor" || recorder.messages[0].Body != "rss.item" {
		t.Fatalf("unexpected rendered messages: %#v", recorder.messages)
	}
	stored, err := db.GetNotificationAttempt(ctx, attempt.ID)
	if err != nil || stored.Kind != "preview" {
		t.Fatalf("stored attempt=%#v err=%v", stored, err)
	}
}
