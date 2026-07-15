export type MonitorType = 'rss' | 'testflight' | 'webpage' | 'github_release';
export type ChannelType = 'bark' | 'email' | 'webhook';

export interface PluginConfigField {
  key: string;
  label: string;
  type: string;
  required?: boolean;
  secret?: boolean;
  description?: string;
}

export interface MonitorPlugin {
  id: MonitorType;
  name: string;
  description: string;
  builtin: boolean;
  defaultIntervalSeconds: number;
  defaultConfig: Record<string, unknown>;
  configFields: PluginConfigField[];
  events: string[];
  templateVariables: string[];
}

export interface AuthStatus {
  enabled: boolean;
  username: string;
}

export interface CurrentUser {
  username: string;
}

export interface LoginInput {
  username: string;
  password: string;
}

export interface Monitor {
  id: number;
  name: string;
  type: MonitorType;
  enabled: boolean;
  intervalSeconds: number;
  config: Record<string, unknown>;
  state?: Record<string, unknown>;
  lastCheckedAt?: string;
  lastStatus?: string;
  lastMessage?: string;
  lastError?: string;
  consecutiveFailures: number;
  failureAlertAfter: number;
  failureNotifyChannelIds: number[];
  failureAlertActive: boolean;
  nextCheckAt?: string;
  configuredSecrets?: string[];
  createdAt: string;
  updatedAt: string;
}

export interface MonitorInput {
  name: string;
  type: MonitorType;
  enabled: boolean;
  intervalSeconds: number;
  config: Record<string, unknown>;
  failureAlertAfter: number;
  failureNotifyChannelIds: number[];
}

export interface Rule {
  id: number;
  monitorId: number;
  name: string;
  enabled: boolean;
  condition: Record<string, unknown>;
  notifyChannelIds: number[];
  templateId?: number;
  cooldownSeconds: number;
  quietHours: QuietHours;
  lastFiredAt?: string;
  createdAt: string;
  updatedAt: string;
}

export interface RuleInput {
  monitorId: number;
  name: string;
  enabled: boolean;
  condition: Record<string, unknown>;
  notifyChannelIds: number[];
  templateId?: number | null;
  cooldownSeconds: number;
  quietHours: QuietHours;
}

export interface QuietHours {
  enabled: boolean;
  start?: string;
  end?: string;
  timezone?: string;
}

export interface RuleTestResponse {
  tested: number;
  matched: number;
  results: Array<{
    eventId: number;
    eventType: string;
    matched: string[];
    payload: Record<string, unknown>;
    createdAt: string;
  }>;
}

export interface NotifyChannel {
  id: number;
  name: string;
  type: ChannelType;
  enabled: boolean;
  config: Record<string, unknown>;
  configuredSecrets?: string[];
  createdAt: string;
  updatedAt: string;
}

export interface NotifyChannelInput {
  name: string;
  type: ChannelType;
  enabled: boolean;
  config: Record<string, unknown>;
}

export interface NotificationTemplate {
  id: number;
  name: string;
  subjectTemplate: string;
  bodyTemplate: string;
  isDefault: boolean;
  createdAt: string;
  updatedAt: string;
}

export interface NotificationTemplateInput {
  name: string;
  subjectTemplate: string;
  bodyTemplate: string;
}

export interface EventRecord {
  id: number;
  monitorId: number;
  checkRunId?: number;
  type: string;
  fingerprint: string;
  payload: Record<string, unknown>;
  createdAt: string;
}

export interface NotificationLog {
  id: number;
  eventId: number;
  channelId: number;
  status: string;
  error?: string;
  sentAt?: string;
  createdAt: string;
}

export interface CheckRun {
  id: number;
  monitorId: number;
  monitorName: string;
  monitorType: MonitorType;
  trigger: 'manual' | 'scheduled';
  configSnapshot: Record<string, unknown>;
  status: string;
  message?: string;
  error?: string;
  eventCount: number;
  durationMs: number;
  startedAt: string;
  finishedAt?: string;
  createdAt: string;
}

export interface RuleEvaluation {
  id: number;
  eventId: number;
  ruleId?: number;
  ruleName: string;
  status: 'matched' | 'not_matched' | 'skipped' | 'error';
  reason?: string;
  matched: string[];
  createdAt: string;
}

export interface NotificationAttempt {
  id: number;
  monitorId?: number;
  eventId?: number;
  ruleEvaluationId?: number;
  channelId?: number;
  retryOfId?: number;
  channelName: string;
  channelType: ChannelType;
  kind: 'delivery' | 'test' | 'monitor_failure' | 'monitor_recovery';
  status: 'sent' | 'failed';
  subject: string;
  body: string;
  error?: string;
  attemptNo: number;
  durationMs: number;
  sentAt?: string;
  nextRetryAt?: string;
  retriable: boolean;
  resolved: boolean;
  createdAt: string;
}

export interface DeadLetter {
  eventId: number;
  monitorId: number;
  monitorName: string;
  eventType: string;
  fingerprint: string;
  attempts: number;
  lastError: string;
  eventCreatedAt: string;
  updatedAt: string;
}

export interface AuditLog {
  id: number;
  actor: string;
  action: string;
  entityType: string;
  entityId?: number;
  summary: string;
  changes: Record<string, unknown>;
  createdAt: string;
}

export interface HistoryPage<T> {
  items: T[];
  page: number;
  pageSize: number;
  total: number;
  totalPages: number;
}

export interface HistoryQuery {
  page: number;
  pageSize: number;
  from?: string;
  to?: string;
  monitorId?: number;
  checkRunId?: number;
  eventId?: number;
  ruleId?: number;
  channelId?: number;
  entityId?: number;
  status?: string;
  trigger?: string;
  monitorType?: string;
  type?: string;
  kind?: string;
  channelType?: string;
  actor?: string;
  action?: string;
  entityType?: string;
}

export interface ConfigBackup {
  version: number;
  exportedAt: string;
  includesSecrets: boolean;
  monitors: Array<Record<string, unknown>>;
  rules: Array<Record<string, unknown>>;
  channels: Array<Record<string, unknown>>;
  templates: Array<Record<string, unknown>>;
}

export interface ConfigImportReport {
  version: number;
  mode: 'merge';
  created: { monitors: number; rules: number; channels: number; templates: number };
  updated: { monitors: number; rules: number; channels: number; templates: number };
  idMap: Record<string, Record<string, number>>;
  warnings: string[];
}

export interface DashboardSummary {
  monitorCount: number;
  healthyMonitors: number;
  failingMonitors: number;
  pendingMonitors: number;
  ruleCount: number;
  channelCount: number;
  eventsLast24Hours: number;
  failedAttempts: number;
}

export interface SchedulerHealth {
  startedAt: string;
  lastTickAt?: string;
  workerCount: number;
  inFlight: number;
}

export interface SystemStatus {
  database: string;
  scheduler: SchedulerHealth;
  outbox: { pending: number; processing: number; processed: number; dead_letter: number };
  time: string;
}
