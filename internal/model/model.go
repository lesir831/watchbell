package model

import (
	"encoding/json"
	"time"
)

const (
	MonitorTypeRSS           = "rss"
	MonitorTypeTestFlight    = "testflight"
	MonitorTypeWebpage       = "webpage"
	MonitorTypeGitHubRelease = "github_release"

	ChannelTypeBark     = "bark"
	ChannelTypeDingTalk = "dingtalk"
	ChannelTypeEmail    = "email"
	ChannelTypeWebhook  = "webhook"

	ProxyTypeHTTP   = "http"
	ProxyTypeHTTPS  = "https"
	ProxyTypeSOCKS5 = "socks5"
)

type PluginConfigField struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Type        string `json:"type"`
	Required    bool   `json:"required,omitempty"`
	Secret      bool   `json:"secret,omitempty"`
	Description string `json:"description,omitempty"`
}

type MonitorPlugin struct {
	ID                     string              `json:"id"`
	Name                   string              `json:"name"`
	Description            string              `json:"description"`
	Builtin                bool                `json:"builtin"`
	DefaultIntervalSeconds int                 `json:"defaultIntervalSeconds"`
	DefaultConfig          map[string]any      `json:"defaultConfig"`
	ConfigFields           []PluginConfigField `json:"configFields"`
	Events                 []string            `json:"events"`
	TemplateVariables      []string            `json:"templateVariables"`
}

type Monitor struct {
	ID                      int64           `json:"id"`
	Name                    string          `json:"name"`
	Type                    string          `json:"type"`
	ProxyID                 *int64          `json:"proxyId,omitempty"`
	Proxy                   *ProxyProfile   `json:"-"`
	Enabled                 bool            `json:"enabled"`
	IntervalSeconds         int             `json:"intervalSeconds"`
	Config                  json.RawMessage `json:"config"`
	State                   json.RawMessage `json:"state,omitempty"`
	LastCheckedAt           *time.Time      `json:"lastCheckedAt,omitempty"`
	LastStatus              string          `json:"lastStatus,omitempty"`
	LastMessage             string          `json:"lastMessage,omitempty"`
	LastError               string          `json:"lastError,omitempty"`
	ConsecutiveFailures     int             `json:"consecutiveFailures"`
	FailureAlertAfter       int             `json:"failureAlertAfter"`
	FailureNotifyChannelIDs []int64         `json:"failureNotifyChannelIds"`
	FailureAlertActive      bool            `json:"failureAlertActive"`
	NextCheckAt             *time.Time      `json:"nextCheckAt,omitempty"`
	ConfiguredSecrets       []string        `json:"configuredSecrets,omitempty"`
	CreatedAt               time.Time       `json:"createdAt"`
	UpdatedAt               time.Time       `json:"updatedAt"`
}

type MonitorInput struct {
	Name                    string          `json:"name"`
	Type                    string          `json:"type"`
	ProxyID                 *int64          `json:"proxyId"`
	Enabled                 bool            `json:"enabled"`
	IntervalSeconds         int             `json:"intervalSeconds"`
	Config                  json.RawMessage `json:"config"`
	FailureAlertAfter       int             `json:"failureAlertAfter"`
	FailureNotifyChannelIDs []int64         `json:"failureNotifyChannelIds"`
}

// ProxyProfile is a reusable outbound proxy that can be assigned to one or
// more monitors. Password is intentionally excluded from API serialization;
// sanitized responses advertise its presence through ConfiguredSecrets.
type ProxyProfile struct {
	ID                int64     `json:"id"`
	Name              string    `json:"name"`
	Type              string    `json:"type"`
	Host              string    `json:"host"`
	Port              int       `json:"port"`
	Username          string    `json:"username,omitempty"`
	Password          string    `json:"-"`
	ConfiguredSecrets []string  `json:"configuredSecrets,omitempty"`
	CreatedAt         time.Time `json:"createdAt"`
	UpdatedAt         time.Time `json:"updatedAt"`
}

type ProxyProfileInput struct {
	Name          string `json:"name"`
	Type          string `json:"type"`
	Host          string `json:"host"`
	Port          int    `json:"port"`
	Username      string `json:"username"`
	Password      string `json:"password"`
	ClearPassword bool   `json:"clearPassword,omitempty"`
}

type Rule struct {
	ID               int64           `json:"id"`
	MonitorID        int64           `json:"monitorId"`
	Name             string          `json:"name"`
	Enabled          bool            `json:"enabled"`
	Condition        json.RawMessage `json:"condition"`
	NotifyChannelIDs []int64         `json:"notifyChannelIds"`
	TemplateID       *int64          `json:"templateId,omitempty"`
	CooldownSeconds  int             `json:"cooldownSeconds"`
	QuietHours       QuietHours      `json:"quietHours"`
	LastFiredAt      *time.Time      `json:"lastFiredAt,omitempty"`
	CreatedAt        time.Time       `json:"createdAt"`
	UpdatedAt        time.Time       `json:"updatedAt"`
}

type QuietHours struct {
	Enabled  bool   `json:"enabled"`
	Start    string `json:"start,omitempty"`
	End      string `json:"end,omitempty"`
	Timezone string `json:"timezone,omitempty"`
}

type RuleInput struct {
	MonitorID        int64           `json:"monitorId"`
	Name             string          `json:"name"`
	Enabled          bool            `json:"enabled"`
	Condition        json.RawMessage `json:"condition"`
	NotifyChannelIDs []int64         `json:"notifyChannelIds"`
	TemplateID       *int64          `json:"templateId"`
	CooldownSeconds  int             `json:"cooldownSeconds"`
	QuietHours       QuietHours      `json:"quietHours"`
}

type NotifyChannel struct {
	ID                int64           `json:"id"`
	Name              string          `json:"name"`
	Type              string          `json:"type"`
	Enabled           bool            `json:"enabled"`
	Config            json.RawMessage `json:"config"`
	ConfiguredSecrets []string        `json:"configuredSecrets,omitempty"`
	CreatedAt         time.Time       `json:"createdAt"`
	UpdatedAt         time.Time       `json:"updatedAt"`
}

type NotifyChannelInput struct {
	Name    string          `json:"name"`
	Type    string          `json:"type"`
	Enabled bool            `json:"enabled"`
	Config  json.RawMessage `json:"config"`
}

type NotificationTemplate struct {
	ID              int64     `json:"id"`
	Name            string    `json:"name"`
	SubjectTemplate string    `json:"subjectTemplate"`
	BodyTemplate    string    `json:"bodyTemplate"`
	IsDefault       bool      `json:"isDefault"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

type NotificationTemplateInput struct {
	Name            string `json:"name"`
	SubjectTemplate string `json:"subjectTemplate"`
	BodyTemplate    string `json:"bodyTemplate"`
}

type Event struct {
	ID          int64           `json:"id"`
	MonitorID   int64           `json:"monitorId"`
	CheckRunID  *int64          `json:"checkRunId,omitempty"`
	Type        string          `json:"type"`
	Fingerprint string          `json:"fingerprint"`
	Payload     json.RawMessage `json:"payload"`
	CreatedAt   time.Time       `json:"createdAt"`
}

type EventData struct {
	Type        string         `json:"type"`
	Fingerprint string         `json:"fingerprint"`
	Payload     map[string]any `json:"payload"`
}

// Observation is a live, read-only view of a monitor's source. Unlike an
// EventData value it has not been persisted, deduplicated, or dispatched.
// Available reports whether the source currently exposes the concrete object
// represented by Type (for example an RSS item or a GitHub release).
type Observation struct {
	Type        string         `json:"type"`
	Fingerprint string         `json:"fingerprint,omitempty"`
	Message     string         `json:"message,omitempty"`
	Available   bool           `json:"available"`
	Payload     map[string]any `json:"payload"`
}

type CheckResult struct {
	Status  string         `json:"status"`
	Message string         `json:"message"`
	State   map[string]any `json:"state"`
	Events  []EventData    `json:"events"`
}

type NotificationLog struct {
	ID        int64      `json:"id"`
	EventID   int64      `json:"eventId"`
	ChannelID int64      `json:"channelId"`
	Status    string     `json:"status"`
	Error     string     `json:"error,omitempty"`
	SentAt    *time.Time `json:"sentAt,omitempty"`
	CreatedAt time.Time  `json:"createdAt"`
}

type CheckRun struct {
	ID             int64           `json:"id"`
	MonitorID      int64           `json:"monitorId"`
	MonitorName    string          `json:"monitorName"`
	MonitorType    string          `json:"monitorType"`
	Trigger        string          `json:"trigger"`
	ConfigSnapshot json.RawMessage `json:"configSnapshot"`
	Status         string          `json:"status"`
	Message        string          `json:"message,omitempty"`
	Error          string          `json:"error,omitempty"`
	EventCount     int             `json:"eventCount"`
	DurationMS     int64           `json:"durationMs"`
	StartedAt      time.Time       `json:"startedAt"`
	FinishedAt     *time.Time      `json:"finishedAt,omitempty"`
	CreatedAt      time.Time       `json:"createdAt"`
}

type RuleEvaluation struct {
	ID        int64           `json:"id"`
	EventID   int64           `json:"eventId"`
	RuleID    *int64          `json:"ruleId,omitempty"`
	RuleName  string          `json:"ruleName"`
	Status    string          `json:"status"`
	Reason    string          `json:"reason,omitempty"`
	Matched   json.RawMessage `json:"matched"`
	CreatedAt time.Time       `json:"createdAt"`
}

type NotificationAttempt struct {
	ID               int64           `json:"id"`
	MonitorID        *int64          `json:"monitorId,omitempty"`
	EventID          *int64          `json:"eventId,omitempty"`
	RuleEvaluationID *int64          `json:"ruleEvaluationId,omitempty"`
	ChannelID        *int64          `json:"channelId,omitempty"`
	RetryOfID        *int64          `json:"retryOfId,omitempty"`
	ChannelName      string          `json:"channelName"`
	ChannelType      string          `json:"channelType"`
	Kind             string          `json:"kind"`
	Status           string          `json:"status"`
	Subject          string          `json:"subject"`
	Body             string          `json:"body"`
	Data             json.RawMessage `json:"-"`
	Error            string          `json:"error,omitempty"`
	AttemptNo        int             `json:"attemptNo"`
	DurationMS       int64           `json:"durationMs"`
	SentAt           *time.Time      `json:"sentAt,omitempty"`
	NextRetryAt      *time.Time      `json:"nextRetryAt,omitempty"`
	// Retriable is true only for a failed leaf in the retry chain. Resolved is
	// true once a successor attempt has superseded this row.
	Retriable bool      `json:"retriable"`
	Resolved  bool      `json:"resolved"`
	CreatedAt time.Time `json:"createdAt"`
}

type NotificationAttemptInput struct {
	MonitorID        *int64
	EventID          *int64
	RuleEvaluationID *int64
	ChannelID        *int64
	RetryOfID        *int64
	ChannelName      string
	ChannelType      string
	Kind             string
	Status           string
	Subject          string
	Body             string
	Data             json.RawMessage
	Error            string
	AttemptNo        int
	DurationMS       int64
	SentAt           *time.Time
	NextRetryAt      *time.Time
}

type DeadLetter struct {
	EventID      int64     `json:"eventId"`
	MonitorID    int64     `json:"monitorId"`
	MonitorName  string    `json:"monitorName"`
	EventType    string    `json:"eventType"`
	Fingerprint  string    `json:"fingerprint"`
	Attempts     int       `json:"attempts"`
	LastError    string    `json:"lastError"`
	EventCreated time.Time `json:"eventCreatedAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

type AuditLog struct {
	ID         int64           `json:"id"`
	Actor      string          `json:"actor"`
	Action     string          `json:"action"`
	EntityType string          `json:"entityType"`
	EntityID   *int64          `json:"entityId,omitempty"`
	Summary    string          `json:"summary"`
	Changes    json.RawMessage `json:"changes"`
	CreatedAt  time.Time       `json:"createdAt"`
}

type SchedulerHealth struct {
	StartedAt   time.Time  `json:"startedAt"`
	LastTickAt  *time.Time `json:"lastTickAt,omitempty"`
	WorkerCount int        `json:"workerCount"`
	InFlight    int        `json:"inFlight"`
}

type DashboardSummary struct {
	MonitorCount      int `json:"monitorCount"`
	HealthyMonitors   int `json:"healthyMonitors"`
	FailingMonitors   int `json:"failingMonitors"`
	PendingMonitors   int `json:"pendingMonitors"`
	RuleCount         int `json:"ruleCount"`
	ChannelCount      int `json:"channelCount"`
	EventsLast24Hours int `json:"eventsLast24Hours"`
	FailedAttempts    int `json:"failedAttempts"`
}
