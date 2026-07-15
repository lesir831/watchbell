package notifier

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/watchbell/watchbell/internal/model"
	"github.com/watchbell/watchbell/internal/templatex"
)

type BarkNotifier struct {
	client *http.Client
}

type BarkConfig struct {
	ServerURL string `json:"serverUrl"`
	DeviceKey string `json:"deviceKey"`
	Group     string `json:"group"`
	Sound     string `json:"sound"`
	Icon      string `json:"icon"`
	URL       string `json:"url"`
}

func NewBarkNotifier() *BarkNotifier {
	return &BarkNotifier{client: &http.Client{Timeout: 15 * time.Second}}
}

func (n *BarkNotifier) Type() string {
	return model.ChannelTypeBark
}

func (n *BarkNotifier) Send(ctx context.Context, channel model.NotifyChannel, message Message) error {
	var cfg BarkConfig
	if err := json.Unmarshal(channel.Config, &cfg); err != nil {
		return err
	}
	if strings.TrimSpace(cfg.DeviceKey) == "" {
		return fmt.Errorf("bark deviceKey is required")
	}
	serverURL := strings.TrimRight(strings.TrimSpace(cfg.ServerURL), "/")
	if serverURL == "" {
		serverURL = "https://api.day.app"
	}
	body := map[string]string{
		"device_key": cfg.DeviceKey,
		"title":      message.Subject,
		"body":       message.Body,
	}
	if cfg.Group != "" {
		body["group"] = cfg.Group
	}
	if cfg.Sound != "" {
		body["sound"] = cfg.Sound
	}
	if cfg.Icon != "" {
		body["icon"] = cfg.Icon
	}
	if targetURL := strings.TrimSpace(templatex.Render(cfg.URL, templateData(message))); targetURL != "" {
		body["url"] = targetURL
	}
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, serverURL+"/push", bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	client := n.client
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	requestClient := *client
	// Bark's request body contains the device key. Following a redirect to a
	// different host would disclose that credential, so endpoints must be
	// configured with their final URL.
	requestClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := requestClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		detail := strings.TrimSpace(string(respBody))
		if strings.Contains(strings.ToLower(detail), "<html") {
			detail = http.StatusText(resp.StatusCode)
		}
		if len(detail) > 512 {
			detail = detail[:512] + "…"
		}
		return fmt.Errorf("bark http %d: %s", resp.StatusCode, detail)
	}
	return nil
}
