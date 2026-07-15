import { useMemo, useState } from 'react';
import { Alert, App as AntApp, Button, Card, Descriptions, Drawer, Input, Space, Statistic, Table, Tabs, Tag, Typography } from 'antd';
import { CopyOutlined, DownloadOutlined, ReloadOutlined, SearchOutlined } from '@ant-design/icons';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from '../api';
import { formatDate, formatDuration, jsonText, PageError, relativeDate, StatusTag } from '../components/Common';
import type { AuditLog, CheckRun, EventRecord, NotificationAttempt, RuleEvaluation } from '../types';

const { Text, Title } = Typography;

type DetailItem = { title: string; data: unknown } | null;

export default function ActivityPage() {
  const { message } = AntApp.useApp();
  const queryClient = useQueryClient();
  const [search, setSearch] = useState('');
  const [detail, setDetail] = useState<DetailItem>(null);
  const monitors = useQuery({ queryKey: ['monitors'], queryFn: api.listMonitors });
  const runs = useQuery({ queryKey: ['checkRuns'], queryFn: api.listCheckRuns });
  const events = useQuery({ queryKey: ['events'], queryFn: api.listEvents });
  const evaluations = useQuery({ queryKey: ['ruleEvaluations'], queryFn: api.listRuleEvaluations });
  const attempts = useQuery({ queryKey: ['notificationAttempts'], queryFn: api.listNotificationAttempts });
  const audits = useQuery({ queryKey: ['auditLogs'], queryFn: api.listAuditLogs });
  const system = useQuery({ queryKey: ['systemStatus'], queryFn: api.systemStatus, refetchInterval: 30_000 });
  const monitorByID = useMemo(() => new Map((monitors.data ?? []).map((item) => [item.id, item.name])), [monitors.data]);
  const retryMutation = useMutation({
    mutationFn: api.retryNotificationAttempt,
    onSuccess: async (item) => { await queryClient.invalidateQueries({ queryKey: ['notificationAttempts'] }); message.success(item.status === 'sent' ? '重试发送成功' : '已记录新的失败尝试'); },
    onError: async (error: Error) => { await queryClient.invalidateQueries({ queryKey: ['notificationAttempts'] }); message.error(error.message); }
  });

  const matches = (value: unknown) => !search || JSON.stringify(value).toLowerCase().includes(search.toLowerCase());
  const toolbar = <Input prefix={<SearchOutlined />} allowClear placeholder="搜索当前记录" value={search} onChange={(event) => setSearch(event.target.value)} style={{ width: 260 }} />;
  const loadingError = runs.error || events.error || evaluations.error || attempts.error || audits.error;
  return (
    <Space direction="vertical" size={16} className="full-width">
      <PageError error={loadingError as Error | null} onRetry={() => { runs.refetch(); events.refetch(); evaluations.refetch(); attempts.refetch(); audits.refetch(); }} />
      <Card className="activity-shell" bordered={false}>
        <Tabs
          tabBarExtraContent={toolbar}
          items={[
            { key: 'runs', label: `检查运行 ${runs.data?.length ?? ''}`, children: <RunsTable data={(runs.data ?? []).filter(matches)} loading={runs.isLoading} onDetail={(item) => setDetail({ title: `检查运行 #${item.id}`, data: item })} /> },
            { key: 'events', label: `事件 ${events.data?.length ?? ''}`, children: <EventsTable data={(events.data ?? []).filter(matches)} loading={events.isLoading} monitorByID={monitorByID} onDetail={(item) => setDetail({ title: `事件 #${item.id}`, data: item })} /> },
            { key: 'evaluations', label: `规则判断 ${evaluations.data?.length ?? ''}`, children: <EvaluationsTable data={(evaluations.data ?? []).filter(matches)} loading={evaluations.isLoading} onDetail={(item) => setDetail({ title: `规则判断 #${item.id}`, data: item })} /> },
            { key: 'attempts', label: `通知尝试 ${attempts.data?.length ?? ''}`, children: <AttemptsTable data={(attempts.data ?? []).filter(matches)} loading={attempts.isLoading} retrying={retryMutation.variables} onRetry={(id) => retryMutation.mutate(id)} onDetail={(item) => setDetail({ title: `通知尝试 #${item.id}`, data: item })} /> },
            { key: 'audit', label: `操作审计 ${audits.data?.length ?? ''}`, children: <AuditTable data={(audits.data ?? []).filter(matches)} loading={audits.isLoading} onDetail={(item) => setDetail({ title: `操作记录 #${item.id}`, data: item })} /> },
            { key: 'system', label: '系统诊断', children: <SystemPanel data={system.data} loading={system.isLoading} onRefresh={() => system.refetch()} /> }
          ]}
        />
      </Card>
      <Drawer title={detail?.title} open={detail !== null} onClose={() => setDetail(null)} width={680}>
        <Button icon={<CopyOutlined />} onClick={() => { navigator.clipboard.writeText(jsonText(detail?.data)); message.success('详情已复制'); }}>复制 JSON</Button>
        <pre className="detail-json">{jsonText(detail?.data)}</pre>
      </Drawer>
    </Space>
  );
}

function RunsTable({ data, loading, onDetail }: { data: CheckRun[]; loading: boolean; onDetail: (item: CheckRun) => void }) {
  return <Table<CheckRun> rowKey="id" loading={loading} dataSource={data} scroll={{ x: 980 }} pagination={{ pageSize: 15, showSizeChanger: false }} columns={[
    { title: 'ID', dataIndex: 'id', width: 75, render: (id, item) => <Button type="link" onClick={() => onDetail(item)}>#{id}</Button> },
    { title: '监控', dataIndex: 'monitorName', width: 180 }, { title: '触发', dataIndex: 'trigger', width: 90, render: (value) => <StatusTag status={value} /> },
    { title: '状态', dataIndex: 'status', width: 90, render: (value) => <StatusTag status={value} /> },
    { title: '消息 / 错误', width: 300, ellipsis: true, render: (_, item) => <Text type={item.error ? 'danger' : undefined}>{item.error || item.message || '—'}</Text> },
    { title: '事件', dataIndex: 'eventCount', width: 75 }, { title: '耗时', dataIndex: 'durationMs', width: 85, render: formatDuration },
    { title: '开始时间', dataIndex: 'startedAt', width: 150, render: (value) => <span title={formatDate(value)}>{relativeDate(value)}</span> }
  ]} />;
}

function EventsTable({ data, loading, monitorByID, onDetail }: { data: EventRecord[]; loading: boolean; monitorByID: Map<number, string>; onDetail: (item: EventRecord) => void }) {
  return <Table<EventRecord> rowKey="id" loading={loading} dataSource={data} scroll={{ x: 880 }} pagination={{ pageSize: 15, showSizeChanger: false }} columns={[
    { title: 'ID', dataIndex: 'id', width: 75, render: (id, item) => <Button type="link" onClick={() => onDetail(item)}>#{id}</Button> },
    { title: '检查', dataIndex: 'checkRunId', width: 85, render: (id) => id ? `#${id}` : '—' },
    { title: '监控', dataIndex: 'monitorId', width: 170, render: (id) => monitorByID.get(id) ?? `已归档监控 #${id}` },
    { title: '类型', dataIndex: 'type', width: 160, render: (value) => <Tag>{value}</Tag> },
    { title: '摘要', dataIndex: 'payload', ellipsis: true, render: (value) => eventSummary(value) },
    { title: '产生时间', dataIndex: 'createdAt', width: 150, render: (value) => <span title={formatDate(value)}>{relativeDate(value)}</span> }
  ]} />;
}

function EvaluationsTable({ data, loading, onDetail }: { data: RuleEvaluation[]; loading: boolean; onDetail: (item: RuleEvaluation) => void }) {
  return <Table<RuleEvaluation> rowKey="id" loading={loading} dataSource={data} scroll={{ x: 820 }} pagination={{ pageSize: 15, showSizeChanger: false }} columns={[
    { title: 'ID', dataIndex: 'id', width: 75, render: (id, item) => <Button type="link" onClick={() => onDetail(item)}>#{id}</Button> },
    { title: '事件', dataIndex: 'eventId', width: 85, render: (id) => `#${id}` }, { title: '规则', dataIndex: 'ruleName', width: 190 },
    { title: '结果', dataIndex: 'status', width: 100, render: (value) => <StatusTag status={value} /> },
    { title: '原因', dataIndex: 'reason', ellipsis: true }, { title: '时间', dataIndex: 'createdAt', width: 150, render: relativeDate }
  ]} />;
}

function AttemptsTable({ data, loading, retrying, onRetry, onDetail }: { data: NotificationAttempt[]; loading: boolean; retrying?: number; onRetry: (id: number) => void; onDetail: (item: NotificationAttempt) => void }) {
  return <Table<NotificationAttempt> rowKey="id" loading={loading} dataSource={data} scroll={{ x: 1080 }} pagination={{ pageSize: 15, showSizeChanger: false }} columns={[
    { title: 'ID', dataIndex: 'id', width: 75, render: (id, item) => <Button type="link" onClick={() => onDetail(item)}>#{id}</Button> },
    { title: '关联', width: 130, render: (_, item) => item.kind === 'test' ? <Tag color="blue">渠道测试</Tag> : `事件 #${item.eventId}` },
    { title: '渠道', dataIndex: 'channelName', width: 160 }, { title: '状态', dataIndex: 'status', width: 90, render: (value) => <StatusTag status={value} /> },
    { title: '尝试', dataIndex: 'attemptNo', width: 75, render: (value) => `第 ${value} 次` },
    { title: '错误', dataIndex: 'error', ellipsis: true, render: (value) => <Text type={value ? 'danger' : undefined}>{value || '—'}</Text> },
    { title: '耗时', dataIndex: 'durationMs', width: 85, render: formatDuration }, { title: '时间', dataIndex: 'createdAt', width: 140, render: relativeDate },
    { title: '操作', width: 100, fixed: 'right', render: (_, item) => item.status === 'failed' ? <Button size="small" icon={<ReloadOutlined />} loading={retrying === item.id} onClick={() => onRetry(item.id)}>重试</Button> : null }
  ]} />;
}

function AuditTable({ data, loading, onDetail }: { data: AuditLog[]; loading: boolean; onDetail: (item: AuditLog) => void }) {
  return <Table<AuditLog> rowKey="id" loading={loading} dataSource={data} scroll={{ x: 760 }} pagination={{ pageSize: 15, showSizeChanger: false }} columns={[
    { title: 'ID', dataIndex: 'id', width: 75, render: (id, item) => <Button type="link" onClick={() => onDetail(item)}>#{id}</Button> },
    { title: '用户', dataIndex: 'actor', width: 120 }, { title: '动作', dataIndex: 'action', width: 100, render: (value) => <Tag>{auditActionLabel(value)}</Tag> },
    { title: '对象', width: 150, render: (_, item) => `${auditEntityLabel(item.entityType)}${item.entityId ? ` #${item.entityId}` : ''}` },
    { title: '摘要', dataIndex: 'summary' }, { title: '时间', dataIndex: 'createdAt', width: 150, render: relativeDate }
  ]} />;
}

function SystemPanel({ data, loading, onRefresh }: { data: Awaited<ReturnType<typeof api.systemStatus>> | undefined; loading: boolean; onRefresh: () => void }) {
  const { message } = AntApp.useApp();
  const diagnostics = useMutation({
    mutationFn: api.diagnostics,
    onSuccess: (value) => {
      const blob = new Blob([JSON.stringify(value, null, 2)], { type: 'application/json' });
      const link = document.createElement('a');
      link.href = URL.createObjectURL(blob); link.download = `watchbell-diagnostics-${new Date().toISOString()}.json`; link.click(); URL.revokeObjectURL(link.href);
      message.success('诊断包已导出，敏感配置已脱敏');
    },
    onError: (error: Error) => message.error(error.message)
  });
  if (loading) return <Card loading />;
  return (
    <Space direction="vertical" size={16} className="full-width">
      <Alert type={data?.database === 'ok' && data.scheduler.lastTickAt ? 'success' : 'error'} showIcon message={data?.database === 'ok' ? '数据库连接正常' : '数据库异常'} description={`调度器最近心跳：${relativeDate(data?.scheduler.lastTickAt)}`} />
      <div className="system-metrics">
        <Card><Statistic title="工作线程" value={data?.scheduler.workerCount ?? 0} /></Card>
        <Card><Statistic title="正在检查" value={data?.scheduler.inFlight ?? 0} /></Card>
        <Card><Statistic title="调度器运行时间" value={relativeDate(data?.scheduler.startedAt)} /></Card>
      </div>
      <Space><Button icon={<ReloadOutlined />} onClick={onRefresh}>刷新状态</Button><Button type="primary" icon={<DownloadOutlined />} loading={diagnostics.isPending} onClick={() => diagnostics.mutate()}>导出诊断包</Button></Space>
    </Space>
  );
}

function eventSummary(payload: Record<string, unknown>) {
  const root = Object.values(payload)[0];
  if (root && typeof root === 'object') {
    const value = root as Record<string, unknown>;
    return String(value.title ?? value.name ?? value.message ?? value.summary ?? jsonText(value));
  }
  return jsonText(payload);
}

function auditActionLabel(value: string) {
  return ({ create: '创建', update: '修改', delete: '归档', check: '检查', test: '测试', retry: '重试' } as Record<string, string>)[value] ?? value;
}

function auditEntityLabel(value: string) {
  return ({ monitor: '监控', rule: '规则', channel: '渠道', template: '模板', notification_attempt: '通知尝试' } as Record<string, string>)[value] ?? value;
}
