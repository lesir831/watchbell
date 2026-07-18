import { Alert, Button, Card, List, Progress, Skeleton, Space, Statistic, Table, Typography } from 'antd';
import { ArrowRightOutlined, CheckCircleOutlined, PlusOutlined } from '@ant-design/icons';
import { useQuery } from '@tanstack/react-query';
import { api } from '../api';
import { formatDuration, PageError, relativeDate, StatusTag } from '../components/Common';
import type { CheckRun, Monitor, NotificationAttempt } from '../types';

const { Text, Title } = Typography;

type DashboardState = 'system-error' | 'checking' | 'attention' | 'pending' | 'empty' | 'quiet';

export default function DashboardPage({ onNavigate, onCreateMonitor }: { onNavigate: (page: string) => void; onCreateMonitor: () => void }) {
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
  const failedAttempts = (attempts.data ?? []).filter((item) => item.status === 'failed' && !item.resolved);
  const setupIncomplete = !data?.channelCount || !data?.monitorCount || !data?.ruleCount;
  const attentionCount = unhealthy.length + failedAttempts.length;
  const pendingCount = data?.pendingMonitors ?? 0;
  const databaseUnavailable = system.data !== undefined && system.data.database !== 'ok';
  const state: DashboardState = system.isError || databaseUnavailable
    ? 'system-error'
    : system.isLoading
      ? 'checking'
      : attentionCount > 0
        ? 'attention'
        : pendingCount > 0
          ? 'pending'
          : enabled.length === 0
            ? 'empty'
            : 'quiet';
  const stateCopy = dashboardStateCopy(state, attentionCount, pendingCount);

  return (
    <div className="dashboard-stack" data-od-id="dashboard-page">
      <PageError error={loadingError as Error | null} onRetry={() => { monitors.refetch(); runs.refetch(); attempts.refetch(); system.refetch(); }} />
      {system.data && system.data.database !== 'ok' && <Alert type="error" showIcon message="数据库不可用" description="请打开活动与诊断页面查看系统状态。" />}

      <header className="dashboard-intro" data-od-id="dashboard-heading">
        <div>
          <Text className="eyebrow">运行态势</Text>
          <Title level={2}>{stateCopy.headline}</Title>
          <Text type="secondary">汇总监控、规则和通知链路的实时状态。</Text>
        </div>
        <Button type="primary" icon={<PlusOutlined />} onClick={onCreateMonitor}>新建监控</Button>
      </header>

      {setupIncomplete && (
        <Card className="onboarding-card" variant="borderless" data-od-id="setup-guide">
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

      <section className="dashboard-focus-grid" aria-label="实时状态">
        <Card className={`pulse-card state-${state}`} variant="borderless" data-od-id="live-signal-card">
          <div className="pulse-card-head">
            <div><Text>实时信号</Text><span className={`live-indicator is-${state}`}>{stateCopy.indicator}</span></div>
            <Text>{system.data?.scheduler.lastTickAt ? `调度 ${relativeDate(system.data.scheduler.lastTickAt)}` : '等待调度状态'}</Text>
          </div>
          <div className="pulse-card-main">
            <div className="health-figure">
              <strong>{healthyRate}<small>%</small></strong>
              <span>启用监控健康度</span>
            </div>
            <div className="pulse-wave" aria-hidden="true">
              <svg viewBox="0 0 560 150" role="presentation">
                <path className="pulse-baseline" d="M0 76H560" />
                <path className="pulse-path" d="M0 76H86L112 76L136 38L160 118L185 76H276L300 76L324 57L348 96L374 76H560" />
                <circle className="pulse-node" cx="324" cy="57" r="6" />
              </svg>
            </div>
          </div>
          <div className="pulse-card-foot">
            <span><strong>{enabled.length}</strong> 个启用监控</span>
            <span><strong>{system.data?.scheduler.inFlight ?? '—'}</strong> 个执行中任务</span>
            <span><strong>{system.data?.outbox.pending ?? '—'}</strong> 条待发通知</span>
          </div>
        </Card>

        <Card className="attention-card" variant="borderless" data-od-id="attention-queue">
          <div className="panel-heading">
            <div><Text className="eyebrow">处理队列</Text><Title level={4}>{attentionCount > 0 ? `${attentionCount} 项待处理` : stateCopy.queueTitle}</Title></div>
            <Button type="text" icon={<ArrowRightOutlined />} aria-label="打开活动与诊断" onClick={() => onNavigate('activity')} />
          </div>
          {attentionCount === 0 ? (
            <div className="healthy-state">
              <Progress type="circle" percent={healthyRate} size={78} strokeWidth={8} status={state === 'system-error' ? 'exception' : undefined} />
              <div><Text strong>{stateCopy.queueLead}</Text><br /><Text type="secondary">{stateCopy.queueDescription}</Text></div>
            </div>
          ) : (
            <List
              className="attention-list"
              dataSource={[...unhealthy, ...failedAttempts].slice(0, 4)}
              renderItem={(item) => 'lastStatus' in item ? <MonitorProblem item={item as Monitor} onOpen={() => onNavigate('monitors')} /> : (
                <List.Item actions={[<Button type="link" onClick={() => onNavigate('activity')}>查看</Button>]}>
                  <List.Item.Meta title={`通知发送失败 · ${(item as NotificationAttempt).channelName}`} description={compactError((item as NotificationAttempt).error)} />
                </List.Item>
              )}
            />
          )}
        </Card>
      </section>

      <section className="metric-strip" aria-label="关键指标">
        <MetricCard tone="success" title="运行健康度" value={healthyRate} suffix="%" />
        <MetricCard tone={data?.failingMonitors ? 'danger' : 'neutral'} title="异常监控" value={data?.failingMonitors ?? 0} />
        <MetricCard tone="primary" title="24 小时事件" value={data?.eventsLast24Hours ?? 0} />
        <MetricCard tone={data?.failedAttempts ? 'danger' : 'neutral'} title="24 小时发送失败" value={data?.failedAttempts ?? 0} />
      </section>

      <section className="dashboard-lower-grid">
          <Card className="recent-checks-card" title="最近检查" extra={<Button type="link" onClick={() => onNavigate('activity')}>查看全部</Button>}>
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
          <Card className="system-note-card" variant="borderless">
            <Text className="eyebrow">系统侧写</Text>
            <Title level={4}>安静不是空白，而是链路正常。</Title>
            <Text type="secondary">WatchBell 只在状态变化满足规则时触发通知。你可以在“活动与诊断”中追溯每一次检查和发送。</Text>
            <Button onClick={() => onNavigate('activity')}>查看诊断记录</Button>
          </Card>
      </section>
    </div>
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

function MetricCard({ tone, title, value, suffix }: { tone: string; title: string; value: number; suffix?: string }) {
  return <Card className={`metric-card tone-${tone}`} variant="borderless"><span className="metric-marker" /><Statistic title={title} value={value} suffix={suffix} /></Card>;
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

function dashboardStateCopy(state: DashboardState, attentionCount: number, pendingCount: number) {
  switch (state) {
    case 'system-error':
      return {
        headline: '系统状态异常',
        indicator: '链路异常',
        queueTitle: '系统状态异常',
        queueLead: '无法确认完整运行状态',
        queueDescription: '请查看上方错误提示，并前往“活动与诊断”检查数据库和调度器状态。'
      };
    case 'checking':
      return {
        headline: '正在确认系统状态',
        indicator: '状态确认中',
        queueTitle: '正在确认运行状态',
        queueLead: '正在读取调度与通知链路',
        queueDescription: '状态加载完成后，这里会显示需要处理的问题。'
      };
    case 'attention':
      return {
        headline: `此刻有 ${attentionCount} 项需要处理`,
        indicator: '需要关注',
        queueTitle: `${attentionCount} 项待处理`,
        queueLead: '',
        queueDescription: ''
      };
    case 'pending':
      return {
        headline: `${pendingCount} 个监控等待首次检查`,
        indicator: '等待首次检查',
        queueTitle: '监控尚未完成首次检查',
        queueLead: `${pendingCount} 个监控等待首次检查`,
        queueDescription: '首次检查完成前，系统还不能确认这些监控是否健康。'
      };
    case 'empty':
      return {
        headline: '还没有启用监控',
        indicator: '等待配置',
        queueTitle: '还没有启用监控',
        queueLead: '创建并启用第一个监控',
        queueDescription: '启用监控并完成首次检查后，这里会显示实际健康状态。'
      };
    case 'quiet':
      return {
        headline: '此刻一切安静',
        indicator: '链路稳定',
        queueTitle: '没有待处理故障',
        queueLead: '监控与通知保持正常',
        queueDescription: '系统会继续保持安静，直到出现重要变化。'
      };
  }
}
