package checker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/watchbell/watchbell/internal/eventvars"
	"github.com/watchbell/watchbell/internal/model"
	"golang.org/x/net/html"
)

const maxWebpageBytes = 5 * 1024 * 1024

type WebpageChecker struct {
	client *http.Client
}

type WebpageConfig struct {
	URL            string   `json:"url"`
	Selector       string   `json:"selector"`
	UserAgent      string   `json:"userAgent"`
	TimeoutSeconds int      `json:"timeoutSeconds"`
	IgnorePatterns []string `json:"ignorePatterns"`
}

type webpageState struct {
	Initialized bool   `json:"initialized"`
	LastHash    string `json:"lastHash,omitempty"`
}

func NewWebpageChecker() *WebpageChecker {
	return &WebpageChecker{client: &http.Client{}}
}

func (c *WebpageChecker) Type() string {
	return model.MonitorTypeWebpage
}

func (c *WebpageChecker) Plugin() model.MonitorPlugin {
	return model.MonitorPlugin{
		ID: model.MonitorTypeWebpage, Name: "网页变化", Builtin: true,
		Description:            "抓取网页或指定选择器的文本，在内容发生变化时通知。",
		DefaultIntervalSeconds: 300,
		DefaultConfig: map[string]any{
			"url": "https://example.com", "selector": "", "timeoutSeconds": 15,
			"ignorePatterns": []string{},
		},
		ConfigFields: []model.PluginConfigField{
			{Key: "url", Label: "网页地址", Type: "url", Required: true},
			{Key: "selector", Label: "CSS 选择器", Type: "string"},
			{Key: "timeoutSeconds", Label: "超时时间（秒）", Type: "number"},
			{Key: "ignorePatterns", Label: "忽略模式", Type: "string-list"},
		},
		Events:            []string{"webpage.changed"},
		TemplateVariables: eventvars.EventVariableKeys(model.MonitorTypeWebpage),
	}
}

func (c *WebpageChecker) Check(ctx context.Context, monitor model.Monitor) (model.CheckResult, error) {
	cfg, err := DecodeConfig(monitor, WebpageConfig{
		UserAgent:      "WatchBell/0.1",
		TimeoutSeconds: 15,
	})
	if err != nil {
		return model.CheckResult{}, err
	}
	if strings.TrimSpace(cfg.URL) == "" {
		return model.CheckResult{}, fmt.Errorf("webpage url is required")
	}
	if cfg.TimeoutSeconds <= 0 {
		cfg.TimeoutSeconds = 15
	}
	state := DecodeState(monitor, webpageState{})

	reqCtx, cancel := context.WithTimeout(ctx, time.Duration(cfg.TimeoutSeconds)*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, cfg.URL, nil)
	if err != nil {
		return model.CheckResult{}, err
	}
	req.Header.Set("User-Agent", cfg.UserAgent)
	client, err := clientForMonitor(c.client, monitor)
	if err != nil {
		return model.CheckResult{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return model.CheckResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return model.CheckResult{}, fmt.Errorf("webpage fetch failed: http %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxWebpageBytes+1))
	if err != nil {
		return model.CheckResult{}, err
	}
	if len(body) > maxWebpageBytes {
		return model.CheckResult{}, fmt.Errorf("webpage body exceeds %d bytes", maxWebpageBytes)
	}

	text := string(body)
	if strings.TrimSpace(cfg.Selector) != "" {
		selected, err := selectText(text, cfg.Selector)
		if err != nil {
			return model.CheckResult{}, err
		}
		text = selected
	}
	text = normalizeWebpageText(text, cfg.IgnorePatterns)
	sum := sha256.Sum256([]byte(text))
	hash := hex.EncodeToString(sum[:])

	events := []model.EventData{}
	if state.Initialized && state.LastHash != "" && state.LastHash != hash {
		events = append(events, model.EventData{
			Type:        "webpage.changed",
			Fingerprint: fmt.Sprintf("webpage:changed:%d:%s", time.Now().UTC().Unix(), hash[:12]),
			Payload: map[string]any{
				"webpage": map[string]any{
					"url":      cfg.URL,
					"selector": cfg.Selector,
					"oldHash":  state.LastHash,
					"newHash":  hash,
					"summary":  snippet(text, 400),
				},
			},
		})
	}

	state.Initialized = true
	state.LastHash = hash
	return model.CheckResult{
		Status:  "ok",
		Message: "webpage checked",
		State:   stateToMap(state),
		Events:  events,
	}, nil
}

func selectText(input string, selector string) (string, error) {
	root, err := html.Parse(strings.NewReader(input))
	if err != nil {
		return "", err
	}
	matches := make([]string, 0)
	match := selectorMatcher(strings.TrimSpace(selector))
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node == nil {
			return
		}
		if node.Type == html.ElementNode && match(node) {
			matches = append(matches, nodeText(node))
			return
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(root)
	return strings.Join(matches, " "), nil
}

func selectorMatcher(selector string) func(*html.Node) bool {
	if strings.HasPrefix(selector, "#") {
		id := strings.TrimPrefix(selector, "#")
		return func(node *html.Node) bool {
			return attr(node, "id") == id
		}
	}
	if strings.HasPrefix(selector, ".") {
		className := strings.TrimPrefix(selector, ".")
		return func(node *html.Node) bool {
			for _, value := range strings.Fields(attr(node, "class")) {
				if value == className {
					return true
				}
			}
			return false
		}
	}
	tag := strings.ToLower(selector)
	return func(node *html.Node) bool {
		return node.Data == tag
	}
}

func attr(node *html.Node, name string) string {
	for _, attr := range node.Attr {
		if attr.Key == name {
			return attr.Val
		}
	}
	return ""
}

func nodeText(node *html.Node) string {
	var parts []string
	var walk func(*html.Node)
	walk = func(item *html.Node) {
		if item.Type == html.TextNode {
			parts = append(parts, item.Data)
		}
		for child := item.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(node)
	return strings.Join(parts, " ")
}

func normalizeWebpageText(text string, ignorePatterns []string) string {
	for _, pattern := range ignorePatterns {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			continue
		}
		re, err := regexp.Compile(pattern)
		if err != nil {
			continue
		}
		text = re.ReplaceAllString(text, "")
	}
	fields := strings.Fields(text)
	return strings.Join(fields, " ")
}

func snippet(text string, max int) string {
	text = strings.TrimSpace(text)
	if len(text) <= max {
		return text
	}
	return text[:max]
}
