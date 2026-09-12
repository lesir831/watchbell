package notifier

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/watchbell/watchbell/internal/model"
)

func TestWeComSendRefreshSplitAndIsolation(t *testing.T) {
	var tokenCalls atomic.Int32
	var mu sync.Mutex
	var contents []string
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/cgi-bin/gettoken" {
			n := tokenCalls.Add(1)
			if r.URL.Query().Get("corpsecret") == "second-secret" {
				fmt.Fprint(w, `{"errcode":0,"access_token":"second-token","expires_in":7200}`)
				return
			}
			fmt.Fprintf(w, `{"errcode":0,"access_token":"token-%d","expires_in":7200}`, n)
			return
		}
		if r.URL.Query().Get("access_token") == "token-1" {
			fmt.Fprint(w, `{"errcode":42001}`)
			return
		}
		var payload struct {
			ToUser  string `json:"touser"`
			AgentID int    `json:"agentid"`
			Text    struct {
				Content string `json:"content"`
			} `json:"text"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		if payload.ToUser != "alice|bob" || payload.AgentID != 1000002 || len(payload.Text.Content) > 2048 || !utf8.ValidString(payload.Text.Content) {
			t.Errorf("invalid message: %#v", payload)
		}
		mu.Lock()
		contents = append(contents, payload.Text.Content)
		mu.Unlock()
		fmt.Fprint(w, `{"errcode":0}`)
	}))
	defer provider.Close()
	cfg := WeComConfig{CorpID: "ww-example", CorpSecret: "first-secret", AgentID: 1000002, ToUser: "alice|bob", APIBaseURL: provider.URL, AllowPrivate: true}
	raw, _ := json.Marshal(cfg)
	n := NewWeComNotifier()
	message := Message{Subject: "标题", Body: strings.Repeat("中文内容🙂", 400)}
	if err := n.Send(context.Background(), model.NotifyChannel{Config: raw}, message); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	combined := strings.Join(contents, "")
	mu.Unlock()
	if combined != message.Subject+"\n"+message.Body || tokenCalls.Load() != 2 {
		t.Fatalf("chunks corrupted or token refresh failed: tokens=%d", tokenCalls.Load())
	}
	if err := n.Send(context.Background(), model.NotifyChannel{Config: raw}, Message{Body: "cached"}); err != nil {
		t.Fatal(err)
	}
	if tokenCalls.Load() != 2 {
		t.Fatal("token was not cached")
	}
	cfg.CorpSecret = "second-secret"
	raw, _ = json.Marshal(cfg)
	if err := n.Send(context.Background(), model.NotifyChannel{Config: raw}, Message{Body: "isolated"}); err != nil {
		t.Fatal(err)
	}
	if tokenCalls.Load() != 3 {
		t.Fatal("credential edit reused old token")
	}
}

func TestWeComProviderFailuresAndRedirects(t *testing.T) {
	for _, body := range []string{`{}`, `{"errcode":0,"invaliduser":"secret-user"}`, `{"errcode":60020,"errmsg":"sensitive-secret"}`, `not json`, strings.Repeat("x", 65537)} {
		t.Run(body[:min(len(body), 35)], func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
			defer server.Close()
			_, err := weComRequest(context.Background(), WeComConfig{APIBaseURL: server.URL, AllowPrivate: true}, "message/send", nil, map[string]string{"test": "test"})
			if err == nil || strings.Contains(err.Error(), "sensitive-secret") || strings.Contains(err.Error(), "secret-user") {
				t.Fatalf("unsafe or missing error: %v", err)
			}
		})
	}
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("redirect followed") }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 302) }))
	defer redirect.Close()
	_, err := weComRequest(context.Background(), WeComConfig{APIBaseURL: redirect.URL, AllowPrivate: true}, "gettoken", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "302") {
		t.Fatalf("redirect: %v", err)
	}
}

func TestWeComConfigValidation(t *testing.T) {
	base := WeComConfig{CorpID: "ww-test", CorpSecret: "secret", AgentID: 1000001, CommandsEnabled: true, Token: "CallbackToken", EncodingAESKey: base64.RawStdEncoding.EncodeToString(make([]byte, 32)), AllowedUserIDs: []string{"alice"}}
	raw, _ := json.Marshal(base)
	if err := ValidateWeComConfig(raw); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*WeComConfig){
		"no secret":          func(c *WeComConfig) { c.CorpSecret = "" },
		"no agent":           func(c *WeComConfig) { c.AgentID = 0 },
		"bad key":            func(c *WeComConfig) { c.EncodingAESKey = "short" },
		"no members":         func(c *WeComConfig) { c.AllowedUserIDs = nil },
		"all members":        func(c *WeComConfig) { c.AllowedUserIDs = []string{"@all"} },
		"bad token":          func(c *WeComConfig) { c.Token = "secret with spaces" },
		"private":            func(c *WeComConfig) { c.APIBaseURL = "https://127.0.0.1" },
		"unsupported scheme": func(c *WeComConfig) { c.APIBaseURL = "ftp://example.com" },
		"private HTTP":       func(c *WeComConfig) { c.APIBaseURL = "http://127.0.0.1" },
		"query":              func(c *WeComConfig) { c.APIBaseURL = "https://example.com?secret=x" },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := base
			mutate(&cfg)
			raw, _ := json.Marshal(cfg)
			if ValidateWeComConfig(raw) == nil {
				t.Fatal("accepted invalid config")
			}
		})
	}
}

func TestWeComCryptoRoundtripAndTampering(t *testing.T) {
	cfg := WeComConfig{CorpID: "ww-test", Token: "CallbackToken", EncodingAESKey: base64.RawStdEncoding.EncodeToString(make([]byte, 32))}
	timestamp := fmt.Sprint(time.Now().Unix())
	for _, content := range []string{"echo", "<xml>中文 &amp; 🙂</xml>", strings.Repeat("x", 128)} {
		encrypted, err := EncryptWeCom(cfg, []byte(content), timestamp, "nonce")
		if err != nil {
			t.Fatal(err)
		}
		var envelope struct {
			Encrypt      string
			MsgSignature string
			TimeStamp    string
			Nonce        string
		}
		if err := xml.Unmarshal(encrypted, &envelope); err != nil {
			t.Fatal(err)
		}
		plain, err := DecryptWeCom(cfg, envelope.MsgSignature, timestamp, "nonce", envelope.Encrypt)
		if err != nil || string(plain) != content {
			t.Fatalf("roundtrip: %s %v", plain, err)
		}
		wrong := cfg
		wrong.CorpID = "different"
		if _, err := DecryptWeCom(wrong, envelope.MsgSignature, timestamp, "nonce", envelope.Encrypt); err == nil {
			t.Fatal("wrong corporate ID accepted")
		}
		if _, err := DecryptWeCom(cfg, envelope.MsgSignature, timestamp, "changed", envelope.Encrypt); err == nil {
			t.Fatal("tampered nonce accepted")
		}
		for _, bad := range []string{"", "invalid base64", base64.StdEncoding.EncodeToString(make([]byte, 16)), base64.StdEncoding.EncodeToString(make([]byte, 32))} {
			sig := WeComSignature(cfg.Token, timestamp, "nonce", bad)
			if _, err := DecryptWeCom(cfg, sig, timestamp, "nonce", bad); err == nil {
				t.Fatal("malformed ciphertext accepted")
			}
		}
	}
}

// Fixed independently generated OpenSSL AES-256-CBC vector, using the WeCom
// framing (16 random bytes + uint32 network length + XML + CorpID), PKCS#7/32.
// This catches mutually compatible mistakes in EncryptWeCom/DecryptWeCom.
func TestWeComCryptoIndependentVector(t *testing.T) {
	cfg := WeComConfig{CorpID: "ww-fixture", Token: "FixtureToken", EncodingAESKey: "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8"}
	const encrypted = "4j/AuRx71kQlxVlzbpsMWB4+txHCfZke9nX8cf7xaNJqkqIoY93pErdZnfWtpUnpAg7dWdlpwibjUkdIZvXeNIA6n9eI19mXblh+mXBmkzxkIYV0DT6HuEakPouAG+U2"
	plain, err := DecryptWeCom(cfg, "6b86d4fa90a72fb8e21bcba20774ef265fb4a3ac", "1700000000", "fixture-nonce", encrypted)
	if err != nil || string(plain) != "<xml><Content>WatchBell fixture</Content></xml>" {
		t.Fatalf("independent vector: %s %v", plain, err)
	}
}
