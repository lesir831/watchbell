import type { ReactNode } from 'react';
import { Alert, Button, Empty, Result, Space, Tag, Typography } from 'antd';
import { ReloadOutlined } from '@ant-design/icons';
import type { APIError } from '../api';

const { Text } = Typography;

export type DateTimeFormat = 'yyyy-MM-dd HH:mm:ss' | 'yyyy-MM-dd HH:mm' | 'MM-dd-yyyy HH:mm:ss';

let dateTimePreferences: { timezone: string; dateTimeFormat: DateTimeFormat } = {
  timezone: Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC',
  dateTimeFormat: 'yyyy-MM-dd HH:mm:ss'
};

export function configureDateTimePreferences(preferences: { timezone?: string; dateTimeFormat?: string }) {
  const timezone = preferences.timezone?.trim();
  const dateTimeFormat = preferences.dateTimeFormat;
  if (timezone && isValidTimezone(timezone)) dateTimePreferences.timezone = timezone;
  if (isDateTimeFormat(dateTimeFormat)) dateTimePreferences.dateTimeFormat = dateTimeFormat;
}

export function StatusTag({ status }: { status?: string }) {
  const value = status || 'pending';
  const color = statusColor(value);
  return <Tag className={`status-chip status-${statusTone(value)}`} color={color}><span className="status-dot" />{statusLabel(value)}</Tag>;
}

export function PageHeader(props: { eyebrow: ReactNode; title: ReactNode; description: ReactNode; actions?: ReactNode }) {
  return (
    <header className="page-head">
      <div className="page-head-copy">
        <div className="page-eyebrow">{props.eyebrow}</div>
        <h1 className="page-title">{props.title}</h1>
        <p className="page-subtitle">{props.description}</p>
      </div>
      {props.actions && <div className="page-actions">{props.actions}</div>}
    </header>
  );
}

export function PageError({ error, onRetry }: { error?: Error | null; onRetry?: () => void }) {
  if (!error) return null;
  const apiError = error as APIError;
  const fields = Object.values(apiError.fields ?? {});
  return (
    <Alert
      className="page-alert"
      type="error"
      showIcon
      message={error.message}
      description={fields.length > 0 ? fields.join(' ') : '请检查配置或稍后重试。'}
      action={onRetry ? <Button size="small" icon={<ReloadOutlined />} onClick={onRetry}>重试</Button> : undefined}
    />
  );
}

export function LoadError({ error, onRetry }: { error: Error; onRetry: () => void }) {
  return <Result status="error" title="数据加载失败" subTitle={error.message} extra={<Button type="primary" onClick={onRetry}>重新加载</Button>} />;
}

export function EmptyState({ title, description, action }: { title: string; description: string; action?: React.ReactNode }) {
  return <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={<Space direction="vertical" size={4}><Text strong>{title}</Text><Text type="secondary">{description}</Text>{action}</Space>} />;
}

export function formatDate(value?: string, preferences = dateTimePreferences) {
  if (!value) return '—';
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  const parts = dateParts(date, preferences.timezone);
  if (preferences.dateTimeFormat === 'MM-dd-yyyy HH:mm:ss') return `${parts.month}-${parts.day}-${parts.year} ${parts.hour}:${parts.minute}:${parts.second}`;
  const seconds = preferences.dateTimeFormat === 'yyyy-MM-dd HH:mm' ? '' : `:${parts.second}`;
  return `${parts.year}-${parts.month}-${parts.day} ${parts.hour}:${parts.minute}${seconds}`;
}

export function formatTime(value?: string) {
  if (!value) return '—';
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  const parts = dateParts(date, dateTimePreferences.timezone);
  return `${parts.hour}:${parts.minute}:${parts.second}`;
}

export function formatDateWithPreferences(value: string | undefined, timezone: string, dateTimeFormat: DateTimeFormat) {
  return formatDate(value, { timezone, dateTimeFormat });
}

export function relativeDate(value?: string) {
  if (!value) return '从未';
  const delta = new Date(value).getTime() - Date.now();
  const abs = Math.abs(delta);
  const formatter = new Intl.RelativeTimeFormat('zh-CN', { numeric: 'auto' });
  if (abs < 60_000) return formatter.format(Math.round(delta / 1000), 'second');
  if (abs < 3_600_000) return formatter.format(Math.round(delta / 60_000), 'minute');
  if (abs < 86_400_000) return formatter.format(Math.round(delta / 3_600_000), 'hour');
  return formatter.format(Math.round(delta / 86_400_000), 'day');
}

export function formatDuration(ms?: number) {
  if (ms === undefined || ms === null) return '—';
  if (ms < 1000) return `${ms} ms`;
  return `${(ms / 1000).toFixed(ms < 10_000 ? 1 : 0)} s`;
}

export function formatInterval(seconds: number) {
  if (seconds % 86_400 === 0) return `${seconds / 86_400} 天`;
  if (seconds % 3_600 === 0) return `${seconds / 3_600} 小时`;
  if (seconds % 60 === 0) return `${seconds / 60} 分钟`;
  return `${seconds} 秒`;
}

export function jsonText(value: unknown) {
  return JSON.stringify(value ?? {}, null, 2);
}

export function eventTitle(payload: Record<string, unknown>, monitorName = '') {
  const preferred = findNestedValue(payload, ['title', 'name', 'tagName']);
  if (preferred) return preferred;
  if (monitorName.trim()) return monitorName.trim();
  const fallback = findNestedValue(payload, ['message', 'summary']);
  return fallback || '未命名事件';
}

export function RenderedData({ value, path = '' }: { value: unknown; path?: string }) {
  if (Array.isArray(value)) {
    if (!value.length) return <span className="rendered-empty">空列表</span>;
    return <div className="rendered-list">{value.map((item, index) => <div className="rendered-list-item" key={`${path}-${index}`}><span className="rendered-index">{index + 1}</span><RenderedData value={item} path={`${path}.${index}`} /></div>)}</div>;
  }
  if (value && typeof value === 'object') {
    const entries = Object.entries(value as Record<string, unknown>);
    if (!entries.length) return <span className="rendered-empty">空对象</span>;
    return <dl className="rendered-data">{entries.map(([key, item]) => {
      const itemPath = path ? `${path}.${key}` : key;
      const nested = item !== null && typeof item === 'object';
      return <div className={nested ? 'rendered-field rendered-field-nested' : 'rendered-field'} key={itemPath}>
        <dt>{displayFieldName(key)}</dt>
        <dd>{nested ? <RenderedData value={item} path={itemPath} /> : renderScalar(key, item)}</dd>
      </div>;
    })}</dl>;
  }
  return <span>{renderScalar(path.split('.').pop() ?? '', value)}</span>;
}

function renderScalar(key: string, value: unknown) {
  if (value === null || value === undefined || value === '') return <span className="rendered-empty">—</span>;
  if (typeof value === 'boolean') return value ? '是' : '否';
  const text = String(value);
  if (looksLikeDateField(key) && !Number.isNaN(new Date(text).getTime())) return <time dateTime={text}>{formatDate(text)}</time>;
  if (/^https?:\/\//i.test(text)) return <a href={text} target="_blank" rel="noreferrer">{text}</a>;
  return text;
}

function displayFieldName(key: string) {
  return ({
    title: '标题', name: '名称', link: '链接', url: '地址', author: '作者', summary: '摘要', content: '正文',
    status: '状态', message: '消息', publishedAt: '发布时间', createdAt: '创建时间', updatedAt: '更新时间',
    tagName: '版本标签', body: '正文', repository: '仓库', sourceTitle: '订阅源', sourceLink: '订阅源链接'
  } as Record<string, string>)[key] ?? key;
}

function findNestedValue(value: unknown, keys: string[]): string {
  if (!value || typeof value !== 'object') return '';
  const record = value as Record<string, unknown>;
  for (const key of keys) {
    const candidate = record[key];
    if (typeof candidate === 'string' && candidate.trim()) return candidate.trim();
  }
  for (const child of Object.values(record)) {
    const candidate = findNestedValue(child, keys);
    if (candidate) return candidate;
  }
  return '';
}

function looksLikeDateField(key: string) {
  return /(?:time|date|at)$/i.test(key);
}

function dateParts(date: Date, timezone: string) {
  const values = Object.fromEntries(new Intl.DateTimeFormat('en-GB', {
    timeZone: isValidTimezone(timezone) ? timezone : 'UTC', year: 'numeric', month: '2-digit', day: '2-digit',
    hour: '2-digit', minute: '2-digit', second: '2-digit', hourCycle: 'h23'
  }).formatToParts(date).filter((part) => part.type !== 'literal').map((part) => [part.type, part.value]));
  return values as Record<'year' | 'month' | 'day' | 'hour' | 'minute' | 'second', string>;
}

function isValidTimezone(timezone: string) {
  try {
    new Intl.DateTimeFormat('en-US', { timeZone: timezone }).format();
    return true;
  } catch {
    return false;
  }
}

function isDateTimeFormat(value?: string): value is DateTimeFormat {
  return value === 'yyyy-MM-dd HH:mm:ss' || value === 'yyyy-MM-dd HH:mm' || value === 'MM-dd-yyyy HH:mm:ss';
}

function statusColor(status: string) {
  if (['ok', 'available', 'sent', 'matched', 'ready'].includes(status)) return 'green';
  if (['error', 'failed', 'not_ready'].includes(status)) return 'red';
  if (['warning', 'full', 'skipped'].includes(status)) return 'orange';
  if (['running', 'pending', 'scheduled', 'manual'].includes(status)) return 'blue';
  return 'default';
}

function statusTone(status: string) {
  if (['ok', 'available', 'sent', 'matched', 'ready'].includes(status)) return 'success';
  if (['error', 'failed', 'not_ready'].includes(status)) return 'danger';
  if (['warning', 'full', 'skipped'].includes(status)) return 'warning';
  return 'neutral';
}

function statusLabel(status: string) {
  return ({
    ok: '正常', available: '可用', sent: '已发送', matched: '已匹配', ready: '就绪',
    error: '错误', failed: '失败', not_ready: '未就绪', warning: '警告', full: '已满',
    skipped: '已跳过', running: '运行中', pending: '待检查', scheduled: '定时', manual: '手动',
    not_matched: '未匹配', unknown: '未知', disabled: '已停用'
  } as Record<string, string>)[status] ?? status;
}
