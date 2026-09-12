package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/watchbell/watchbell/internal/auth"
	"github.com/watchbell/watchbell/internal/checker"
	"github.com/watchbell/watchbell/internal/model"
	"github.com/watchbell/watchbell/internal/notifier"
	"github.com/watchbell/watchbell/internal/scheduler"
)

func weComTestConfig() notifier.WeComConfig {
	return notifier.WeComConfig{CorpID: "ww-test", CorpSecret: "private-app-secret", AgentID: 1000001, CommandsEnabled: true, Token: "PrivateCallbackToken", EncodingAESKey: base64.RawStdEncoding.EncodeToString(make([]byte, 32)), AllowedUserIDs: []string{"alice"}}
}
func weComEnvelope(t *testing.T, cfg notifier.WeComConfig, plain []byte, stamp int64) ([]byte, url.Values) {
	t.Helper()
	body, err := notifier.EncryptWeCom(cfg, plain, fmt.Sprint(stamp), "nonce")
	if err != nil {
		t.Fatal(err)
	}
	var env struct {
		Encrypt      string
		MsgSignature string
		TimeStamp    string
		Nonce        string
	}
	if err := xml.Unmarshal(body, &env); err != nil {
		t.Fatal(err)
	}
	return body, url.Values{"msg_signature": {env.MsgSignature}, "timestamp": {env.TimeStamp}, "nonce": {env.Nonce}, "echostr": {env.Encrypt}}
}
func weComDecodeReply(t *testing.T, cfg notifier.WeComConfig, body []byte) string {
	t.Helper()
	var env struct {
		Encrypt      string
		MsgSignature string
		TimeStamp    string
		Nonce        string
	}
	if err := xml.Unmarshal(body, &env); err != nil {
		t.Fatalf("reply %s: %v", body, err)
	}
	plain, err := notifier.DecryptWeCom(cfg, env.MsgSignature, env.TimeStamp, env.Nonce, env.Encrypt)
	if err != nil {
		t.Fatal(err)
	}
	var reply struct {
		Content    string
		ToUserName string
	}
	if err := xml.Unmarshal(plain, &reply); err != nil {
		t.Fatal(err)
	}
	if reply.ToUserName != "alice" && reply.ToUserName != "mallory" {
		t.Fatalf("wrong reply recipient %q", reply.ToUserName)
	}
	return reply.Content
}

func TestWeComCallbackAuthCommandsAndDedup(t *testing.T) {
	_, db := newTestServer(t)
	cfg := weComTestConfig()
	raw, _ := json.Marshal(cfg)
	channel, err := db.CreateNotifyChannel(context.Background(), model.NotifyChannelInput{Name: "WeCom", Type: "wecom", Enabled: true, Config: raw})
	if err != nil {
		t.Fatal(err)
	}
	monitor, err := db.CreateMonitor(context.Background(), model.MonitorInput{Name: "Example", Type: "rss", Enabled: true, IntervalSeconds: 300, Config: json.RawMessage(`{"url":"https://example.com/secret"}`)})
	if err != nil {
		t.Fatal(err)
	}
	manager, err := auth.NewManager(auth.Config{Enabled: true, Username: "admin", PasswordHash: "pbkdf2-sha256$210000$wJ7uwPXRx3I5W-CYFTWCqw$ugxmCBayTf_gzUkDj1St3hd8dC5iUedtf98HzjUcbKE", SessionSecret: "01234567890123456789012345678901"}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	app := NewServer(db, scheduler.New(db, checker.NewRegistry(), notifier.NewRegistry(), scheduler.Options{}), "", slog.New(slog.NewTextHandler(io.Discard, nil)), manager)
	handler := app.Routes()
	callback := fmt.Sprintf("/api/wecom/%d/callback", channel.ID)
	invoke := func(method string, body []byte, q url.Values) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(method, callback+"?"+q.Encode(), bytes.NewReader(body))
		handler.ServeHTTP(rec, req)
		return rec
	}
	now := time.Now().Unix()
	_, q := weComEnvelope(t, cfg, []byte("echo-challenge"), now)
	verified := invoke("GET", nil, q)
	if verified.Code != 200 || verified.Body.String() != "echo-challenge" {
		t.Fatalf("verification: %d %s", verified.Code, verified.Body.String())
	}
	private := httptest.NewRecorder()
	handler.ServeHTTP(private, httptest.NewRequest("GET", "/api/channels", nil))
	if private.Code != 401 {
		t.Fatalf("private routes exposed: %d", private.Code)
	}
	incoming := weComIncoming{ToUserName: cfg.CorpID, FromUserName: "alice", AgentID: cfg.AgentID, CreateTime: now, MsgType: "text", MsgID: "100", Content: fmt.Sprintf("/disable %d", monitor.ID)}
	plain, _ := xml.Marshal(incoming)
	body, q := weComEnvelope(t, cfg, plain, now)
	rec := invoke("POST", body, q)
	if rec.Code != 200 || !strings.Contains(weComDecodeReply(t, cfg, rec.Body.Bytes()), "已停用") {
		t.Fatalf("command: %d %s", rec.Code, rec.Body.String())
	}
	updated, _ := db.GetMonitor(context.Background(), monitor.ID)
	if updated.Enabled || !bytes.Equal(updated.Config, monitor.Config) {
		t.Fatal("disable failed or damaged config")
	}
	// Concurrent delivery must not duplicate side effects/audits.
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := invoke("POST", body, q)
			if r.Code != 200 && r.Code != 503 {
				t.Errorf("retry: %d", r.Code)
			}
		}()
	}
	wg.Wait()
	logs, err := db.ListAuditLogs(context.Background(), 100)
	if err != nil || len(logs) != 1 || logs[0].Actor != "wecom:1:alice" {
		t.Fatalf("duplicate audit or actor: %#v %v", logs, err)
	}
	for name, mutate := range map[string]func(*weComIncoming){
		"unauthorized":    func(m *weComIncoming) { m.FromUserName = "mallory" },
		"wrong agent":     func(m *weComIncoming) { m.AgentID++ },
		"wrong recipient": func(m *weComIncoming) { m.ToUserName = "other" },
		"stale message":   func(m *weComIncoming) { m.CreateTime -= 600 },
	} {
		t.Run(name, func(t *testing.T) {
			m := incoming
			m.MsgID = name
			m.Content = fmt.Sprintf("/enable %d", monitor.ID)
			mutate(&m)
			plain, _ := xml.Marshal(m)
			b, q := weComEnvelope(t, cfg, plain, now)
			r := invoke("POST", b, q)
			if name == "unauthorized" {
				if r.Code != 200 || !strings.Contains(weComDecodeReply(t, cfg, r.Body.Bytes()), "没有") {
					t.Fatal("member not rejected")
				}
			} else if r.Code != 403 {
				t.Fatalf("status %d", r.Code)
			}
			m2, _ := db.GetMonitor(context.Background(), monitor.ID)
			if m2.Enabled {
				t.Fatal("unauthorized mutation")
			}
		})
	}
	for _, stale := range []int64{now - 600, now + 600} {
		b, q := weComEnvelope(t, cfg, plain, stale)
		if r := invoke("POST", b, q); r.Code != 403 {
			t.Fatal("stale signature accepted")
		}
	}
	q.Set("msg_signature", strings.Repeat("0", 40))
	if r := invoke("POST", body, q); r.Code != 403 {
		t.Fatal("bad signature accepted")
	}
	// Menu clicks use the same dispatcher and member authorization.
	incoming.MsgType = "event"
	incoming.Event = "click"
	incoming.EventKey = "/monitors"
	incoming.MsgID = ""
	plain, _ = xml.Marshal(incoming)
	body, q = weComEnvelope(t, cfg, plain, now)
	rec = invoke("POST", body, q)
	if rec.Code != 200 || !strings.Contains(weComDecodeReply(t, cfg, rec.Body.Bytes()), "Example") {
		t.Fatalf("menu: %s", rec.Body.String())
	}
	cfg.CommandsEnabled = false
	raw, _ = json.Marshal(cfg)
	_, err = db.UpdateNotifyChannel(context.Background(), channel.ID, model.NotifyChannelInput{Name: channel.Name, Type: channel.Type, Enabled: true, Config: raw})
	if err != nil {
		t.Fatal(err)
	}
	if r := invoke("POST", body, q); r.Code != 404 {
		t.Fatal("disabled commands accepted")
	}
}

func TestWeComChannelLifecycleAndBackup(t *testing.T) {
	server, db := newTestServer(t)
	cfg := weComTestConfig()
	raw, _ := json.Marshal(cfg)
	input := model.NotifyChannelInput{Name: "WeCom", Type: "wecom", Enabled: true, Config: raw}
	body, _ := json.Marshal(input)
	response, err := http.Post(server.URL+"/api/channels", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 201 {
		t.Fatalf("create: %d %s", response.StatusCode, data)
	}
	for _, secret := range []string{cfg.CorpSecret, cfg.Token, cfg.EncodingAESKey} {
		if bytes.Contains(data, []byte(secret)) {
			t.Fatal("secret leaked")
		}
	}
	// Editing a redacted channel retains all callback credentials.
	var saved model.NotifyChannel
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	input.Config = saved.Config
	body, _ = json.Marshal(input)
	req, _ := http.NewRequest("PUT", server.URL+"/api/channels/1", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	response, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	data, _ = io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("edit: %s", data)
	}
	stored, _ := db.GetNotifyChannel(context.Background(), 1)
	if !bytes.Contains(stored.Config, []byte(cfg.CorpSecret)) {
		t.Fatal("secret not retained")
	}
	for _, full := range []bool{false, true} {
		response, err = http.Get(fmt.Sprintf("%s/api/config/export?includeSecrets=%t", server.URL, full))
		if err != nil {
			t.Fatal(err)
		}
		data, _ = io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode != 200 {
			t.Fatalf("backup: %s", data)
		}
		for _, secret := range []string{cfg.CorpSecret, cfg.Token, cfg.EncodingAESKey} {
			if bytes.Contains(data, []byte(secret)) != full {
				t.Fatal("backup secrets incorrect")
			}
		}
		var backup model.ConfigBackup
		if err := json.Unmarshal(data, &backup); err != nil {
			t.Fatal(err)
		}
		if full {
			target, _ := newTestServer(t)
			importBackup(t, target.URL, backup, 200)
		} else {
			importBackup(t, server.URL, backup, 200)
		}
	}
	response, err = http.Post(server.URL+"/api/channels/1/copy", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	data, _ = io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 201 {
		t.Fatalf("copy: %s", data)
	}
	var copied model.NotifyChannel
	json.Unmarshal(data, &copied)
	if copied.Enabled || bytes.Contains(data, []byte(cfg.Token)) {
		t.Fatal("unsafe copy")
	}
	persisted, _ := db.GetNotifyChannel(context.Background(), copied.ID)
	if !bytes.Contains(persisted.Config, []byte(cfg.Token)) {
		t.Fatal("copy lost callback secret")
	}
}

type weComBlockingChecker struct {
	started chan struct{}
	release chan struct{}
}

func (c *weComBlockingChecker) Type() string { return "wecom-test" }
func (c *weComBlockingChecker) Check(ctx context.Context, m model.Monitor) (model.CheckResult, error) {
	close(c.started)
	select {
	case <-c.release:
		return model.CheckResult{Status: "available", State: map[string]any{}}, nil
	case <-ctx.Done():
		return model.CheckResult{}, ctx.Err()
	}
}
func TestWeComCheckAcknowledgesBeforeCompletionAndRepliesOnlyToSender(t *testing.T) {
	_, db := newTestServer(t)
	replies := make(chan string, 4)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/cgi-bin/gettoken" {
			fmt.Fprint(w, `{"errcode":0,"access_token":"test-token","expires_in":7200}`)
			return
		}
		var payload struct {
			ToUser string `json:"touser"`
			Text   struct {
				Content string `json:"content"`
			} `json:"text"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		if payload.ToUser != "alice" {
			t.Errorf("result broadcast to %q", payload.ToUser)
		}
		replies <- payload.Text.Content
		fmt.Fprint(w, `{"errcode":0}`)
	}))
	defer provider.Close()
	cfg := weComTestConfig()
	cfg.APIBaseURL = provider.URL
	cfg.AllowPrivate = true
	raw, _ := json.Marshal(cfg)
	channel, err := db.CreateNotifyChannel(context.Background(), model.NotifyChannelInput{Name: "WeCom", Type: "wecom", Enabled: true, Config: raw})
	if err != nil {
		t.Fatal(err)
	}
	monitor, err := db.CreateMonitor(context.Background(), model.MonitorInput{Name: "Test", Type: "wecom-test", Enabled: true, IntervalSeconds: 300, Config: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	check := &weComBlockingChecker{started: make(chan struct{}), release: make(chan struct{})}
	app := NewServer(db, scheduler.New(db, checker.NewRegistry(check), notifier.NewRegistry(), scheduler.Options{}), "", slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	server := httptest.NewServer(app.Routes())
	defer server.Close()
	incoming := weComIncoming{ToUserName: cfg.CorpID, FromUserName: "alice", AgentID: cfg.AgentID, CreateTime: time.Now().Unix(), MsgType: "text", MsgID: "check-1", Content: fmt.Sprintf("/check %d", monitor.ID)}
	plain, _ := xml.Marshal(incoming)
	body, q := weComEnvelope(t, cfg, plain, time.Now().Unix())
	client := &http.Client{Timeout: time.Second}
	response, err := client.Post(fmt.Sprintf("%s/api/wecom/%d/callback?%s", server.URL, channel.ID, q.Encode()), "application/xml", bytes.NewReader(body))
	if err != nil {
		close(check.release)
		t.Fatal(err)
	}
	data, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 200 || !strings.Contains(weComDecodeReply(t, cfg, data), "已受理") {
		close(check.release)
		t.Fatalf("no immediate acknowledgement: %s", data)
	}
	select {
	case <-check.started:
	case <-time.After(time.Second):
		close(check.release)
		t.Fatal("check did not start")
	}
	close(check.release)
	select {
	case reply := <-replies:
		if !strings.Contains(reply, "检查完成") {
			t.Fatalf("result: %s", reply)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no result reply")
	}
	// Ensure the detached task has completely released its slot before closing DB.
	deadline := time.Now().Add(time.Second)
	for len(app.weComSlots) > 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(app.weComSlots) > 0 {
		t.Fatal("command slot leaked")
	}
	logs, _ := db.ListAuditLogs(context.Background(), 100)
	if len(logs) != 2 {
		t.Fatalf("check audit missing: %#v", logs)
	}
}

func TestWeComMenuSyncUsesSavedApplication(t *testing.T) {
	var synced bool
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/cgi-bin/gettoken" {
			fmt.Fprint(w, `{"errcode":0,"access_token":"menu-token","expires_in":7200}`)
			return
		}
		if r.URL.Path != "/cgi-bin/menu/create" || r.URL.Query().Get("agentid") != "1000001" || r.URL.Query().Get("access_token") != "menu-token" {
			t.Errorf("wrong menu endpoint: %s", r.URL)
		}
		var payload struct {
			Button []struct {
				Type string
				Name string
				Key  string
			}
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		if len(payload.Button) != 3 || payload.Button[0].Key != "/monitors" || payload.Button[2].Key != "/help" {
			t.Errorf("wrong menu: %#v", payload)
		}
		synced = true
		fmt.Fprint(w, `{"errcode":0}`)
	}))
	defer provider.Close()
	server, db := newTestServer(t)
	cfg := weComTestConfig()
	cfg.APIBaseURL = provider.URL
	cfg.AllowPrivate = true
	raw, _ := json.Marshal(cfg)
	_, err := db.CreateNotifyChannel(context.Background(), model.NotifyChannelInput{Name: "WeCom", Type: "wecom", Enabled: true, Config: raw})
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.Post(server.URL+"/api/channels/1/wecom-menu", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if response.StatusCode != 200 || !synced {
		t.Fatalf("menu sync failed: %d", response.StatusCode)
	}
}
