package checker

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/watchbell/watchbell/internal/eventvars"
	"github.com/watchbell/watchbell/internal/model"
)

const maxTestFlightBytes = 2 * 1024 * 1024

type TestFlightChecker struct {
	client *http.Client
}

type TestFlightConfig struct {
	URL               string   `json:"url"`
	UserAgent         string   `json:"userAgent"`
	TimeoutSeconds    int      `json:"timeoutSeconds"`
	AvailablePatterns []string `json:"availablePatterns"`
	FullPatterns      []string `json:"fullPatterns"`
}

type testFlightState struct {
	Initialized bool   `json:"initialized"`
	LastStatus  string `json:"lastStatus,omitempty"`
}

type testFlightSnapshot struct {
	Status  string
	Message string
}

func NewTestFlightChecker() *TestFlightChecker {
	return &TestFlightChecker{client: &http.Client{}}
}

func (c *TestFlightChecker) Type() string {
	return model.MonitorTypeTestFlight
}

func (c *TestFlightChecker) Plugin() model.MonitorPlugin {
	return model.MonitorPlugin{
		ID: model.MonitorTypeTestFlight, Name: "TestFlight", Builtin: true,
		Description:            "监控公开 TestFlight 测试链接，在重新出现测试名额时通知。",
		DefaultIntervalSeconds: 60,
		DefaultConfig: map[string]any{
			"url": "https://testflight.apple.com/join/example", "timeoutSeconds": 15,
		},
		ConfigFields: []model.PluginConfigField{
			{Key: "url", Label: "公开邀请地址", Type: "url", Required: true},
			{Key: "timeoutSeconds", Label: "超时时间（秒）", Type: "number"},
		},
		Events:            []string{"testflight.available"},
		TemplateVariables: eventvars.EventVariableKeys(model.MonitorTypeTestFlight),
	}
}

func (c *TestFlightChecker) Check(ctx context.Context, monitor model.Monitor) (model.CheckResult, error) {
	cfg, err := decodeTestFlightConfig(monitor)
	if err != nil {
		return model.CheckResult{}, err
	}
	state := DecodeState(monitor, testFlightState{})
	snapshot, err := c.fetch(ctx, monitor, cfg)
	if err != nil {
		return model.CheckResult{}, err
	}

	events := []model.EventData{}
	if snapshot.Status == "available" && state.LastStatus != "available" {
		events = append(events, model.EventData{
			Type:        "testflight.available",
			Fingerprint: fmt.Sprintf("testflight:available:%d", time.Now().UTC().Unix()),
			Payload:     testFlightPayload(cfg.URL, snapshot),
		})
	}

	state.Initialized = true
	state.LastStatus = snapshot.Status
	return model.CheckResult{
		Status:  snapshot.Status,
		Message: snapshot.Message,
		State:   stateToMap(state),
		Events:  events,
	}, nil
}

// Inspect returns the currently classified page even when it is full or
// unknown, states for which the regular checker deliberately creates no event.
func (c *TestFlightChecker) Inspect(ctx context.Context, monitor model.Monitor) (model.Observation, error) {
	cfg, err := decodeTestFlightConfig(monitor)
	if err != nil {
		return model.Observation{}, err
	}
	snapshot, err := c.fetch(ctx, monitor, cfg)
	if err != nil {
		return model.Observation{}, err
	}
	return model.Observation{
		Type: "testflight.status", Fingerprint: "testflight:status:" + snapshot.Status,
		Message: snapshot.Message, Available: true, Payload: testFlightPayload(cfg.URL, snapshot),
	}, nil
}

func decodeTestFlightConfig(monitor model.Monitor) (TestFlightConfig, error) {
	cfg, err := DecodeConfig(monitor, TestFlightConfig{
		UserAgent:      "WatchBell/0.1",
		TimeoutSeconds: 15,
		AvailablePatterns: []string{
			"view in testflight",
			"start testing",
			"在 testflight 中查看",
			"开始测试",
		},
		FullPatterns: []string{
			"this beta is full",
			"beta is full",
			"此 beta 版本的测试员已满",
			"测试员已满",
			"not accepting any new testers",
		},
	})
	if err != nil {
		return TestFlightConfig{}, err
	}
	if strings.TrimSpace(cfg.URL) == "" {
		return TestFlightConfig{}, fmt.Errorf("testflight url is required")
	}
	if cfg.TimeoutSeconds <= 0 {
		cfg.TimeoutSeconds = 15
	}
	if len(cfg.AvailablePatterns) == 0 {
		cfg.AvailablePatterns = []string{
			"view in testflight",
			"start testing",
			"在 testflight 中查看",
			"开始测试",
		}
	}
	if len(cfg.FullPatterns) == 0 {
		cfg.FullPatterns = []string{
			"this beta is full",
			"beta is full",
			"此 beta 版本的测试员已满",
			"测试员已满",
			"not accepting any new testers",
		}
	}
	return cfg, nil
}

func (c *TestFlightChecker) fetch(ctx context.Context, monitor model.Monitor, cfg TestFlightConfig) (testFlightSnapshot, error) {
	reqCtx, cancel := context.WithTimeout(ctx, time.Duration(cfg.TimeoutSeconds)*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, cfg.URL, nil)
	if err != nil {
		return testFlightSnapshot{}, err
	}
	req.Header.Set("User-Agent", cfg.UserAgent)
	client, err := clientForMonitor(c.client, monitor)
	if err != nil {
		return testFlightSnapshot{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return testFlightSnapshot{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return testFlightSnapshot{}, fmt.Errorf("testflight fetch failed: http %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxTestFlightBytes+1))
	if err != nil {
		return testFlightSnapshot{}, err
	}
	if len(body) > maxTestFlightBytes {
		return testFlightSnapshot{}, fmt.Errorf("testflight body exceeds %d bytes", maxTestFlightBytes)
	}

	text := strings.ToLower(string(body))
	snapshot := testFlightSnapshot{Status: "unknown", Message: "unable to classify page"}
	if containsAny(text, cfg.FullPatterns) {
		snapshot.Status = "full"
		snapshot.Message = "TestFlight 测试名额已满"
	} else if containsAny(text, cfg.AvailablePatterns) {
		snapshot.Status = "available"
		snapshot.Message = "testflight beta has available slots"
	}
	return snapshot, nil
}

func testFlightPayload(url string, snapshot testFlightSnapshot) map[string]any {
	return map[string]any{
		"testflight": map[string]any{
			"url": url, "status": snapshot.Status, "message": snapshot.Message,
		},
	}
}

func containsAny(text string, patterns []string) bool {
	for _, pattern := range patterns {
		pattern = strings.ToLower(strings.TrimSpace(pattern))
		if pattern != "" && strings.Contains(text, pattern) {
			return true
		}
	}
	return false
}
