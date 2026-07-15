import { Alert, Button, Card, Col, List, Progress, Row, Skeleton, Space, Statistic, Table, Typography } from 'antd';
import { CheckCircleOutlined, ExclamationCircleOutlined, NotificationOutlined, PlusOutlined, ThunderboltOutlined } from '@ant-design/icons';
import { useQuery } from '@tanstack/react-query';
import { api } from '../api';
import { formatDuration, PageError, relativeDate, StatusTag } from '../components/Common';
import type { CheckRun, Monitor, NotificationAttempt } from '../types';

const { Text, Title } = Typography;

export default function DashboardPage({ onNavigate }: { onNavigate: (page: string) => void }) {
  const summary = useQuery({ queryKey: ['dashboard'], queryFn: api.dashboard, refetchInterval: 20_000 });
  const monitors = useQuery({ queryKey: ['monitors'], queryFn: api.listMonitors, refetchInterval: 20_000 });
  const runs = useQuery({ queryKey: ['checkRuns'], queryFn: api.listCheckRuns, refetchInterval: 20_000 });
  const attempts = useQuery({ queryKey: ['notificationAttempts'], queryFn: api.listNotificationAttempts, refetchInterval: 20_000 });
  const system = useQuery({ queryKey: ['systemStatus'], queryFn: api.systemStatus, refetchInterval: 30_000 });

  if (summary.isLoading || monitors.isLoading) return <Skeleton active />;
  if (summary.isError) return <PageError error={summary.error as Error} onRetry={() => summary.refetch()} />;
  const data = summary.data;
  const loadingError = monitors.error || runs.error || attempts.error || system.error;
  const enabled = (monitors.data ?? []).filter((item) => item.enabled);
  const healthyRate = enabled.length === 0 ? 0 : Math.round(((data?.healthyMonitors ?? 0) / enabled.length) * 100);
  const unhealthy = enabled.filter((item) => ['error', 'warning'].includes(item.lastStatus ?? ''));
  const latestRuns = (runs.data ?? []).slice(0, 6);
  const failedAttempts = (attempts.data ?? []).filter((item) => item.status === 'failed' && !item.resolved).slice(0, 4);
  const setupIncomplete = !data?.channelCount || !data?.monitorCount || !data?.ruleCount;

  return (
    <Space direction="vertical" size={20} className="full-width">
      <PageError error={loadingError as Error | null} onRetry={() => { monitors.refetch(); runs.refetch(); attempts.refetch(); system.refetch(); }} />
      {system.data && system.data.database !== 'ok' && <Alert type="error" showIcon message="数据库不可用" description="请打开活动与诊断页面查看系统状态。" />}
      {setupIncomplete && (
        <Card className="onboarding-card" bordered={false}>
          <div className="onboarding-copy">
            <Text className="eyebrow">首次设置</Text>
            <Title level={3}>完成一条真正可用的通知链路</Title>
            <Text type="secondary">按顺序创建通知渠道、监控和规则。每一步都可以立即测试。</Text>
          </div>
          <div className="setup-steps">
            <SetupStep done={(data?.channelCount ?? 0) > 0} index={1} title="通知渠道" action={() => onNavigate('channels')} />
            <SetupStep done={(data?.monitorCount ?? 0) > 0} index={2} title="监控数据源" action={() => onNavigate('monitors')} />
            <SetupStep done={(data?.ruleCount ?? 0) > 0} index={3} title="触发规则" action={() => onNavigate('rules')} />
          </div>
        </Card>
      )}

      <Row gutter={[16, 16]}>
        <Col xs={12} lg={6}>
          <MetricCard icon={<CheckCircleOutlined />} tone="success" title="运行健康度" value={healthyRate} suffix="%" />
        </Col>
        <Col xs={12} lg={6}>
          <MetricCard icon={<ExclamationCircleOutlined />} tone={data?.failingMonitors ? 'danger' : 'neutral'} title="异常监控" value={data?.failingMonitors ?? 0} />
        </Col>
        <Col xs={12} lg={6}>
          <MetricCard icon={<ThunderboltOutlined />} tone="primary" title="24 小时事件" value={data?.eventsLast24Hours ?? 0} />
        </Col>
        <Col xs={12} lg={6}>
          <MetricCard icon={<NotificationOutlined />} tone={data?.failedAttempts ? 'danger' : 'neutral'} title="24 小时发送失败" value={data?.failedAttempts ?? 0} />
        </Col>
      </Row>

      <Row gutter={[20, 20]}>
        <Col xs={24} xl={15}>
          <Card title="最近检查" extra={<Button type="link" onClick={() => onNavigate('activity')}>查看全部</Button>}>
            <Table<CheckRun>
              rowKey="id"
              size="small"
              pagination={false}
              dataSource={latestRuns}
              scroll={{ x: 680 }}
              locale={{ emptyText: '还没有检查记录。创建监控后可立即运行一次检查。' }}
              columns={[
                { title: '监控', dataIndex: 'monitorName' },
                { title: '触发', dataIndex: 'trigger', width: 90, render: (value) => <StatusTag status={value} /> },
                { title: '结果', dataIndex: 'status', width: 90, render: (value) => <StatusTag status={value} /> },
                { title: '耗时', dataIndex: 'durationMs', width: 90, render: formatDuration },
                { title: '时间', dataIndex: 'startedAt', width: 130, render: relativeDate }
              ]}
            />
          </Card>
        </Col>
        <Col xs={24} xl={9}>
          <Card title="需要处理">
            {unhealthy.length === 0 && failedAttempts.length === 0 ? (
              <div className="healthy-state"><Progress type="circle" percent={healthyRate} size={86} /><div><Text strong>{enabled.length ? '当前没有待处理故障' : '还没有启用监控'}</Text><br /><Text type="secondary">{enabled.length ? '系统会持续检查监控和通知状态。' : '启用监控后，这里会显示实际健康度。'}</Text></div></div>
            ) : (
              <List
                dataSource={[...unhealthy, ...failedAttempts]}
                renderItem={(item) => 'lastStatus' in item ? <MonitorProblem item={item as Monitor} onOpen={() => onNavigate('monitors')} /> : (
                  <List.Item actions={[<Button type="link" onClick={() => onNavigate('activity')}>查看</Button>]}>
                    <List.Item.Meta title={`通知发送失败 · ${(item as NotificationAttempt).channelName}`} description={compactError((item as NotificationAttempt).error)} />
                  </List.Item>
                )}
              />
            )}
          </Card>
        </Col>
      </Row>
    </Space>
  );
}

function SetupStep({ done, index, title, action }: { done: boolean; index: number; title: string; action: () => void }) {
  return (
    <button className={`setup-step ${done ? 'done' : ''}`} onClick={action}>
      <span>{done ? <CheckCircleOutlined /> : index}</span>
      <strong>{title}</strong>
      {!done && <PlusOutlined />}
    </button>
  );
}

function MetricCard({ icon, tone, title, value, suffix }: { icon: React.ReactNode; tone: string; title: string; value: number; suffix?: string }) {
  return <Card className={`metric-card tone-${tone}`} bordered={false}><div className="metric-icon">{icon}</div><Statistic title={title} value={value} suffix={suffix} /></Card>;
}

function MonitorProblem({ item, onOpen }: { item: Monitor; onOpen: () => void }) {
  return (
    <List.Item actions={[<Button type="link" onClick={onOpen}>处理</Button>]}>
      <List.Item.Meta title={<Space>{item.name}<StatusTag status={item.lastStatus} /></Space>} description={compactError(item.lastError || item.lastMessage || '监控状态异常')} />
    </List.Item>
  );
}

function compactError(value?: string) {
  const text = (value || '未知错误').replace(/\s+/g, ' ').trim();
  return text.length > 180 ? `${text.slice(0, 177)}…` : text;
}
