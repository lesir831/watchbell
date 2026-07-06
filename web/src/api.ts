import type {
  AuthStatus,
  CurrentUser,
  EventRecord,
  LoginInput,
  Monitor,
  MonitorInput,
  NotificationLog,
  NotificationTemplate,
  NotificationTemplateInput,
  NotifyChannel,
  NotifyChannelInput,
  Rule,
  RuleInput
} from './types';

export class APIError extends Error {
  status: number;

  constructor(message: string, status: number) {
    super(message);
    this.name = 'APIError';
    this.status = status;
  }
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(path, {
    ...init,
    credentials: 'same-origin',
    headers: {
      'Content-Type': 'application/json',
      ...(init?.headers ?? {})
    }
  });
  if (response.status === 204) {
    return undefined as T;
  }
  const data = await response.json().catch(() => ({}));
  if (!response.ok) {
    throw new APIError(data.error ?? `HTTP ${response.status}`, response.status);
  }
  return data as T;
}

function jsonInit(method: string, body?: unknown): RequestInit {
  return {
    method,
    body: body === undefined ? undefined : JSON.stringify(body)
  };
}

export const api = {
  health: () => request<{ status: string }>('/api/health'),

  authStatus: () => request<AuthStatus>('/api/auth/status'),
  me: () => request<CurrentUser>('/api/auth/me'),
  login: (body: LoginInput) => request<CurrentUser>('/api/auth/login', jsonInit('POST', body)),
  logout: () => request<{ status: string }>('/api/auth/logout', jsonInit('POST')),

  listMonitors: () => request<Monitor[]>('/api/monitors'),
  createMonitor: (body: MonitorInput) => request<Monitor>('/api/monitors', jsonInit('POST', body)),
  updateMonitor: (id: number, body: MonitorInput) => request<Monitor>(`/api/monitors/${id}`, jsonInit('PUT', body)),
  deleteMonitor: (id: number) => request<void>(`/api/monitors/${id}`, jsonInit('DELETE')),
  checkMonitor: (id: number) => request<{ status: string }>(`/api/monitors/${id}/check`, jsonInit('POST')),

  listRules: () => request<Rule[]>('/api/rules'),
  createRule: (body: RuleInput) => request<Rule>('/api/rules', jsonInit('POST', body)),
  updateRule: (id: number, body: RuleInput) => request<Rule>(`/api/rules/${id}`, jsonInit('PUT', body)),
  deleteRule: (id: number) => request<void>(`/api/rules/${id}`, jsonInit('DELETE')),

  listChannels: () => request<NotifyChannel[]>('/api/channels'),
  createChannel: (body: NotifyChannelInput) => request<NotifyChannel>('/api/channels', jsonInit('POST', body)),
  updateChannel: (id: number, body: NotifyChannelInput) => request<NotifyChannel>(`/api/channels/${id}`, jsonInit('PUT', body)),
  deleteChannel: (id: number) => request<void>(`/api/channels/${id}`, jsonInit('DELETE')),
  testChannel: (id: number) => request<{ status: string }>(`/api/channels/${id}/test`, jsonInit('POST')),

  listTemplates: () => request<NotificationTemplate[]>('/api/templates'),
  createTemplate: (body: NotificationTemplateInput) => request<NotificationTemplate>('/api/templates', jsonInit('POST', body)),
  updateTemplate: (id: number, body: NotificationTemplateInput) => request<NotificationTemplate>(`/api/templates/${id}`, jsonInit('PUT', body)),
  deleteTemplate: (id: number) => request<void>(`/api/templates/${id}`, jsonInit('DELETE')),
  previewTemplate: (body: Partial<NotificationTemplateInput>) =>
    request<{ subject: string; body: string }>('/api/templates/preview', jsonInit('POST', body)),

  listEvents: () => request<EventRecord[]>('/api/events?limit=100'),
  listNotificationLogs: () => request<NotificationLog[]>('/api/notification-logs?limit=100')
};
