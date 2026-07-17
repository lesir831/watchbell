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
		return model.CheckResult{}, err
	}
	if strings.TrimSpace(cfg.URL) == "" {
		return model.CheckResult{}, fmt.Errorf("testflight url is required")
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
	state := DecodeState(monitor, testFlightState{})

	reqCtx, cancel := context.WithTimeout(ctx, time.Duration(cfg.TimeoutSeconds)*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, cfg.URL, nil)
	if err != nil {
		return model.CheckResult{}, err
	}
	req.Header.Set("User-Agent", cfg.UserAgent)
	resp, err := c.client.Do(req)
	if err != nil {
		return model.CheckResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return model.CheckResult{}, fmt.Errorf("testflight fetch failed: http %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxTestFlightBytes+1))
	if err != nil {
		return model.CheckResult{}, err
	}
	if len(body) > maxTestFlightBytes {
		return model.CheckResult{}, fmt.Errorf("testflight body exceeds %d bytes", maxTestFlightBytes)
	}

	text := strings.ToLower(string(body))
	status := "unknown"
	message := "unable to classify page"
	if containsAny(text, cfg.FullPatterns) {
		status = "full"
		message = "testflight beta is full"
	} else if containsAny(text, cfg.AvailablePatterns) {
		status = "available"
		message = "testflight beta has available slots"
	}

	events := []model.EventData{}
	if status == "available" && state.LastStatus != "available" {
		events = append(events, model.EventData{
			Type:        "testflight.available",
			Fingerprint: fmt.Sprintf("testflight:available:%d", time.Now().UTC().Unix()),
			Payload: map[string]any{
				"testflight": map[string]any{
					"url":     cfg.URL,
					"status":  status,
					"message": message,
				},
			},
		})
	}

	state.Initialized = true
	state.LastStatus = status
	return model.CheckResult{
		Status:  status,
		Message: message,
		State:   stateToMap(state),
		Events:  events,
	}, nil
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
