package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/watchbell/watchbell/internal/model"
)

func TestDingTalkChannelSecretsAreRedactedAndRetained(t *testing.T) {
	server, db := newTestServer(t)
	body := []byte(`{"name":"Ops DingTalk","type":"dingtalk","enabled":true,"config":{"webhookUrl":"https://oapi.dingtalk.com/robot/send?access_token=sensitive-token","secret":"SEC-sensitive-secret","messageType":"markdown","title":"${message.subject}","text":"${message.body}"}}`)
	response, err := http.Post(server.URL+"/api/channels", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	createdBody, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d body = %s", response.StatusCode, createdBody)
	}
	if bytes.Contains(createdBody, []byte("sensitive-token")) || bytes.Contains(createdBody, []byte("sensitive-secret")) {
		t.Fatalf("DingTalk response leaked secrets: %s", createdBody)
	}
	if !bytes.Contains(createdBody, []byte(`"configuredSecrets":["webhookUrl","secret"]`)) {
		t.Fatalf("configured secrets missing: %s", createdBody)
	}

	redactedResponse, err := http.Get(server.URL + "/api/config/export")
	if err != nil {
		t.Fatal(err)
	}
	redactedBody, _ := io.ReadAll(redactedResponse.Body)
	redactedResponse.Body.Close()
	if redactedResponse.StatusCode != http.StatusOK {
		t.Fatalf("redacted export status = %d body = %s", redactedResponse.StatusCode, redactedBody)
	}
	if bytes.Contains(redactedBody, []byte("sensitive-token")) || bytes.Contains(redactedBody, []byte("sensitive-secret")) || !bytes.Contains(redactedBody, []byte(`"redactedSecrets":["webhookUrl","secret"]`)) {
		t.Fatalf("redacted DingTalk backup is unsafe: %s", redactedBody)
	}
	var redactedBackup model.ConfigBackup
	if err := json.Unmarshal(redactedBody, &redactedBackup); err != nil {
		t.Fatal(err)
	}
	// A redacted backup can safely update the same target and must retain that
	// target's existing webhook token and signing key.
	importBackup(t, server.URL, redactedBackup, http.StatusOK)
	preserved, err := db.GetNotifyChannel(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(preserved.Config, []byte("sensitive-token")) || !bytes.Contains(preserved.Config, []byte("sensitive-secret")) {
		t.Fatalf("redacted DingTalk import lost target secrets: %s", preserved.Config)
	}

	fullResponse, err := http.Get(server.URL + "/api/config/export?includeSecrets=true")
	if err != nil {
		t.Fatal(err)
	}
	fullBody, _ := io.ReadAll(fullResponse.Body)
	fullResponse.Body.Close()
	if fullResponse.StatusCode != http.StatusOK || !bytes.Contains(fullBody, []byte("sensitive-token")) || !bytes.Contains(fullBody, []byte("sensitive-secret")) {
		t.Fatalf("full DingTalk backup omitted secrets: status=%d body=%s", fullResponse.StatusCode, fullBody)
	}
	var fullBackup model.ConfigBackup
	if err := json.Unmarshal(fullBody, &fullBackup); err != nil {
		t.Fatal(err)
	}
	targetServer, targetStore := newTestServer(t)
	report := importBackup(t, targetServer.URL, fullBackup, http.StatusOK)
	targetChannelID := report.IDMap.Channels["1"]
	targetChannel, err := targetStore.GetNotifyChannel(context.Background(), targetChannelID)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(targetChannel.Config, []byte("sensitive-token")) || !bytes.Contains(targetChannel.Config, []byte("sensitive-secret")) {
		t.Fatalf("full DingTalk import did not restore secrets: %s", targetChannel.Config)
	}

	updateBody := []byte(`{"name":"Ops DingTalk","type":"dingtalk","enabled":true,"config":{"webhookUrl":"","secret":"","messageType":"text"}}`)
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
	stored, err := db.GetNotifyChannel(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(stored.Config, []byte("sensitive-token")) || !bytes.Contains(stored.Config, []byte("sensitive-secret")) {
		t.Fatalf("empty secret update did not retain DingTalk secrets: %s", stored.Config)
	}

	clearBody := []byte(`{"name":"Ops DingTalk","type":"dingtalk","enabled":true,"config":{"webhookUrl":"","secret":"","clearSecret":true,"messageType":"text"}}`)
	clearRequest, _ := http.NewRequest(http.MethodPut, server.URL+"/api/channels/1", bytes.NewReader(clearBody))
	clearRequest.Header.Set("Content-Type", "application/json")
	clearResponse, err := http.DefaultClient.Do(clearRequest)
	if err != nil {
		t.Fatal(err)
	}
	clearedBody, _ := io.ReadAll(clearResponse.Body)
	clearResponse.Body.Close()
	if clearResponse.StatusCode != http.StatusOK {
		t.Fatalf("clear secret status = %d body = %s", clearResponse.StatusCode, clearedBody)
	}
	if bytes.Contains(clearedBody, []byte(`"secret"`)) || !bytes.Contains(clearedBody, []byte(`"configuredSecrets":["webhookUrl"]`)) {
		t.Fatalf("clear secret response is incorrect: %s", clearedBody)
	}
	stored, err = db.GetNotifyChannel(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(stored.Config, []byte("sensitive-token")) || bytes.Contains(stored.Config, []byte("sensitive-secret")) || bytes.Contains(stored.Config, []byte("clearSecret")) {
		t.Fatalf("clearSecret did not remove only the signing key: %s", stored.Config)
	}
}

func TestDingTalkClearSecretControlIsNotPersistedOnCreate(t *testing.T) {
	server, db := newTestServer(t)
	body := []byte(`{"name":"Unsigned DingTalk","type":"dingtalk","enabled":true,"config":{"webhookUrl":"https://oapi.dingtalk.com/robot/send?access_token=sensitive-token","secret":"should-not-be-stored","clearSecret":true,"messageType":"text"}}`)
	response, err := http.Post(server.URL+"/api/channels", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	responseBody, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d body = %s", response.StatusCode, responseBody)
	}
	stored, err := db.GetNotifyChannel(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(stored.Config, []byte("should-not-be-stored")) || bytes.Contains(stored.Config, []byte("clearSecret")) {
		t.Fatalf("update-only secret control was persisted on create: %s", stored.Config)
	}
}

func TestDingTalkClearSecretControlIsNotPersistedOnChannelTypeChange(t *testing.T) {
	server, db := newTestServer(t)
	barkBody := []byte(`{"name":"Switchable","type":"bark","enabled":true,"config":{"serverUrl":"https://api.day.app","deviceKey":"device-key"}}`)
	response, err := http.Post(server.URL+"/api/channels", "application/json", bytes.NewReader(barkBody))
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d", response.StatusCode)
	}

	changeBody := []byte(`{"name":"Switchable","type":"dingtalk","enabled":true,"config":{"webhookUrl":"https://oapi.dingtalk.com/robot/send?access_token=sensitive-token","secret":"should-be-cleared","clearSecret":true,"messageType":"text"}}`)
	request, _ := http.NewRequest(http.MethodPut, server.URL+"/api/channels/1", bytes.NewReader(changeBody))
	request.Header.Set("Content-Type", "application/json")
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	responseBody, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("change status = %d body = %s", response.StatusCode, responseBody)
	}
	stored, err := db.GetNotifyChannel(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Type != model.ChannelTypeDingTalk || bytes.Contains(stored.Config, []byte("should-be-cleared")) || bytes.Contains(stored.Config, []byte("clearSecret")) {
		t.Fatalf("type-change persisted secret control: %#v config=%s", stored, stored.Config)
	}
}

func TestDingTalkClearSecretControlIsRejectedFromConfigImport(t *testing.T) {
	server, db := newTestServer(t)
	backup := model.ConfigBackup{
		Version: model.ConfigBackupVersion, ExportedAt: time.Now().UTC(), IncludesSecrets: true,
		Proxies: []model.ConfigBackupProxy{}, Monitors: []model.ConfigBackupMonitor{}, Rules: []model.ConfigBackupRule{}, Templates: []model.ConfigBackupTemplate{},
		Channels: []model.ConfigBackupChannel{{
			ID: 1, Name: "Imported DingTalk", Type: model.ChannelTypeDingTalk, Enabled: true,
			Config: json.RawMessage(`{"webhookUrl":"https://oapi.dingtalk.com/robot/send?access_token=token","secret":"secret","clearSecret":true,"messageType":"text"}`),
		}},
	}
	body, err := json.Marshal(model.ConfigImportRequest{Mode: "merge", Backup: backup})
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.Post(server.URL+"/api/config/import", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	responseBody, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusUnprocessableEntity || !bytes.Contains(responseBody, []byte("backup.channels.0.config.clearSecret")) {
		t.Fatalf("import status = %d body = %s", response.StatusCode, responseBody)
	}
	channels, err := db.ListNotifyChannels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(channels) != 0 {
		t.Fatalf("invalid import persisted channels: %#v", channels)
	}
}

func TestDingTalkChannelValidationAndRedactedBackupPlaceholder(t *testing.T) {
	invalid := model.NotifyChannelInput{
		Name: "Broken DingTalk", Type: model.ChannelTypeDingTalk, Enabled: true,
		Config: json.RawMessage(`{"webhookUrl":"http://127.0.0.1/robot/send","messageType":"feedCard"}`),
	}
	err := validateChannelInput(invalid)
	problem, ok := err.(*problemError)
	if !ok || problem.Fields["config"] == "" {
		t.Fatalf("DingTalk validation error = %#v", err)
	}
	controlErr := validateChannelInput(model.NotifyChannelInput{
		Name: "Backup control", Type: model.ChannelTypeDingTalk, Enabled: true,
		Config: json.RawMessage(`{"webhookUrl":"https://oapi.dingtalk.com/robot/send?access_token=token","messageType":"text","clearSecret":true}`),
	})
	controlProblem, ok := controlErr.(*problemError)
	if !ok || controlProblem.Fields["config.clearSecret"] == "" {
		t.Fatalf("persistent clearSecret validation error = %#v", controlErr)
	}

	fields := map[string]string{}
	redacted := validateBackupSecrets(
		"backup.channels.0",
		json.RawMessage(`{"messageType":"text"}`),
		[]string{"webhookUrl", "secret"},
		channelSecretKeys(model.ChannelTypeDingTalk),
		backupSecretValidationValues(model.ChannelTypeDingTalk),
		false,
		fields,
	)
	if len(fields) != 0 {
		t.Fatalf("redacted backup secret validation fields = %#v", fields)
	}
	if err := validateChannelInput(model.NotifyChannelInput{
		Name: "Restored DingTalk", Type: model.ChannelTypeDingTalk, Enabled: true, Config: redacted,
	}); err != nil {
		t.Fatalf("redacted backup placeholder did not validate: %v (config %s)", err, redacted)
	}
}
