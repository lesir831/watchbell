package model

import (
	"encoding/json"
	"time"
)

const ConfigBackupVersion = 1

// ConfigBackup is a portable, versioned representation of WatchBell's
// user-managed configuration. Runtime state and history are intentionally not
// included.
type ConfigBackup struct {
	Version         int                    `json:"version"`
	ExportedAt      time.Time              `json:"exportedAt"`
	IncludesSecrets bool                   `json:"includesSecrets"`
	Monitors        []ConfigBackupMonitor  `json:"monitors"`
	Rules           []ConfigBackupRule     `json:"rules"`
	Channels        []ConfigBackupChannel  `json:"channels"`
	Templates       []ConfigBackupTemplate `json:"templates"`
}

type ConfigBackupMonitor struct {
	ID                      int64           `json:"id"`
	Name                    string          `json:"name"`
	Type                    string          `json:"type"`
	Enabled                 bool            `json:"enabled"`
	IntervalSeconds         int             `json:"intervalSeconds"`
	Config                  json.RawMessage `json:"config"`
	FailureAlertAfter       int             `json:"failureAlertAfter"`
	FailureNotifyChannelIDs []int64         `json:"failureNotifyChannelIds"`
	RedactedSecrets         []string        `json:"redactedSecrets,omitempty"`
}

type ConfigBackupRule struct {
	ID               int64           `json:"id"`
	MonitorID        int64           `json:"monitorId"`
	Name             string          `json:"name"`
	Enabled          bool            `json:"enabled"`
	Condition        json.RawMessage `json:"condition"`
	NotifyChannelIDs []int64         `json:"notifyChannelIds"`
	TemplateID       *int64          `json:"templateId,omitempty"`
	CooldownSeconds  int             `json:"cooldownSeconds"`
	QuietHours       QuietHours      `json:"quietHours"`
}

type ConfigBackupChannel struct {
	ID              int64           `json:"id"`
	Name            string          `json:"name"`
	Type            string          `json:"type"`
	Enabled         bool            `json:"enabled"`
	Config          json.RawMessage `json:"config"`
	RedactedSecrets []string        `json:"redactedSecrets,omitempty"`
}

type ConfigBackupTemplate struct {
	ID              int64  `json:"id"`
	Name            string `json:"name"`
	SubjectTemplate string `json:"subjectTemplate"`
	BodyTemplate    string `json:"bodyTemplate"`
	IsDefault       bool   `json:"isDefault"`
}

type ConfigImportRequest struct {
	Mode   string       `json:"mode"`
	Backup ConfigBackup `json:"backup"`
}

type ConfigImportCounts struct {
	Monitors  int `json:"monitors"`
	Rules     int `json:"rules"`
	Channels  int `json:"channels"`
	Templates int `json:"templates"`
}

type ConfigImportIDMap struct {
	Monitors  map[string]int64 `json:"monitors"`
	Rules     map[string]int64 `json:"rules"`
	Channels  map[string]int64 `json:"channels"`
	Templates map[string]int64 `json:"templates"`
}

type ConfigImportReport struct {
	Version  int                `json:"version"`
	Mode     string             `json:"mode"`
	Created  ConfigImportCounts `json:"created"`
	Updated  ConfigImportCounts `json:"updated"`
	IDMap    ConfigImportIDMap  `json:"idMap"`
	Warnings []string           `json:"warnings"`
}
