import { Alert, App as AntApp, Button, Card, Col, Descriptions, Row, Space, Statistic, Table, Tabs, Tag, Typography } from 'antd';
import { ArrowLeftOutlined, PlayCircleOutlined } from '@ant-design/icons';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from '../api';
import { formatDate, formatDuration, formatInterval, jsonText, PageError, relativeDate, StatusTag } from '../components/Common';
import type { CheckRun, EventRecord, NotificationAttempt, Rule } from '../types';

const { Text, Title } = Typography;

export default function MonitorDetailPage({ monitorId, onNavigate }: { monitorId: number; onNavigate: (page: string) => void }) {
  const { message } = AntApp.useApp();
  const queryClient = useQueryClient();
  const monitors = useQuery({ queryKey: ['monitors'], queryFn: api.listMonitors, refetchInterval: 20_000 });
  const rules = useQuery({ queryKey: ['rules'], queryFn: api.listRules, refetchInterval: 30_000 });
  const channels = useQuery({ queryKey: ['channels'], queryFn: api.listChannels, refetchInterval: 30_000 });
  const runs = useQuery({ queryKey: ['checkRuns', 'monitorDetail', monitorId], queryFn: () => api.listCheckRunsPage({ page: 1, pageSize: 100, monitorId }), refetchInterval: 15_000 });
  const events = useQuery({ queryKey: ['events', 'monitorDetail', monitorId], queryFn: () => api.listEventsPage({ page: 1, pageSize: 100, monitorId }), refetchInterval: 15_000 });
  const attempts = useQuery({ queryKey: ['notificationAttempts', 'monitorDetail', monitorId], queryFn: () => api.listNotificationAttemptsPage({ page: 1, pageSize: 100, monitorId }), refetchInterval: 15_000 });
  const failedAttempts = useQuery({ queryKey: ['notificationAttempts', 'monitorDetail', monitorId, 'failedCount'], queryFn: () => api.listNotificationAttemptsPage({ page: 1, pageSize: 1, monitorId, status: 'failed' }), refetchInterval: 15_000 });
  const monitor = monitors.data?.find((item) => item.id === monitorId);
  const monitorRules = (rules.data ?? []).filter((item) => item.monitorId === monitorId);
  const monitorRuns = runs.data?.items ?? [];
  const monitorEvents = events.data?.items ?? [];
  const monitorAttempts = attempts.data?.items ?? [];
  const channelByID = new Map((channels.data ?? []).map((item) => [item.id, item.name]));
  const check = useMutation({
    mutationFn: () => api.checkMonitor(monitorId),
    onSuccess: (result) => message.success(result.eventCount > 0 ? `检查完成，发现 ${result.eventCount} 个新事件` : '检查完成，未发现新事件'),
    onSettled: async () => {
      await Promise.all(['monitors', 'checkRuns', 'events', 'notificationAttempts'].map((key) => queryClient.invalidateQueries({ queryKey: [key] })));
    }
  });
  const error = monitors.error || rules.error || channels.error || runs.error || events.error || attempts.error || failedAttempts.error || check.error;

  if (monitors.isError) {
    return <Card><PageError error={monitors.error as Error} onRetry={() => monitors.refetch()} /></Card>;
  }
  if (monitors.isLoading) return <Card loading />;
  if (!monitor) {
    return <Card><Alert type="warning" showIcon message="监控不存在或已归档" action={<Button onClick={() => onNavigate('monitors')}>返回监控列表</Button>} /></Card>;
  }

  return (
    <Space direction="vertical" size={16} className="full-width">
      <div className="page-toolbar responsive-toolbar">
        <Button icon={<ArrowLeftOutlined />} onClick={() => onNavigate('monitors')}>返回监控列表</Button>
        <Button type="primary" icon={<PlayCircleOutlined />} loading={check.isPending} onClick={() => check.mutate()}>立即检查</Button>
      </div>
      <PageError error={error as Error | null} onRetry={() => { monitors.refetch(); rules.refetch(); channels.refetch(); runs.refetch(); events.refetch(); attempts.refetch(); failedAttempts.refetch(); }} />
      {monitor && <>
        <Card>
          <div className="detail-heading"><div><Title level={3}>{monitor.name}</Title><Space><Tag>{monitor.type}</Tag><StatusTag status={!monitor.enabled ? 'disabled' : monitor.lastStatus} /></Space></div><Text type="secondary">下次检查：{monitor.enabled ? relativeDate(monitor.nextCheckAt) : '已停用'}</Text></div>
          {(monitor.lastError || monitor.lastMessage) && <Alert className="detail-health-alert" type={monitor.lastError ? 'error' : 'info'} showIcon message={monitor.lastError || monitor.lastMessage} />}
          {monitor.failureAlertActive && <Alert className="detail-health-alert" type="error" showIcon message="已发送监控故障告警" description="恢复正常后会向所选渠道发送恢复通知，并关闭本轮故障状态。" />}
          <Descriptions column={{ xs: 1, sm: 2, lg: 4 }} size="small">
            <Descriptions.Item label="检查频率">{formatInterval(monitor.intervalSeconds)}</Descriptions.Item>
            <Descriptions.Item label="上次检查">{formatDate(monitor.lastCheckedAt)}</Descriptions.Item>
            <Descriptions.Item label="连续失败">{monitor.consecutiveFailures}</Descriptions.Item>
            <Descriptions.Item label="故障告警">{monitor.failureAlertAfter > 0 ? `${monitor.failureAlertAfter} 次失败触发${monitor.failureAlertActive ? ' · 告警中' : ''}` : '关闭'}</Descriptions.Item>
            <Descriptions.Item label="创建时间">{formatDate(monitor.createdAt)}</Descriptions.Item>
          </Descriptions>
        </Card>
        <Row gutter={[16, 16]}>
          <Col xs={12} lg={6}><Card><Statistic title="关联规则" value={monitorRules.length} /></Card></Col>
          <Col xs={12} lg={6}><Card><Statistic title="检查记录" value={runs.data?.total ?? 0} /></Card></Col>
          <Col xs={12} lg={6}><Card><Statistic title="事件记录" value={events.data?.total ?? 0} /></Card></Col>
          <Col xs={12} lg={6}><Card><Statistic title="发送失败" value={failedAttempts.data?.total ?? 0} /></Card></Col>
        </Row>
        <Card>
          <Tabs items={[
            { key: 'runs', label: `检查 ${runs.data?.total ?? 0}`, children: <Runs data={monitorRuns} /> },
            { key: 'events', label: `事件 ${events.data?.total ?? 0}`, children: <Events data={monitorEvents} /> },
            { key: 'rules', label: `规则 ${monitorRules.length}`, children: <Rules data={monitorRules} channelByID={channelByID} /> },
            { key: 'attempts', label: `通知 ${attempts.data?.total ?? 0}`, children: <Attempts data={monitorAttempts} /> },
            { key: 'config', label: '配置', children: <pre className="detail-json">{jsonText(monitor.config)}</pre> }
          ]} />
        </Card>
      </>}
    </Space>
  );
}

function Runs({ data }: { data: CheckRun[] }) {
  return <Table rowKey="id" dataSource={data} pagination={{ pageSize: 10 }} scroll={{ x: 700 }} columns={[
    { title: 'ID', dataIndex: 'id', render: (id) => `#${id}` }, { title: '触发', dataIndex: 'trigger', render: (value) => <StatusTag status={value} /> },
    { title: '结果', dataIndex: 'status', render: (value) => <StatusTag status={value} /> }, { title: '消息 / 错误', render: (_, item) => item.error || item.message || '—' },
    { title: '事件', dataIndex: 'eventCount' }, { title: '耗时', dataIndex: 'durationMs', render: formatDuration }, { title: '时间', dataIndex: 'startedAt', render: relativeDate }
  ]} />;
}

function Events({ data }: { data: EventRecord[] }) {
  return <Table rowKey="id" dataSource={data} pagination={{ pageSize: 10 }} scroll={{ x: 650 }} columns={[
    { title: 'ID', dataIndex: 'id', render: (id) => `#${id}` }, { title: '检查', dataIndex: 'checkRunId', render: (id) => id ? `#${id}` : '—' },
    { title: '类型', dataIndex: 'type', render: (value) => <Tag>{value}</Tag> }, { title: '事件数据', dataIndex: 'payload', ellipsis: true, render: jsonText }, { title: '时间', dataIndex: 'createdAt', render: relativeDate }
  ]} />;
}

function Rules({ data, channelByID = new Map<number, string>() }: { data: Rule[]; channelByID?: Map<number, string> }) {
  return <Table rowKey="id" dataSource={data} pagination={false} columns={[
    { title: '规则', dataIndex: 'name' }, { title: '状态', dataIndex: 'enabled', render: (value) => <StatusTag status={value ? 'ok' : 'disabled'} /> },
    { title: '通知渠道', dataIndex: 'notifyChannelIds', render: (ids: number[]) => ids.map((id) => <Tag key={id}>{channelByID.get(id) ?? `已归档渠道 #${id}`}</Tag>) }, { title: '上次触发', dataIndex: 'lastFiredAt', render: relativeDate }
  ]} />;
}

function Attempts({ data }: { data: NotificationAttempt[] }) {
  return <Table rowKey="id" dataSource={data} pagination={{ pageSize: 10 }} scroll={{ x: 700 }} columns={[
    { title: 'ID', dataIndex: 'id', render: (id) => `#${id}` }, { title: '渠道', dataIndex: 'channelName' }, { title: '状态', dataIndex: 'status', render: (value) => <StatusTag status={value} /> },
    { title: '次数', dataIndex: 'attemptNo' }, { title: '错误', dataIndex: 'error', ellipsis: true }, { title: '时间', dataIndex: 'createdAt', render: relativeDate }
  ]} />;
}
