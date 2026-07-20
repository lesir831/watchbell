package notifier

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/watchbell/watchbell/internal/model"
)

func TestDingTalkNotifierBuildsSupportedMessageFormats(t *testing.T) {
	tests := []struct {
		name       string
		configJSON string
		section    string
		assert     func(*testing.T, map[string]any)
	}{
		{
			name:       "text",
			configJSON: `{"messageType":"text"}`,
			section:    "text",
			assert: func(t *testing.T, message map[string]any) {
				if message["content"] != "Body" {
					t.Fatalf("content = %#v", message["content"])
				}
			},
		},
		{
			name:       "markdown",
			configJSON: `{"messageType":"markdown","title":"${monitor.name} · ${message.subject}","text":"## ${message.body}"}`,
			section:    "markdown",
			assert: func(t *testing.T, message map[string]any) {
				if message["title"] != "Releases · Subject" || message["text"] != "## Body" {
					t.Fatalf("markdown = %#v", message)
				}
			},
		},
		{
			name:       "link",
			configJSON: `{"messageType":"link","extraParams":{"link":{"messageUrl":"${rss.link}","picUrl":"https://example.com/pic.png"},"traceId":"${event.id}"}}`,
			section:    "link",
			assert: func(t *testing.T, message map[string]any) {
				if message["messageUrl"] != "https://example.com/item" || message["picUrl"] != "https://example.com/pic.png" {
					t.Fatalf("link = %#v", message)
				}
			},
		},
		{
			name:       "actionCard",
			configJSON: `{"messageType":"actionCard","extraParams":{"actionCard":{"btnOrientation":"1","btns":[{"title":"Open","actionURL":"${rss.link}"}],"canForward":true}}}`,
			section:    "actionCard",
			assert: func(t *testing.T, message map[string]any) {
				buttons, ok := message["btns"].([]any)
				if !ok || len(buttons) != 1 || message["btnOrientation"] != "1" || message["canForward"] != true {
					t.Fatalf("actionCard = %#v", message)
				}
			},
		},
		{
			name:       "feedCard",
			configJSON: `{"messageType":"feedCard","extraParams":{"feedCard":{"links":[{"title":"${message.subject}","messageURL":"${rss.link}","picURL":"https://example.com/pic.png"}]}}}`,
			section:    "feedCard",
			assert: func(t *testing.T, message map[string]any) {
				links, ok := message["links"].([]any)
				if !ok || len(links) != 1 {
					t.Fatalf("feedCard = %#v", message)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var cfg DingTalkConfig
			if err := json.Unmarshal([]byte(test.configJSON), &cfg); err != nil {
				t.Fatal(err)
			}
			payload, err := buildDingTalkPayload(cfg, Message{
				Subject: "Subject", Body: "Body",
				Data: map[string]any{
					"monitor": map[string]any{"name": "Releases"},
					"rss":     map[string]any{"link": "https://example.com/item"},
					"event":   map[string]any{"id": 42},
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := validateDingTalkPayload(cfg.MessageType, payload); err != nil {
				t.Fatalf("validate payload: %v (%#v)", err, payload)
			}
			if payload["msgtype"] != cfg.MessageType {
				t.Fatalf("msgtype = %#v", payload["msgtype"])
			}
			message, ok := payload[test.section].(map[string]any)
			if !ok {
				t.Fatalf("%s = %#v", test.section, payload[test.section])
			}
			test.assert(t, message)
			if test.name == "link" && payload["traceId"] != "42" {
				t.Fatalf("native top-level parameter was not rendered: %#v", payload)
			}
		})
	}
}

func TestNormalizeDingTalkMarkdownBreaks(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "ordinary soft breaks",
			input: "第一行\n第二行\r\n第三行",
			want:  "第一行  \n第二行  \n第三行",
		},
		{
			name:  "paragraphs and existing hard breaks",
			input: "第一段\n\n第二段第一行  \n第二行\\\n第三行",
			want:  "第一段\n\n第二段第一行  \n第二行\\\n第三行",
		},
		{
			name:  "list block",
			input: "说明第一行\n说明第二行\n\n- 第一项\n  延续内容\n\n  松散列表段落\n  - 嵌套项\n- 第二项\n\n结尾第一行\n结尾第二行",
			want:  "说明第一行  \n说明第二行\n\n- 第一项\n  延续内容\n\n  松散列表段落\n  - 嵌套项\n- 第二项\n\n结尾第一行  \n结尾第二行",
		},
		{
			name:  "list establishes paragraph boundary",
			input: "说明\n1. 第一项\n2. 第二项",
			want:  "说明\n\n1. 第一项\n2. 第二项",
		},
		{
			name:  "fenced and indented code",
			input: "代码如下\n```go\nfirst := 1\n```not-a-close\nsecond := 2\n```\n处理完成\n\n    alpha\n    beta\n\n尾行",
			want:  "代码如下\n\n```go\nfirst := 1\n```not-a-close\nsecond := 2\n```\n\n处理完成\n\n    alpha\n    beta\n\n尾行",
		},
		{
			name:  "setext heading",
			input: "标题\n---\n正文第一行\n正文第二行",
			want:  "标题\n---\n\n正文第一行  \n正文第二行",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := normalizeDingTalkMarkdownBreaks(test.input); got != test.want {
				t.Fatalf("normalizeDingTalkMarkdownBreaks() =\n%q\nwant\n%q", got, test.want)
			}
		})
	}
}

func TestDingTalkMarkdownBreakNormalizationOnlyAffectsMarkdownTextFields(t *testing.T) {
	input := "第一行\n第二行"
	wantMarkdown := "第一行  \n第二行"
	tests := []struct {
		name    string
		config  DingTalkConfig
		section string
		field   string
		want    string
	}{
		{name: "text unchanged", config: DingTalkConfig{MessageType: "text"}, section: "text", field: "content", want: input},
		{
			name: "markdown normalized after extra params merge",
			config: DingTalkConfig{MessageType: "markdown", ExtraParams: map[string]any{
				"markdown": map[string]any{"text": input},
			}},
			section: "markdown", field: "text", want: wantMarkdown,
		},
		{
			name: "link unchanged",
			config: DingTalkConfig{MessageType: "link", ExtraParams: map[string]any{
				"link": map[string]any{"messageUrl": "https://example.com/item"},
			}},
			section: "link", field: "text", want: input,
		},
		{
			name: "actionCard normalized",
			config: DingTalkConfig{MessageType: "actionCard", ExtraParams: map[string]any{
				"actionCard": map[string]any{"singleTitle": "打开", "singleURL": "https://example.com/item"},
			}},
			section: "actionCard", field: "text", want: wantMarkdown,
		},
		{
			name: "feedCard native title unchanged",
			config: DingTalkConfig{MessageType: "feedCard", ExtraParams: map[string]any{
				"feedCard": map[string]any{"links": []any{map[string]any{
					"title": input, "messageURL": "https://example.com/item", "picURL": "https://example.com/pic.png",
				}}},
			}},
			section: "feedCard", field: "links.0.title", want: input,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			payload, err := buildDingTalkPayload(test.config, Message{Subject: "标题", Body: input})
			if err != nil {
				t.Fatal(err)
			}
			section := payload[test.section].(map[string]any)
			var got string
			if test.field == "links.0.title" {
				got = section["links"].([]any)[0].(map[string]any)["title"].(string)
			} else {
				got = section[test.field].(string)
			}
			if got != test.want {
				t.Fatalf("%s = %q, want %q", test.field, got, test.want)
			}
		})
	}
}

func TestDingTalkNotifierSignsAndSendsNativeParameters(t *testing.T) {
	fixed := time.Date(2026, time.July, 20, 6, 52, 58, 610*1e6, time.UTC)
	secret := "SEC-signing-secret"
	received := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json; charset=utf-8" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		timestamp := r.URL.Query().Get("timestamp")
		mac := hmac.New(sha256.New, []byte(secret))
		_, _ = mac.Write([]byte(timestamp + "\n" + secret))
		wantSign := base64.StdEncoding.EncodeToString(mac.Sum(nil))
		if timestamp != "1784530378610" || r.URL.Query().Get("sign") != wantSign || r.URL.Query().Get("access_token") != "token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		received <- payload
		_, _ = io.WriteString(w, `{"errcode":0,"errmsg":"ok"}`)
	}))
	defer server.Close()

	config := DingTalkConfig{
		WebhookURL: server.URL + "/robot/send?access_token=token", Secret: secret,
		MessageType: "markdown", AtMobiles: []string{"13800138000"}, AtUserIDs: []string{"manager1"}, IsAtAll: true,
		ExtraParams: map[string]any{"at": map[string]any{"customAtField": "${event.id}"}}, AllowPrivate: true,
	}
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	notifier := NewDingTalkNotifier()
	notifier.now = func() time.Time { return fixed }
	if err := notifier.Send(context.Background(), model.NotifyChannel{Config: raw}, Message{
		Subject: "Subject", Body: "第一行\n第二行", Data: map[string]any{"event": map[string]any{"id": 9}},
	}); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	payload := <-received
	at, ok := payload["at"].(map[string]any)
	if !ok || at["isAtAll"] != true || at["customAtField"] != "9" {
		t.Fatalf("at = %#v", payload["at"])
	}
	markdown, ok := payload["markdown"].(map[string]any)
	if !ok || markdown["text"] != "第一行  \n第二行" {
		t.Fatalf("markdown = %#v", payload["markdown"])
	}
}

func TestDingTalkNotifierReportsProviderErrcode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"errcode":310000,"errmsg":"sign not match"}`)
	}))
	defer server.Close()
	raw, _ := json.Marshal(DingTalkConfig{WebhookURL: server.URL, MessageType: "text", AllowPrivate: true})
	err := NewDingTalkNotifier().Send(context.Background(), model.NotifyChannel{Config: raw}, Message{Body: "test"})
	if err == nil || !strings.Contains(err.Error(), "dingtalk errcode 310000: sign not match") {
		t.Fatalf("Send() error = %v", err)
	}
}

func TestDingTalkConfigRejectsUnsafeAndIncompleteConfiguration(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "http", raw: `{"webhookUrl":"http://example.com/robot/send","messageType":"text"}`, want: "must use https"},
		{name: "template URL", raw: `{"webhookUrl":"https://example.com/${event.token}","messageType":"text"}`, want: "must not contain template variables"},
		{name: "official token", raw: `{"webhookUrl":"https://oapi.dingtalk.com/robot/send","messageType":"text"}`, want: "must include access_token"},
		{name: "type", raw: `{"webhookUrl":"https://example.com/hook","messageType":"html"}`, want: "is not supported"},
		{name: "link URL", raw: `{"webhookUrl":"https://example.com/hook","messageType":"link"}`, want: "link.messageUrl is required"},
		{name: "action", raw: `{"webhookUrl":"https://example.com/hook","messageType":"actionCard"}`, want: "requires singleTitle/singleURL or valid btns"},
		{name: "feed", raw: `{"webhookUrl":"https://example.com/hook","messageType":"feedCard"}`, want: "feedCard.links"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateDingTalkConfig(json.RawMessage(test.raw))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateDingTalkConfig() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestDingTalkConfigAcceptsRequiredFieldsBackedByEventVariables(t *testing.T) {
	tests := []string{
		`{"webhookUrl":"https://example.com/hook","messageType":"link","extraParams":{"link":{"messageUrl":"${rss.link}"}}}`,
		`{"webhookUrl":"https://example.com/hook","messageType":"actionCard","extraParams":{"actionCard":{"singleTitle":"Open","singleURL":"${github.release.htmlUrl}"}}}`,
		`{"webhookUrl":"https://example.com/hook","messageType":"actionCard","extraParams":{"actionCard":{"btns":[{"title":"Open","actionURL":"${url}"}]}}}`,
		`{"webhookUrl":"https://example.com/hook","messageType":"feedCard","extraParams":{"feedCard":{"links":[{"title":"${title}","messageURL":"${url}","picURL":"${imageUrl}"}]}}}`,
	}
	for _, raw := range tests {
		if err := ValidateDingTalkConfig(json.RawMessage(raw)); err != nil {
			t.Errorf("ValidateDingTalkConfig(%s) error = %v", raw, err)
		}
	}
}

func TestDingTalkNotifierRejectsRedirectsAndInvalidSuccessResponse(t *testing.T) {
	targetRequests := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetRequests++
		_, _ = io.WriteString(w, `{"errcode":0,"errmsg":"ok"}`)
	}))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	raw, _ := json.Marshal(DingTalkConfig{WebhookURL: redirect.URL, MessageType: "text", AllowPrivate: true})
	err := NewDingTalkNotifier().Send(context.Background(), model.NotifyChannel{Config: raw}, Message{Body: "test"})
	if err == nil || !strings.Contains(err.Error(), "dingtalk http 307") || targetRequests != 0 {
		t.Fatalf("redirect error = %v, target requests = %d", err, targetRequests)
	}

	invalid := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"errmsg":"ok"}`)
	}))
	defer invalid.Close()
	raw, _ = json.Marshal(DingTalkConfig{WebhookURL: invalid.URL, MessageType: "text", AllowPrivate: true})
	err = NewDingTalkNotifier().Send(context.Background(), model.NotifyChannel{Config: raw}, Message{Body: "test"})
	if err == nil || !strings.Contains(err.Error(), "missing errcode") {
		t.Fatalf("invalid response error = %v", err)
	}
}
