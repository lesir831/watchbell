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

	ChannelTypeBark  = "bark"
	ChannelTypeEmail = "email"
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
	ID                  int64           `json:"id"`
	Name                string          `json:"name"`
	Type                string          `json:"type"`
	Enabled             bool            `json:"enabled"`
	IntervalSeconds     int             `json:"intervalSeconds"`
	Config              json.RawMessage `json:"config"`
	State               json.RawMessage `json:"state,omitempty"`
	LastCheckedAt       *time.Time      `json:"lastCheckedAt,omitempty"`
	LastStatus          string          `json:"lastStatus,omitempty"`
	LastMessage         string          `json:"lastMessage,omitempty"`
	LastError           string          `json:"lastError,omitempty"`
	ConsecutiveFailures int             `json:"consecutiveFailures"`
	NextCheckAt         *time.Time      `json:"nextCheckAt,omitempty"`
	ConfiguredSecrets   []string        `json:"configuredSecrets,omitempty"`
	CreatedAt           time.Time       `json:"createdAt"`
	UpdatedAt           time.Time       `json:"updatedAt"`
}

type MonitorInput struct {
	Name            string          `json:"name"`
	Type            string          `json:"type"`
	Enabled         bool            `json:"enabled"`
	IntervalSeconds int             `json:"intervalSeconds"`
	Config          json.RawMessage `json:"config"`
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
	LastFiredAt      *time.Time      `json:"lastFiredAt,omitempty"`
	CreatedAt        time.Time       `json:"createdAt"`
	UpdatedAt        time.Time       `json:"updatedAt"`
}

type RuleInput struct {
	MonitorID        int64           `json:"monitorId"`
	Name             string          `json:"name"`
	Enabled          bool            `json:"enabled"`
	Condition        json.RawMessage `json:"condition"`
	NotifyChannelIDs []int64         `json:"notifyChannelIds"`
	TemplateID       *int64          `json:"templateId"`
	CooldownSeconds  int             `json:"cooldownSeconds"`
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
	ID               int64      `json:"id"`
	EventID          *int64     `json:"eventId,omitempty"`
	RuleEvaluationID *int64     `json:"ruleEvaluationId,omitempty"`
	ChannelID        *int64     `json:"channelId,omitempty"`
	RetryOfID        *int64     `json:"retryOfId,omitempty"`
	ChannelName      string     `json:"channelName"`
	ChannelType      string     `json:"channelType"`
	Kind             string     `json:"kind"`
	Status           string     `json:"status"`
	Subject          string     `json:"subject"`
	Body             string     `json:"body"`
	Error            string     `json:"error,omitempty"`
	AttemptNo        int        `json:"attemptNo"`
	DurationMS       int64      `json:"durationMs"`
	SentAt           *time.Time `json:"sentAt,omitempty"`
	NextRetryAt      *time.Time `json:"nextRetryAt,omitempty"`
	CreatedAt        time.Time  `json:"createdAt"`
}

type NotificationAttemptInput struct {
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
	Error            string
	AttemptNo        int
	DurationMS       int64
	SentAt           *time.Time
	NextRetryAt      *time.Time
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
