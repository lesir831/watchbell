import type { ReactNode } from 'react';
import { Alert, Button, Empty, Result, Space, Tag, Typography } from 'antd';
import { ReloadOutlined } from '@ant-design/icons';
import type { APIError } from '../api';

const { Text } = Typography;

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

export function formatDate(value?: string) {
  if (!value) return '—';
  return new Intl.DateTimeFormat('zh-CN', { dateStyle: 'short', timeStyle: 'medium' }).format(new Date(value));
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
