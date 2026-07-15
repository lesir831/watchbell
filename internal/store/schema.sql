PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS monitors (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  type TEXT NOT NULL,
  enabled INTEGER NOT NULL DEFAULT 1,
  interval_seconds INTEGER NOT NULL DEFAULT 300,
  config_json TEXT NOT NULL DEFAULT '{}',
  state_json TEXT NOT NULL DEFAULT '{}',
  last_checked_at TEXT,
  last_status TEXT NOT NULL DEFAULT '',
  last_message TEXT NOT NULL DEFAULT '',
  last_error TEXT NOT NULL DEFAULT '',
  consecutive_failures INTEGER NOT NULL DEFAULT 0,
  failure_alert_after INTEGER NOT NULL DEFAULT 0,
  failure_notify_channel_ids_json TEXT NOT NULL DEFAULT '[]',
  failure_alert_active INTEGER NOT NULL DEFAULT 0,
  deleted_at TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_monitors_enabled ON monitors(enabled);

CREATE TABLE IF NOT EXISTS rules (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  monitor_id INTEGER NOT NULL REFERENCES monitors(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  enabled INTEGER NOT NULL DEFAULT 1,
  condition_json TEXT NOT NULL DEFAULT '{}',
  notify_channel_ids_json TEXT NOT NULL DEFAULT '[]',
  template_id INTEGER REFERENCES notification_templates(id) ON DELETE SET NULL,
  cooldown_seconds INTEGER NOT NULL DEFAULT 0,
  quiet_hours_json TEXT NOT NULL DEFAULT '{}',
  last_fired_at TEXT,
  deleted_at TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_rules_monitor_id ON rules(monitor_id);

CREATE TABLE IF NOT EXISTS notify_channels (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  type TEXT NOT NULL,
  enabled INTEGER NOT NULL DEFAULT 1,
  config_json TEXT NOT NULL DEFAULT '{}',
  deleted_at TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS notification_templates (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  subject_template TEXT NOT NULL,
  body_template TEXT NOT NULL,
  is_default INTEGER NOT NULL DEFAULT 0,
  deleted_at TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  monitor_id INTEGER NOT NULL REFERENCES monitors(id) ON DELETE CASCADE,
  type TEXT NOT NULL,
  fingerprint TEXT NOT NULL,
  payload_json TEXT NOT NULL DEFAULT '{}',
  created_at TEXT NOT NULL,
  UNIQUE(monitor_id, fingerprint)
);

CREATE INDEX IF NOT EXISTS idx_events_monitor_id ON events(monitor_id);
CREATE INDEX IF NOT EXISTS idx_events_created_at ON events(created_at);
CREATE INDEX IF NOT EXISTS idx_events_monitor_created_at ON events(monitor_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_events_type_created_at ON events(type, created_at DESC);

CREATE TABLE IF NOT EXISTS notification_logs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  event_id INTEGER NOT NULL REFERENCES events(id) ON DELETE CASCADE,
  channel_id INTEGER NOT NULL REFERENCES notify_channels(id) ON DELETE CASCADE,
  status TEXT NOT NULL,
  error TEXT NOT NULL DEFAULT '',
  sent_at TEXT,
  created_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_notification_logs_created_at ON notification_logs(created_at);

CREATE TABLE IF NOT EXISTS check_runs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  monitor_id INTEGER NOT NULL REFERENCES monitors(id),
  monitor_name TEXT NOT NULL,
  monitor_type TEXT NOT NULL,
  trigger TEXT NOT NULL,
  config_json TEXT NOT NULL DEFAULT '{}',
  status TEXT NOT NULL,
  message TEXT NOT NULL DEFAULT '',
  error TEXT NOT NULL DEFAULT '',
  event_count INTEGER NOT NULL DEFAULT 0,
  duration_ms INTEGER NOT NULL DEFAULT 0,
  started_at TEXT NOT NULL,
  finished_at TEXT,
  created_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_check_runs_monitor_id ON check_runs(monitor_id, id DESC);
CREATE INDEX IF NOT EXISTS idx_check_runs_created_at ON check_runs(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_check_runs_status_created_at ON check_runs(status, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_check_runs_trigger_created_at ON check_runs(trigger, created_at DESC);

CREATE TABLE IF NOT EXISTS event_check_runs (
  event_id INTEGER PRIMARY KEY REFERENCES events(id) ON DELETE CASCADE,
  check_run_id INTEGER NOT NULL REFERENCES check_runs(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS rule_evaluations (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  event_id INTEGER NOT NULL REFERENCES events(id) ON DELETE CASCADE,
  rule_id INTEGER REFERENCES rules(id),
  rule_name TEXT NOT NULL,
  status TEXT NOT NULL,
  reason TEXT NOT NULL DEFAULT '',
  matched_json TEXT NOT NULL DEFAULT '[]',
  created_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_rule_evaluations_event_id ON rule_evaluations(event_id, id);
CREATE INDEX IF NOT EXISTS idx_rule_evaluations_created_at ON rule_evaluations(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_rule_evaluations_rule_id ON rule_evaluations(rule_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_rule_evaluations_status_created_at ON rule_evaluations(status, created_at DESC);

CREATE TABLE IF NOT EXISTS notification_attempts (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  monitor_id INTEGER REFERENCES monitors(id),
  event_id INTEGER REFERENCES events(id),
  rule_evaluation_id INTEGER REFERENCES rule_evaluations(id),
  channel_id INTEGER REFERENCES notify_channels(id),
  retry_of_id INTEGER REFERENCES notification_attempts(id),
  channel_name TEXT NOT NULL,
  channel_type TEXT NOT NULL,
  kind TEXT NOT NULL DEFAULT 'delivery',
  status TEXT NOT NULL,
  subject TEXT NOT NULL DEFAULT '',
  body TEXT NOT NULL DEFAULT '',
  data_json TEXT NOT NULL DEFAULT '{}',
  error TEXT NOT NULL DEFAULT '',
  attempt_no INTEGER NOT NULL DEFAULT 1,
  duration_ms INTEGER NOT NULL DEFAULT 0,
  sent_at TEXT,
  next_retry_at TEXT,
  retry_claimed_at TEXT,
  created_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_notification_attempts_created_at ON notification_attempts(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_notification_attempts_retry ON notification_attempts(status, next_retry_at);
CREATE INDEX IF NOT EXISTS idx_notification_attempts_event_id ON notification_attempts(event_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_notification_attempts_channel_id ON notification_attempts(channel_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_notification_attempts_status_created_at ON notification_attempts(status, created_at DESC);

CREATE TABLE IF NOT EXISTS event_outbox (
  event_id INTEGER PRIMARY KEY REFERENCES events(id) ON DELETE CASCADE,
  status TEXT NOT NULL DEFAULT 'pending',
  attempts INTEGER NOT NULL DEFAULT 0,
  next_attempt_at TEXT NOT NULL,
  last_error TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_event_outbox_due ON event_outbox(status, next_attempt_at);

CREATE TABLE IF NOT EXISTS audit_logs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  actor TEXT NOT NULL,
  action TEXT NOT NULL,
  entity_type TEXT NOT NULL,
  entity_id INTEGER,
  summary TEXT NOT NULL,
  changes_json TEXT NOT NULL DEFAULT '{}',
  created_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_audit_logs_created_at ON audit_logs(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_audit_logs_entity ON audit_logs(entity_type, entity_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_audit_logs_action_created_at ON audit_logs(action, created_at DESC);

INSERT OR IGNORE INTO notification_templates (
  id, name, subject_template, body_template, created_at, updated_at
) VALUES (
  1,
  'Default',
  '${monitor.name}: ${event.type}',
  'Monitor: ${monitor.name}
Type: ${event.type}
Time: ${event.time}

${rss.title}${testflight.message}${webpage.summary}${github.release.name} ${github.release.tagName}

${rss.link}${testflight.url}${webpage.url}${github.release.url}',
  strftime('%Y-%m-%dT%H:%M:%fZ', 'now'),
  strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
);

UPDATE notification_templates
SET body_template = 'Monitor: ${monitor.name}
Type: ${event.type}
Time: ${event.time}

${rss.title}${testflight.message}${webpage.summary}${github.release.name} ${github.release.tagName}

${rss.link}${testflight.url}${webpage.url}${github.release.url}',
    updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE id = 1
  AND body_template = 'Monitor: ${monitor.name}
Type: ${event.type}
Time: ${event.time}

${rss.title}${testflight.message}${webpage.summary}

${rss.link}${testflight.url}${webpage.url}';
