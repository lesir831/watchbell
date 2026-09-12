package notifier

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/watchbell/watchbell/internal/model"
)

// WeComConfig uses a self-built enterprise application, which supports both
// outbound notifications and encrypted message callbacks.
type WeComConfig struct {
	CorpID          string   `json:"corpId"`
	CorpSecret      string   `json:"corpSecret"`
	AgentID         int64    `json:"agentId"`
	ToUser          string   `json:"toUser"`
	APIBaseURL      string   `json:"apiBaseUrl"`
	AllowPrivate    bool     `json:"allowPrivate"`
	CommandsEnabled bool     `json:"commandsEnabled"`
	Token           string   `json:"token"`
	EncodingAESKey  string   `json:"encodingAESKey"`
	AllowedUserIDs  []string `json:"allowedUserIds"`
}

func DecodeWeComConfig(raw json.RawMessage) (WeComConfig, error) {
	var cfg WeComConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return cfg, fmt.Errorf("invalid WeCom config field types")
	}
	cfg.CorpID = strings.TrimSpace(cfg.CorpID)
	cfg.ToUser = strings.TrimSpace(cfg.ToUser)
	cfg.APIBaseURL = strings.TrimRight(strings.TrimSpace(cfg.APIBaseURL), "/")
	if cfg.APIBaseURL == "" {
		cfg.APIBaseURL = "https://qyapi.weixin.qq.com"
	}
	if cfg.CorpID == "" || strings.TrimSpace(cfg.CorpSecret) == "" || cfg.AgentID <= 0 {
		return cfg, fmt.Errorf("corpId, corpSecret and a positive agentId are required")
	}
	if len(cfg.CorpID) > 256 || len(cfg.CorpSecret) > 8192 || len(cfg.ToUser) > 8192 {
		return cfg, fmt.Errorf("WeCom credentials or recipients are too long")
	}
	if cfg.ToUser == "" {
		cfg.ToUser = "@all"
	}
	if err := validateWebhookURL(cfg.APIBaseURL, cfg.AllowPrivate); err != nil {
		return cfg, fmt.Errorf("WeCom API address: %w", err)
	}
	u, _ := url.Parse(cfg.APIBaseURL)
	if u.RawQuery != "" || u.ForceQuery || strings.Contains(cfg.APIBaseURL, "${") {
		return cfg, fmt.Errorf("WeCom API address must not contain query parameters or variables")
	}
	if cfg.CommandsEnabled {
		if len(cfg.Token) < 3 || len(cfg.Token) > 32 || strings.IndexFunc(cfg.Token, func(r rune) bool { return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') }) >= 0 {
			return cfg, fmt.Errorf("callback token must contain 3–32 letters or digits")
		}
		key, err := base64.StdEncoding.DecodeString(cfg.EncodingAESKey + "=")
		if err != nil || len(cfg.EncodingAESKey) != 43 || len(key) != 32 {
			return cfg, fmt.Errorf("encodingAESKey must be a 43-character AES key")
		}
		if len(cfg.AllowedUserIDs) == 0 || len(cfg.AllowedUserIDs) > 100 {
			return cfg, fmt.Errorf("commands require 1–100 allowed member UserIDs")
		}
		for i, id := range cfg.AllowedUserIDs {
			cfg.AllowedUserIDs[i] = strings.TrimSpace(id)
			if cfg.AllowedUserIDs[i] == "" || strings.ContainsAny(cfg.AllowedUserIDs[i], "|@ \t\r\n") || len(id) > 256 {
				return cfg, fmt.Errorf("invalid command member UserID")
			}
		}
	}
	return cfg, nil
}

func ValidateWeComConfig(raw json.RawMessage) error { _, err := DecodeWeComConfig(raw); return err }

type weComToken struct {
	value   string
	expires time.Time
}
type WeComNotifier struct {
	mu     sync.Mutex
	tokens map[[32]byte]weComToken
}

func NewWeComNotifier() *WeComNotifier { return &WeComNotifier{tokens: make(map[[32]byte]weComToken)} }
func (n *WeComNotifier) Type() string  { return model.ChannelTypeWeCom }

// Provider error bodies may echo credentials or URLs. Expose only numeric
// error codes; transport errors deliberately omit credential-bearing URLs.
type weComResponse struct {
	ErrCode      *int   `json:"errcode"`
	AccessToken  string `json:"access_token"`
	ExpiresIn    int    `json:"expires_in"`
	InvalidUser  string `json:"invaliduser"`
	InvalidParty string `json:"invalidparty"`
	InvalidTag   string `json:"invalidtag"`
}

func weComRequest(ctx context.Context, cfg WeComConfig, endpoint string, query url.Values, payload any) (weComResponse, error) {
	var result weComResponse
	var body io.Reader
	method := http.MethodGet
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return result, err
		}
		body = bytes.NewReader(data)
		method = http.MethodPost
	}
	req, err := http.NewRequestWithContext(ctx, method, cfg.APIBaseURL+"/cgi-bin/"+endpoint+"?"+query.Encode(), body)
	if err != nil {
		return result, fmt.Errorf("invalid WeCom request")
	}
	req.Header.Set("Content-Type", "application/json")
	transport, err := webhookTransport(nil, cfg.AllowPrivate, net.DefaultResolver.LookupIPAddr)
	if err != nil {
		return result, err
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Timeout: 15 * time.Second, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return result, fmt.Errorf("WeCom request failed or timed out")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return result, fmt.Errorf("WeCom HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024+1))
	if err != nil || len(data) > 64*1024 || json.Unmarshal(data, &result) != nil || result.ErrCode == nil {
		return result, fmt.Errorf("invalid WeCom response")
	}
	if *result.ErrCode != 0 {
		return result, fmt.Errorf("WeCom API error %d", *result.ErrCode)
	}
	if result.InvalidUser != "" || result.InvalidParty != "" || result.InvalidTag != "" {
		return result, fmt.Errorf("WeCom rejected one or more recipients; check member IDs and application visibility")
	}
	return result, nil
}
func (n *WeComNotifier) token(ctx context.Context, cfg WeComConfig, invalid string) (string, error) {
	// Cache identity includes endpoint and credentials so edits and channels are isolated.
	key := sha256.Sum256([]byte(cfg.APIBaseURL + "\x00" + cfg.CorpID + "\x00" + cfg.CorpSecret))
	n.mu.Lock()
	defer n.mu.Unlock()
	now := time.Now()
	cached := n.tokens[key]
	if cached.value != "" && cached.value != invalid && now.Before(cached.expires) {
		return cached.value, nil
	}
	result, err := weComRequest(ctx, cfg, "gettoken", url.Values{"corpid": {cfg.CorpID}, "corpsecret": {cfg.CorpSecret}}, nil)
	if err != nil {
		return "", err
	}
	if result.AccessToken == "" || result.ExpiresIn <= 0 {
		return "", fmt.Errorf("WeCom returned an empty or expired access token")
	}
	if n.tokens == nil {
		n.tokens = make(map[[32]byte]weComToken)
	}
	for k, v := range n.tokens {
		if !now.Before(v.expires) {
			delete(n.tokens, k)
		}
	}
	if len(n.tokens) >= 256 {
		clear(n.tokens)
	}
	lifetime := time.Duration(result.ExpiresIn) * time.Second
	if lifetime > time.Minute {
		lifetime -= time.Minute
	}
	n.tokens[key] = weComToken{result.AccessToken, now.Add(lifetime)}
	return result.AccessToken, nil
}
func (n *WeComNotifier) post(ctx context.Context, cfg WeComConfig, endpoint string, query url.Values, payload any) error {
	token, err := n.token(ctx, cfg, "")
	if err != nil {
		return err
	}
	query.Set("access_token", token)
	result, err := weComRequest(ctx, cfg, endpoint, query, payload)
	if result.ErrCode != nil && (*result.ErrCode == 40014 || *result.ErrCode == 42001 || *result.ErrCode == 40001) {
		token, err = n.token(ctx, cfg, token)
		if err != nil {
			return err
		}
		query.Set("access_token", token)
		_, err = weComRequest(ctx, cfg, endpoint, query, payload)
	}
	return err
}
func (n *WeComNotifier) Send(ctx context.Context, channel model.NotifyChannel, message Message) error {
	cfg, err := DecodeWeComConfig(channel.Config)
	if err != nil {
		return err
	}
	return n.SendText(ctx, cfg, cfg.ToUser, strings.TrimSpace(message.Subject+"\n"+message.Body))
}
func (n *WeComNotifier) SendText(ctx context.Context, cfg WeComConfig, user, content string) error {
	if !utf8.ValidString(content) || content == "" || len(content) > maxWebhookBodyBytes {
		return fmt.Errorf("WeCom message is empty, invalid UTF-8 or too long")
	}
	for len(content) > 0 {
		end := min(len(content), 2048)
		for end < len(content) && !utf8.RuneStart(content[end]) {
			end--
		}
		payload := map[string]any{"touser": user, "agentid": cfg.AgentID, "msgtype": "text", "text": map[string]string{"content": content[:end]}}
		if err := n.post(ctx, cfg, "message/send", url.Values{}, payload); err != nil {
			return err
		}
		content = content[end:]
	}
	return nil
}
func (n *WeComNotifier) SyncMenu(ctx context.Context, cfg WeComConfig) error {
	button := func(name, key string) map[string]string {
		return map[string]string{"type": "click", "name": name, "key": key}
	}
	return n.post(ctx, cfg, "menu/create", url.Values{"agentid": {fmt.Sprint(cfg.AgentID)}}, map[string]any{"button": []any{button("监控列表", "/monitors"), button("运行状态", "/status"), button("指令帮助", "/help")}})
}
