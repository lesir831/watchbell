export type MonitorType = 'rss' | 'testflight' | 'webpage' | 'github_release';
export type ChannelType = 'bark' | 'email';

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
  eventId?: number;
  ruleEvaluationId?: number;
  channelId?: number;
  retryOfId?: number;
  channelName: string;
  channelType: ChannelType;
  kind: 'delivery' | 'test';
  status: 'sent' | 'failed';
  subject: string;
  body: string;
  error?: string;
  attemptNo: number;
  durationMs: number;
  sentAt?: string;
  nextRetryAt?: string;
  createdAt: string;
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
  time: string;
}
