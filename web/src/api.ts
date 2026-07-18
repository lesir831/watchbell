import type {
  AuthStatus,
  AuditLog,
  CheckRun,
  ChangePasswordInput,
  ConfigBackup,
  ConfigImportReport,
  CurrentUser,
  DashboardSummary,
  DeadLetter,
  EventRecord,
  HistoryPage,
  HistoryQuery,
  LoginInput,
  Monitor,
  MonitorInput,
  MonitorPlugin,
  NotificationLog,
  NotificationAttempt,
  NotificationTemplate,
  NotificationTemplateInput,
  NotifyChannel,
  NotifyChannelInput,
  ProxyProfile,
  ProxyProfileInput,
  Rule,
  RuleEvaluation,
  RuleInput,
  RuleTestResponse,
  SettingsOverview,
  SystemStatus,
  VariableCatalog,
  VariableSnapshot
} from './types';

export class APIError extends Error {
  status: number;
  code?: string;
  requestId?: string;
  fields: Record<string, string>;
  details: Record<string, unknown>;

  constructor(message: string, status: number, data: Record<string, unknown> = {}) {
    super(message);
    this.name = 'APIError';
    this.status = status;
    this.code = typeof data.code === 'string' ? data.code : undefined;
    this.requestId = typeof data.requestId === 'string' ? data.requestId : undefined;
    this.fields = (data.fields as Record<string, string> | undefined) ?? {};
    this.details = data;
  }
}

export const AUTH_EXPIRED_EVENT = 'watchbell:auth-expired';

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
    if (response.status === 401 && !path.startsWith('/api/auth/')) {
      window.dispatchEvent(new Event(AUTH_EXPIRED_EVENT));
    }
    const message = `${data.error ?? `HTTP ${response.status}`}${data.requestId ? ` · 请求 ${data.requestId}` : ''}`;
    throw new APIError(message, response.status, data);
  }
  return data as T;
}

function withQuery(path: string, query: HistoryQuery) {
  const values = new URLSearchParams();
  Object.entries(query).forEach(([key, value]) => {
    if (value !== undefined && value !== null && value !== '') values.set(key, String(value));
  });
  return `${path}?${values.toString()}`;
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
  settings: () => request<SettingsOverview>('/api/settings'),
  changePassword: (body: ChangePasswordInput) => request<{ status: string }>('/api/settings/password', jsonInit('POST', body)),
  listProxies: () => request<ProxyProfile[]>('/api/settings/proxies'),
  createProxy: (body: ProxyProfileInput) => request<ProxyProfile>('/api/settings/proxies', jsonInit('POST', body)),
  updateProxy: (id: number, body: ProxyProfileInput) => request<ProxyProfile>(`/api/settings/proxies/${id}`, jsonInit('PUT', body)),
  deleteProxy: (id: number) => request<void>(`/api/settings/proxies/${id}`, jsonInit('DELETE')),
  listPlugins: () => request<MonitorPlugin[]>('/api/plugins'),
  variableCatalog: () => request<VariableCatalog>('/api/help/variables'),
  dashboard: () => request<DashboardSummary>('/api/dashboard'),
  systemStatus: () => request<SystemStatus>('/api/system/status'),
  diagnostics: () => request<Record<string, unknown>>('/api/diagnostics'),
  exportConfig: (includeSecrets = false) => request<ConfigBackup>(`/api/config/export?includeSecrets=${includeSecrets}`),
  importConfig: (backup: ConfigBackup) => request<ConfigImportReport>('/api/config/import', jsonInit('POST', { mode: 'merge', backup })),

  listMonitors: () => request<Monitor[]>('/api/monitors'),
  createMonitor: (body: MonitorInput) => request<Monitor>('/api/monitors', jsonInit('POST', body)),
  updateMonitor: (id: number, body: MonitorInput) => request<Monitor>(`/api/monitors/${id}`, jsonInit('PUT', body)),
  deleteMonitor: (id: number) => request<void>(`/api/monitors/${id}`, jsonInit('DELETE')),
  checkMonitor: (id: number) => request<{ status: string; eventCount: number; checkRun?: CheckRun }>(`/api/monitors/${id}/check`, jsonInit('POST')),
  monitorVariables: (id: number) => request<VariableSnapshot>(`/api/monitors/${id}/variables`),

  listRules: () => request<Rule[]>('/api/rules'),
  createRule: (body: RuleInput) => request<Rule>('/api/rules', jsonInit('POST', body)),
  testRule: (body: Pick<RuleInput, 'monitorId' | 'condition'> & { limit?: number }) => request<RuleTestResponse>('/api/rules/test', jsonInit('POST', body)),
  updateRule: (id: number, body: RuleInput) => request<Rule>(`/api/rules/${id}`, jsonInit('PUT', body)),
  deleteRule: (id: number) => request<void>(`/api/rules/${id}`, jsonInit('DELETE')),

  listChannels: () => request<NotifyChannel[]>('/api/channels'),
  createChannel: (body: NotifyChannelInput) => request<NotifyChannel>('/api/channels', jsonInit('POST', body)),
  updateChannel: (id: number, body: NotifyChannelInput) => request<NotifyChannel>(`/api/channels/${id}`, jsonInit('PUT', body)),
  deleteChannel: (id: number) => request<void>(`/api/channels/${id}`, jsonInit('DELETE')),
  testChannel: (id: number) => request<NotificationAttempt>(`/api/channels/${id}/test`, jsonInit('POST')),

  listTemplates: () => request<NotificationTemplate[]>('/api/templates'),
  createTemplate: (body: NotificationTemplateInput) => request<NotificationTemplate>('/api/templates', jsonInit('POST', body)),
  updateTemplate: (id: number, body: NotificationTemplateInput) => request<NotificationTemplate>(`/api/templates/${id}`, jsonInit('PUT', body)),
  deleteTemplate: (id: number) => request<void>(`/api/templates/${id}`, jsonInit('DELETE')),
  previewTemplate: (body: Partial<NotificationTemplateInput> & { eventId?: number }) =>
    request<{ subject: string; body: string }>('/api/templates/preview', jsonInit('POST', body)),

  listEvents: () => request<EventRecord[]>('/api/events?limit=100'),
  listEventsPage: (query: HistoryQuery) => request<HistoryPage<EventRecord>>(withQuery('/api/events', query)),
  listNotificationLogs: () => request<NotificationLog[]>('/api/notification-logs?limit=100'),
  listCheckRuns: () => request<CheckRun[]>('/api/check-runs?limit=100'),
  listCheckRunsPage: (query: HistoryQuery) => request<HistoryPage<CheckRun>>(withQuery('/api/check-runs', query)),
  listRuleEvaluations: () => request<RuleEvaluation[]>('/api/rule-evaluations?limit=100'),
  listRuleEvaluationsPage: (query: HistoryQuery) => request<HistoryPage<RuleEvaluation>>(withQuery('/api/rule-evaluations', query)),
  listNotificationAttempts: () => request<NotificationAttempt[]>('/api/notification-attempts?limit=100'),
  listNotificationAttemptsPage: (query: HistoryQuery) => request<HistoryPage<NotificationAttempt>>(withQuery('/api/notification-attempts', query)),
  retryNotificationAttempt: (id: number) => request<NotificationAttempt>(`/api/notification-attempts/${id}/retry`, jsonInit('POST')),
  listDeadLettersPage: (query: Pick<HistoryQuery, 'page' | 'pageSize' | 'monitorId'>) => request<HistoryPage<DeadLetter>>(withQuery('/api/dead-letters', query)),
  retryDeadLetter: (eventId: number) => request<{ status: string; eventId: number }>(`/api/dead-letters/${eventId}/retry`, jsonInit('POST')),
  listAuditLogs: () => request<AuditLog[]>('/api/audit-logs?limit=100'),
  listAuditLogsPage: (query: HistoryQuery) => request<HistoryPage<AuditLog>>(withQuery('/api/audit-logs', query))
};
