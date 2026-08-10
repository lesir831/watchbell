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

const (
	maxBarkAPNsPayloadBytes  = 4 * 1024
	barkPayloadHeadroomBytes = 512
	barkPayloadBudgetBytes   = maxBarkAPNsPayloadBytes - barkPayloadHeadroomBytes
	barkTruncationMarker     = "…（内容已截断）"
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

// These types mirror the portion of Bark's final APNs payload that WatchBell
// can produce. APNs limits that encoded payload to 4 KiB; the smaller budget
// leaves room for fields added by different Bark server versions.
type barkAPNsPayload struct {
	APS   barkAPS `json:"aps"`
	Group string  `json:"group,omitempty"`
	Icon  string  `json:"icon,omitempty"`
	URL   string  `json:"url,omitempty"`
}

type barkAPS struct {
	Alert          barkAlert `json:"alert"`
	Sound          string    `json:"sound"`
	Category       string    `json:"category"`
	MutableContent int       `json:"mutable-content"`
	ThreadID       string    `json:"thread-id,omitempty"`
}

type barkAlert struct {
	Title string `json:"title,omitempty"`
	Body  string `json:"body,omitempty"`
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
	if err := preprocessBarkPayload(body); err != nil {
		return err
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

func preprocessBarkPayload(payload map[string]string) error {
	fits, err := barkPayloadFits(payload)
	if err != nil {
		return err
	}
	if fits {
		return nil
	}

	// The title is the most useful part of a compact push, so consume the
	// available budget from the body first. An unusually long title is the
	// fallback truncation target.
	for _, field := range []string{"body", "title"} {
		if err := truncateBarkPayloadField(payload, field); err != nil {
			return err
		}
		fits, err = barkPayloadFits(payload)
		if err != nil {
			return err
		}
		if fits {
			return nil
		}
	}

	return fmt.Errorf("bark payload metadata exceeds the safe %d-byte APNs budget", barkPayloadBudgetBytes)
}

func truncateBarkPayloadField(payload map[string]string, field string) error {
	original := payload[field]
	if original == "" {
		return nil
	}
	runes := []rune(original)

	// If the other fields do not fit on their own, leave this field empty so
	// the next truncation target can give up space as well.
	payload[field] = ""
	fits, err := barkPayloadFits(payload)
	if err != nil {
		return err
	}
	if !fits {
		return nil
	}

	low, high, best := 0, len(runes)-1, -1
	for low <= high {
		middle := low + (high-low)/2
		payload[field] = string(runes[:middle]) + barkTruncationMarker
		fits, err = barkPayloadFits(payload)
		if err != nil {
			return err
		}
		if fits {
			best = middle
			low = middle + 1
		} else {
			high = middle - 1
		}
	}

	if best < 0 {
		payload[field] = ""
		return nil
	}
	payload[field] = string(runes[:best]) + barkTruncationMarker
	return nil
}

func barkPayloadFits(payload map[string]string) (bool, error) {
	size, err := estimatedBarkAPNsPayloadSize(payload)
	if err != nil {
		return false, fmt.Errorf("encode bark APNs payload estimate: %w", err)
	}
	return size <= barkPayloadBudgetBytes, nil
}

func estimatedBarkAPNsPayloadSize(payload map[string]string) (int, error) {
	title := payload["title"]
	body := payload["body"]
	if title == "" && body == "" {
		body = "Empty Message"
	}

	sound := "1107"
	if configured, exists := payload["sound"]; exists && configured != "" {
		sound = configured
		if !strings.HasSuffix(sound, ".caf") {
			sound += ".caf"
		}
	}
	group := payload["group"]
	estimate := barkAPNsPayload{
		APS: barkAPS{
			Alert:          barkAlert{Title: title, Body: body},
			Sound:          sound,
			Category:       "myNotificationCategory",
			MutableContent: 1,
			ThreadID:       group,
		},
		Group: group,
		Icon:  payload["icon"],
		URL:   payload["url"],
	}
	encoded, err := json.Marshal(estimate)
	if err != nil {
		return 0, err
	}
	return len(encoded), nil
}
