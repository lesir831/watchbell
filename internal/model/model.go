package model

import (
	"encoding/json"
	"time"
)

const (
	MonitorTypeRSS        = "rss"
	MonitorTypeTestFlight = "testflight"
	MonitorTypeWebpage    = "webpage"

	ChannelTypeBark  = "bark"
	ChannelTypeEmail = "email"
)

type Monitor struct {
	ID              int64           `json:"id"`
	Name            string          `json:"name"`
	Type            string          `json:"type"`
	Enabled         bool            `json:"enabled"`
	IntervalSeconds int             `json:"intervalSeconds"`
	Config          json.RawMessage `json:"config"`
	State           json.RawMessage `json:"state,omitempty"`
	LastCheckedAt   *time.Time      `json:"lastCheckedAt,omitempty"`
	LastStatus      string          `json:"lastStatus,omitempty"`
	LastMessage     string          `json:"lastMessage,omitempty"`
	LastError       string          `json:"lastError,omitempty"`
	CreatedAt       time.Time       `json:"createdAt"`
	UpdatedAt       time.Time       `json:"updatedAt"`
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
	ID        int64           `json:"id"`
	Name      string          `json:"name"`
	Type      string          `json:"type"`
	Enabled   bool            `json:"enabled"`
	Config    json.RawMessage `json:"config"`
	CreatedAt time.Time       `json:"createdAt"`
	UpdatedAt time.Time       `json:"updatedAt"`
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
