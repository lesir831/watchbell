package checker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/mmcdole/gofeed"
	"github.com/watchbell/watchbell/internal/eventvars"
	"github.com/watchbell/watchbell/internal/model"
)

const maxFeedBytes = 5 * 1024 * 1024

type RSSChecker struct {
	client *http.Client
}

type RSSConfig struct {
	URL               string `json:"url"`
	UserAgent         string `json:"userAgent"`
	TimeoutSeconds    int    `json:"timeoutSeconds"`
	NotifyExisting    bool   `json:"notifyExisting"`
	MaxSeenItems      int    `json:"maxSeenItems"`
	IncludeFullText   bool   `json:"includeFullText"`
	DisableHTTPHeader bool   `json:"disableHttpHeader"`
}

type rssState struct {
	Initialized  bool              `json:"initialized"`
	ETag         string            `json:"etag,omitempty"`
	LastModified string            `json:"lastModified,omitempty"`
	Seen         map[string]string `json:"seen,omitempty"`
}

type rssFetchResult struct {
	Feed         *gofeed.Feed
	ETag         string
	LastModified string
	NotModified  bool
}

func NewRSSChecker() *RSSChecker {
	return &RSSChecker{client: &http.Client{}}
}

func (c *RSSChecker) Type() string {
	return model.MonitorTypeRSS
}

func (c *RSSChecker) Plugin() model.MonitorPlugin {
	return model.MonitorPlugin{
		ID: model.MonitorTypeRSS, Name: "RSS / Atom", Builtin: true,
		Description:            "订阅 RSS、Atom 或 JSON Feed，在出现新条目时生成事件。",
		DefaultIntervalSeconds: 300,
		DefaultConfig: map[string]any{
			"url": "https://example.com/feed.xml", "timeoutSeconds": 15,
			"notifyExisting": false, "includeFullText": false,
		},
		ConfigFields: []model.PluginConfigField{
			{Key: "url", Label: "订阅地址", Type: "url", Required: true},
			{Key: "timeoutSeconds", Label: "超时时间（秒）", Type: "number"},
			{Key: "notifyExisting", Label: "首次检查通知已有条目", Type: "boolean"},
			{Key: "includeFullText", Label: "包含完整正文", Type: "boolean"},
		},
		Events:            []string{"rss.item"},
		TemplateVariables: eventvars.EventVariableKeys(model.MonitorTypeRSS),
	}
}

func (c *RSSChecker) Check(ctx context.Context, monitor model.Monitor) (model.CheckResult, error) {
	cfg, err := decodeRSSConfig(monitor)
	if err != nil {
		return model.CheckResult{}, err
	}

	state := DecodeState(monitor, rssState{Seen: map[string]string{}})
	if state.Seen == nil {
		state.Seen = map[string]string{}
	}
	etag, lastModified := "", ""
	if !cfg.DisableHTTPHeader {
		etag, lastModified = state.ETag, state.LastModified
	}
	fetched, err := c.fetch(ctx, monitor, cfg, etag, lastModified)
	if err != nil {
		return model.CheckResult{}, err
	}
	if fetched.NotModified {
		return model.CheckResult{
			Status:  "ok",
			Message: "暂无新增文章",
			State:   stateToMap(state),
		}, nil
	}

	now := time.Now().UTC()
	events := make([]model.EventData, 0)
	currentKeys := make([]string, 0, len(fetched.Feed.Items))
	for _, item := range fetched.Feed.Items {
		key := rssItemKey(item)
		if key == "" {
			continue
		}
		currentKeys = append(currentKeys, key)
		_, seen := state.Seen[key]
		if !seen && (state.Initialized || cfg.NotifyExisting) {
			events = append(events, rssItemEvent(fetched.Feed, item, cfg.IncludeFullText))
		}
		state.Seen[key] = now.Format(time.RFC3339Nano)
	}

	state.Initialized = true
	state.ETag = fetched.ETag
	state.LastModified = fetched.LastModified
	trimRSSSeen(&state, currentKeys, cfg.MaxSeenItems)

	message := "暂无新增文章"
	if len(events) > 0 {
		message = fmt.Sprintf("新增 %d 篇文章", len(events))
	}
	return model.CheckResult{
		Status:  "ok",
		Message: message,
		State:   stateToMap(state),
		Events:  events,
	}, nil
}

// Inspect always downloads and parses the feed instead of using the monitor's
// conditional-request state. The first valid item is the feed's current item;
// no returned state is applied to the monitor.
func (c *RSSChecker) Inspect(ctx context.Context, monitor model.Monitor) (model.Observation, error) {
	cfg, err := decodeRSSConfig(monitor)
	if err != nil {
		return model.Observation{}, err
	}
	fetched, err := c.fetch(ctx, monitor, cfg, "", "")
	if err != nil {
		return model.Observation{}, err
	}
	if fetched.NotModified {
		return model.Observation{}, fmt.Errorf("rss source returned 304 without a conditional request")
	}
	for _, item := range fetched.Feed.Items {
		if rssItemKey(item) == "" {
			continue
		}
		event := rssItemEvent(fetched.Feed, item, cfg.IncludeFullText)
		return model.Observation{
			Type: event.Type, Fingerprint: event.Fingerprint, Message: "latest feed item",
			Available: true, Payload: event.Payload,
		}, nil
	}
	return model.Observation{
		Type: "rss.item", Message: "feed contains no items", Available: false,
		Payload: map[string]any{"rss": map[string]any{
			"sourceTitle": fetched.Feed.Title,
			"sourceLink":  fetched.Feed.Link,
		}},
	}, nil
}

func decodeRSSConfig(monitor model.Monitor) (RSSConfig, error) {
	cfg, err := DecodeConfig(monitor, RSSConfig{
		UserAgent:      "WatchBell/0.1",
		TimeoutSeconds: 15,
		MaxSeenItems:   1000,
	})
	if err != nil {
		return RSSConfig{}, err
	}
	if strings.TrimSpace(cfg.URL) == "" {
		return RSSConfig{}, fmt.Errorf("rss url is required")
	}
	if cfg.TimeoutSeconds <= 0 {
		cfg.TimeoutSeconds = 15
	}
	if cfg.MaxSeenItems <= 0 {
		cfg.MaxSeenItems = 1000
	}
	return cfg, nil
}

func (c *RSSChecker) fetch(ctx context.Context, monitor model.Monitor, cfg RSSConfig, etag, lastModified string) (rssFetchResult, error) {
	reqCtx, cancel := context.WithTimeout(ctx, time.Duration(cfg.TimeoutSeconds)*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, cfg.URL, nil)
	if err != nil {
		return rssFetchResult{}, err
	}
	req.Header.Set("User-Agent", cfg.UserAgent)
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	if lastModified != "" {
		req.Header.Set("If-Modified-Since", lastModified)
	}

	client, err := clientForMonitor(c.client, monitor)
	if err != nil {
		return rssFetchResult{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return rssFetchResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified {
		return rssFetchResult{NotModified: true}, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return rssFetchResult{}, fmt.Errorf("rss fetch failed: http %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxFeedBytes+1))
	if err != nil {
		return rssFetchResult{}, err
	}
	if len(body) > maxFeedBytes {
		return rssFetchResult{}, fmt.Errorf("rss body exceeds %d bytes", maxFeedBytes)
	}
	feed, err := gofeed.NewParser().ParseString(string(body))
	if err != nil {
		return rssFetchResult{}, err
	}
	return rssFetchResult{
		Feed: feed, ETag: resp.Header.Get("ETag"), LastModified: resp.Header.Get("Last-Modified"),
	}, nil
}

func rssItemEvent(feed *gofeed.Feed, item *gofeed.Item, includeFullText bool) model.EventData {
	return model.EventData{
		Type: "rss.item", Fingerprint: rssItemKey(item),
		Payload: map[string]any{
			"rss": map[string]any{
				"title":       item.Title,
				"link":        item.Link,
				"author":      authorName(item),
				"summary":     item.Description,
				"content":     contentForItem(item, includeFullText),
				"publishedAt": publishedAt(item),
				"sourceTitle": feed.Title,
				"sourceLink":  feed.Link,
			},
		},
	}
}

func rssItemKey(item *gofeed.Item) string {
	if strings.TrimSpace(item.GUID) != "" {
		return "rss:guid:" + strings.TrimSpace(item.GUID)
	}
	if strings.TrimSpace(item.Link) != "" {
		return "rss:link:" + strings.TrimSpace(item.Link)
	}
	sum := sha256.Sum256([]byte(item.Title + "|" + item.Published + "|" + item.Description))
	return "rss:hash:" + hex.EncodeToString(sum[:])
}

func authorName(item *gofeed.Item) string {
	if item.Author == nil {
		return ""
	}
	return item.Author.Name
}

func publishedAt(item *gofeed.Item) string {
	if item.PublishedParsed != nil {
		return item.PublishedParsed.UTC().Format(time.RFC3339Nano)
	}
	return item.Published
}

func contentForItem(item *gofeed.Item, includeFullText bool) string {
	if includeFullText && item.Content != "" {
		return item.Content
	}
	return item.Description
}

func trimRSSSeen(state *rssState, currentKeys []string, limit int) {
	if len(state.Seen) <= limit {
		return
	}
	keep := map[string]string{}
	for _, key := range currentKeys {
		if value, ok := state.Seen[key]; ok {
			keep[key] = value
		}
		if len(keep) >= limit {
			break
		}
	}
	if len(keep) == 0 {
		state.Seen = map[string]string{}
		return
	}
	state.Seen = keep
}
