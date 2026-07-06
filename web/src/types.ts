export type MonitorType = 'rss' | 'testflight' | 'webpage';
export type ChannelType = 'bark' | 'email';

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
