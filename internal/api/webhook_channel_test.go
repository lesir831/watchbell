package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/watchbell/watchbell/internal/model"
)

func TestEmptyWebhookHeadersAreNotReportedAsConfigured(t *testing.T) {
	redacted, configured := redactConfig(json.RawMessage(`{"url":"https://example.com","headers":{}}`), []string{"headers"})
	if len(configured) != 0 {
		t.Fatalf("configured secrets = %#v", configured)
	}
	if bytes.Contains(redacted, []byte("headers")) {
		t.Fatalf("redacted config still contains headers: %s", redacted)
	}
}

func TestWebhookChannelHeadersAreValidatedAndRedacted(t *testing.T) {
	server, db := newTestServer(t)
	createBody := []byte(`{
		"name":"Deploy hook",
		"type":"webhook",
		"enabled":true,
		"config":{
			"url":"https://hooks.example.com/${event.id}",
			"method":"POST",
			"headers":{"Authorization":"Bearer sensitive-token"},
			"bodyTemplate":"{\"subject\":\"${message.subject}\"}"
		}
	}`)
	response, err := http.Post(server.URL+"/api/channels", "application/json", bytes.NewReader(createBody))
	if err != nil {
		t.Fatal(err)
	}
	createdBody, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d body = %s", response.StatusCode, createdBody)
	}
	if bytes.Contains(createdBody, []byte("sensitive-token")) || bytes.Contains(createdBody, []byte("hooks.example.com")) || !bytes.Contains(createdBody, []byte(`"configuredSecrets":["url","headers"]`)) {
		t.Fatalf("webhook headers were not redacted: %s", createdBody)
	}
	exportResponse, err := http.Get(server.URL + "/api/config/export")
	if err != nil {
		t.Fatal(err)
	}
	exportedBody, _ := io.ReadAll(exportResponse.Body)
	exportResponse.Body.Close()
	if bytes.Contains(exportedBody, []byte("sensitive-token")) || bytes.Contains(exportedBody, []byte("hooks.example.com")) || !bytes.Contains(exportedBody, []byte(`"redactedSecrets":["url","headers"]`)) {
		t.Fatalf("redacted backup leaked webhook secrets: %s", exportedBody)
	}
	var backup model.ConfigBackup
	if err := json.Unmarshal(exportedBody, &backup); err != nil {
		t.Fatal(err)
	}
	importBody, err := json.Marshal(model.ConfigImportRequest{Mode: "merge", Backup: backup})
	if err != nil {
		t.Fatal(err)
	}
	importResponse, err := http.Post(server.URL+"/api/config/import", "application/json", bytes.NewReader(importBody))
	if err != nil {
		t.Fatal(err)
	}
	importResponseBody, _ := io.ReadAll(importResponse.Body)
	importResponse.Body.Close()
	if importResponse.StatusCode != http.StatusOK {
		t.Fatalf("redacted webhook backup import status = %d body = %s", importResponse.StatusCode, importResponseBody)
	}

	stored, err := db.GetNotifyChannel(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(stored.Config, []byte("sensitive-token")) {
		t.Fatalf("stored webhook headers are missing: %s", stored.Config)
	}

	// Omitting the redacted header map while editing preserves the configured
	// credentials, matching the behavior of Bark and SMTP secrets.
	updateBody := []byte(`{
		"name":"Deploy hook updated",
		"type":"webhook",
		"enabled":true,
		"config":{
			"method":"PATCH",
			"bodyTemplate":"${message.body}"
		}
	}`)
	request, _ := http.NewRequest(http.MethodPut, server.URL+"/api/channels/1", bytes.NewReader(updateBody))
	request.Header.Set("Content-Type", "application/json")
	updateResponse, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	updatedBody, _ := io.ReadAll(updateResponse.Body)
	updateResponse.Body.Close()
	if updateResponse.StatusCode != http.StatusOK {
		t.Fatalf("update status = %d body = %s", updateResponse.StatusCode, updatedBody)
	}
	stored, err = db.GetNotifyChannel(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(stored.Config, []byte("sensitive-token")) || !bytes.Contains(stored.Config, []byte("hooks.example.com")) {
		t.Fatalf("omitted webhook URL/headers were not preserved: %s", stored.Config)
	}
}

func TestWebhookChannelRejectsUnsafeHeadersAtSaveTime(t *testing.T) {
	server, _ := newTestServer(t)
	body := []byte(`{
		"name":"Unsafe hook",
		"type":"webhook",
		"enabled":true,
		"config":{
			"url":"https://hooks.example.com/notify",
			"method":"POST",
			"headers":{"Host":"evil.example"}
		}
	}`)
	response, err := http.Post(server.URL+"/api/channels", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	responseBody, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d body = %s", response.StatusCode, responseBody)
	}
	if !bytes.Contains(responseBody, []byte("not allowed")) {
		t.Fatalf("unexpected validation response: %s", responseBody)
	}
}
