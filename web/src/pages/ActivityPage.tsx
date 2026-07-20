import { useEffect, useMemo, useState } from 'react';
import { Alert, App as AntApp, Button, Card, Drawer, Form, Input, InputNumber, Popconfirm, Select, Space, Statistic, Table, Tabs, Tag, Typography, Upload } from 'antd';
import { CopyOutlined, DownloadOutlined, ReloadOutlined, SearchOutlined, UploadOutlined } from '@ant-design/icons';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from '../api';
import { eventTitle, formatDate, formatDuration, formatTime, jsonText, PageError, PageHeader, relativeDate, RenderedData, StatusTag } from '../components/Common';
import type { AuditLog, CheckRun, ConfigBackup, DeadLetter, EventRecord, HistoryPage, HistoryQuery, NotificationAttempt, RuleEvaluation } from '../types';

const { Text } = Typography;

type DetailItem = { title: string; data: unknown } | null;
type HistoryTab = 'runs' | 'events' | 'evaluations' | 'attempts' | 'audit';
type PageState = { page: number; pageSize: number };
type FilterValues = Omit<HistoryQuery, 'page' | 'pageSize'>;
type PageMeta = Pick<HistoryPage<unknown>, 'page' | 'pageSize' | 'total'>;
type ActivityKind = 'all' | 'check' | 'event' | 'notification';

const historyTabs: HistoryTab[] = ['runs', 'events', 'evaluations', 'attempts', 'audit'];

function initialRecord<T>(factory: () => T): Record<HistoryTab, T> {
  return Object.fromEntries(historyTabs.map((key) => [key, factory()])) as Record<HistoryTab, T>;
}

export default function ActivityPage({ initialTab, initialEventId }: { initialTab?: 'events' | 'evaluations' | 'attempts'; initialEventId?: number }) {
  const { message } = AntApp.useApp();
  const queryClient = useQueryClient();
  const [detail, setDetail] = useState<DetailItem>(null);
  const [activeTab, setActiveTab] = useState(initialTab ?? 'runs');
  const [activityKind, setActivityKind] = useState<ActivityKind>('all');
  const [activitySearch, setActivitySearch] = useState('');
  const [filterForm] = Form.useForm<FilterValues>();
  const [paging, setPaging] = useState(() => initialRecord<PageState>(() => ({ page: 1, pageSize: 15 })));
  const [filters, setFilters] = useState(() => {
    const values = initialRecord<FilterValues>(() => ({}));
    if (initialEventId) values.events = { eventId: initialEventId };
    return values;
  });
  const [pendingEventId, setPendingEventId] = useState(initialEventId);
  const monitors = useQuery({ queryKey: ['monitors'], queryFn: api.listMonitors, refetchInterval: 20_000 });
  const runsQuery = { ...paging.runs, ...filters.runs };
  const eventsQuery = { ...paging.events, ...filters.events };
  const evaluationsQuery = { ...paging.evaluations, ...filters.evaluations };
  const attemptsQuery = { ...paging.attempts, ...filters.attempts };
  const auditQuery = { ...paging.audit, ...filters.audit };
  const runs = useQuery({ queryKey: ['checkRuns', 'page', runsQuery], queryFn: () => api.listCheckRunsPage(runsQuery), refetchInterval: 15_000, placeholderData: (previous) => previous });
  const events = useQuery({ queryKey: ['events', 'page', eventsQuery], queryFn: () => api.listEventsPage(eventsQuery), refetchInterval: 15_000, placeholderData: (previous) => previous });
  const evaluations = useQuery({ queryKey: ['ruleEvaluations', 'page', evaluationsQuery], queryFn: () => api.listRuleEvaluationsPage(evaluationsQuery), enabled: activeTab === 'evaluations', refetchInterval: 15_000, placeholderData: (previous) => previous });
  const attempts = useQuery({ queryKey: ['notificationAttempts', 'page', attemptsQuery], queryFn: () => api.listNotificationAttemptsPage(attemptsQuery), refetchInterval: 15_000, placeholderData: (previous) => previous });
  const audits = useQuery({ queryKey: ['auditLogs', 'page', auditQuery], queryFn: () => api.listAuditLogsPage(auditQuery), enabled: activeTab === 'audit', refetchInterval: 20_000, placeholderData: (previous) => previous });
  const system = useQuery({ queryKey: ['systemStatus'], queryFn: api.systemStatus, refetchInterval: 30_000 });
  const monitorByID = useMemo(() => new Map((monitors.data ?? []).map((item) => [item.id, item.name])), [monitors.data]);
  const retryMutation = useMutation({
    mutationFn: api.retryNotificationAttempt,
    onSuccess: async (item) => { await queryClient.invalidateQueries({ queryKey: ['notificationAttempts'] }); message.success(item.status === 'sent' ? '重试发送成功' : '已记录新的失败尝试'); },
    onError: async (error: Error) => { await queryClient.invalidateQueries({ queryKey: ['notificationAttempts'] }); message.error(error.message); }
  });
  const diagnostics = useMutation({
    mutationFn: api.diagnostics,
    onSuccess: (data) => downloadJSON(data, `watchbell-diagnostics-${new Date().toISOString().slice(0, 10)}.json`),
    onError: (error: Error) => message.error(error.message)
  });

  useEffect(() => {
    if (!initialTab && !initialEventId) return;
    if (initialTab) setActiveTab(initialTab);
    if (initialEventId) {
      setActiveTab('events');
      setFilters((current) => ({ ...current, events: { eventId: initialEventId } }));
      setPaging((current) => ({ ...current, events: { ...current.events, page: 1 } }));
      filterForm.setFieldsValue({ eventId: initialEventId });
      setPendingEventId(initialEventId);
    }
  }, [filterForm, initialEventId, initialTab]);

  useEffect(() => {
    if (!pendingEventId) return;
    const event = events.data?.items.find((item) => item.id === pendingEventId);
    if (!event) return;
    setDetail({ title: `事件 #${event.id} · ${eventTitle(event.payload, monitorByID.get(event.monitorId))}`, data: event });
    setPendingEventId(undefined);
  }, [events.data, monitorByID, pendingEventId]);

  const changePage = (key: HistoryTab, page: number, pageSize: number) => setPaging((current) => ({ ...current, [key]: { page, pageSize } }));
  const selectTab = (key: string) => {
    setActiveTab(key);
    filterForm.resetFields();
    if (historyTabs.includes(key as HistoryTab)) filterForm.setFieldsValue(filtersToForm(filters[key as HistoryTab]));
  };
  const applyFilters = async () => {
    if (!historyTabs.includes(activeTab as HistoryTab)) return;
    const key = activeTab as HistoryTab;
    const values = await filterForm.validateFields();
    setFilters((current) => ({ ...current, [key]: normalizeFilters(values) }));
    setPaging((current) => ({ ...current, [key]: { ...current[key], page: 1 } }));
  };
  const clearFilters = () => {
    if (!historyTabs.includes(activeTab as HistoryTab)) return;
    const key = activeTab as HistoryTab;
    filterForm.resetFields();
    setFilters((current) => ({ ...current, [key]: {} }));
    setPaging((current) => ({ ...current, [key]: { ...current[key], page: 1 } }));
  };
  const openRelatedEvent = (eventId: number) => {
    setActiveTab('events');
    setFilters((current) => ({ ...current, events: { eventId } }));
    setPaging((current) => ({ ...current, events: { ...current.events, page: 1 } }));
    filterForm.setFieldsValue({ eventId });
    setPendingEventId(eventId);
  };
  const loadingError = activeTab === 'runs' ? runs.error : activeTab === 'events' ? events.error : activeTab === 'evaluations' ? evaluations.error : activeTab === 'attempts' ? attempts.error : activeTab === 'audit' ? audits.error : system.error;
  const timelineItems = useMemo(() => {
    const items = [
      ...(runs.data?.items ?? []).map((item) => ({ id: `run-${item.id}`, kind: 'check' as const, time: item.finishedAt || item.startedAt, tone: item.status === 'error' ? 'danger' : item.status === 'warning' ? 'warning' : 'success', title: `${item.status === 'error' ? '检查失败' : item.status === 'warning' ? '检查警告' : '检查完成'} · ${item.monitorName}`, description: item.error || item.message || `未发现新事件，耗时 ${formatDuration(item.durationMs)}。`, search: item.monitorName })),
      ...(events.data?.items ?? []).map((item) => ({ id: `event-${item.id}`, kind: 'event' as const, time: item.createdAt, tone: 'success', title: `新事件 · ${monitorByID.get(item.monitorId) ?? `监控 #${item.monitorId}`}`, description: `${item.type} · ${eventSummary(item.payload)}`, search: monitorByID.get(item.monitorId) ?? '' })),
      ...(attempts.data?.items ?? []).map((item) => ({ id: `attempt-${item.id}`, kind: 'notification' as const, time: item.sentAt || item.createdAt, tone: item.status === 'sent' ? 'success' : 'danger', title: `${item.status === 'sent' ? '通知已送达' : '通知发送失败'} · ${item.channelName}`, description: item.error || item.subject || `${item.kind} 通知`, search: `${item.channelName} ${item.monitorId ? monitorByID.get(item.monitorId) ?? '' : ''}` }))
    ];
    const normalized = activitySearch.trim().toLowerCase();
    return items
      .filter((item) => (activityKind === 'all' || item.kind === activityKind) && `${item.title} ${item.description} ${item.search}`.toLowerCase().includes(normalized))
      .sort((a, b) => new Date(b.time).getTime() - new Date(a.time).getTime())
      .slice(0, 20);
  }, [activityKind, activitySearch, attempts.data, events.data, monitorByID, runs.data]);
  return (
    <div className="design-page">
      <PageHeader
        eyebrow="完整活动链路"
        title="活动与诊断"
        description="从信号到通知，每一步都可追溯；异常记录优先提供可行动的上下文。"
        actions={<Button icon={<DownloadOutlined />} loading={diagnostics.isPending} onClick={() => diagnostics.mutate()}>导出诊断</Button>}
      />
      <PageError error={loadingError as Error | null} onRetry={() => { runs.refetch(); events.refetch(); evaluations.refetch(); attempts.refetch(); audits.refetch(); }} />
      <div className="design-toolbar">
        <div className="filter-group" role="group" aria-label="活动筛选">
          {[['all', '全部'], ['check', '检查'], ['event', '事件'], ['notification', '通知']].map(([value, label]) => <button key={value} type="button" className={`filter-button ${activityKind === value ? 'active' : ''}`} onClick={() => setActivityKind(value as ActivityKind)}>{label}</button>)}
        </div>
        <label className="search-box"><SearchOutlined /><span className="sr-only">筛选监控名称</span><input type="search" placeholder="筛选监控名称" value={activitySearch} onChange={(event) => setActivitySearch(event.target.value)} /></label>
      </div>
      <div className="activity-layout">
        <section className="activity-panel">
          <div className="panel-head"><div><h2>实时活动流</h2><span>最新事件显示在顶部</span></div><span className="live-chip"><i />持续更新</span></div>
          <div className="timeline">
            {timelineItems.length ? timelineItems.map((item) => <div key={item.id} className="timeline-item">
              <time>{activityTime(item.time)}</time><span className={`timeline-rail tone-${item.tone}`}><i /></span><div className="timeline-card"><strong>{item.title}</strong><p>{item.description}</p></div>
            </div>) : <div className="timeline-empty">没有符合当前筛选条件的活动。</div>}
          </div>
        </section>
        <aside className="health-panel">
          <div className="panel-head"><div><h2>系统健康</h2><span>最近一次采样</span></div></div>
          <div className="health-list">
            <div><span>数据库</span><strong><i className={system.data?.database === 'ok' ? 'ok' : 'danger'} />{system.data?.database === 'ok' ? '正常' : '异常'}</strong></div>
            <div><span>调度器</span><strong><i className={system.data?.scheduler.lastTickAt ? 'ok' : 'warning'} />{system.data?.scheduler.lastTickAt ? '运行中' : '等待心跳'}</strong></div>
            <div><span>检查队列</span><strong className="number">{system.data?.scheduler.inFlight ?? 0} 执行中</strong></div>
            <div><span>通知队列</span><strong className="number">{system.data?.outbox.pending ?? 0} 等待</strong></div>
            <div><span>工作线程</span><strong className="number">{system.data?.scheduler.workerCount ?? 0}</strong></div>
          </div>
          <div className="health-note">真实数值来自系统状态接口，并每 30 秒自动更新。</div>
        </aside>
      </div>
      <section className="records-panel">
        <div className="records-heading"><div><h2>完整记录</h2><span>筛选、分页并检查每一步的原始数据</span></div></div>
        {historyTabs.includes(activeTab as HistoryTab) && <HistoryFilters tab={activeTab as HistoryTab} form={filterForm} monitors={monitors.data ?? []} onApply={applyFilters} onClear={clearFilters} />}
        <Card className="activity-shell" variant="borderless">
        <Tabs
          activeKey={activeTab}
          onChange={selectTab}
          items={[
            { key: 'runs', label: `检查运行 ${runs.data?.total ?? ''}`, children: <RunsTable data={runs.data?.items ?? []} page={runs.data} loading={runs.isFetching} onPage={(page, size) => changePage('runs', page, size)} onDetail={(item) => setDetail({ title: `检查运行 #${item.id}`, data: item })} /> },
            { key: 'events', label: `事件 ${events.data?.total ?? ''}`, children: <EventsTable data={events.data?.items ?? []} page={events.data} loading={events.isFetching} monitorByID={monitorByID} onPage={(page, size) => changePage('events', page, size)} onDetail={(item) => setDetail({ title: `事件 #${item.id}`, data: item })} /> },
            { key: 'evaluations', label: `规则判断 ${evaluations.data?.total ?? ''}`, children: <EvaluationsTable data={evaluations.data?.items ?? []} page={evaluations.data} loading={evaluations.isFetching} onPage={(page, size) => changePage('evaluations', page, size)} onDetail={(item) => setDetail({ title: `规则判断 #${item.id}`, data: item })} /> },
            { key: 'attempts', label: `通知尝试 ${attempts.data?.total ?? ''}`, children: <AttemptsTable data={attempts.data?.items ?? []} page={attempts.data} loading={attempts.isFetching} retrying={retryMutation.isPending ? retryMutation.variables : undefined} monitorByID={monitorByID} onPage={(page, size) => changePage('attempts', page, size)} onRetry={(id) => retryMutation.mutate(id)} onDetail={(item) => setDetail({ title: `通知尝试 #${item.id}`, data: item })} onOpenEvent={openRelatedEvent} /> },
            { key: 'audit', label: `操作审计 ${audits.data?.total ?? ''}`, children: <AuditTable data={audits.data?.items ?? []} page={audits.data} loading={audits.isFetching} onPage={(page, size) => changePage('audit', page, size)} onDetail={(item) => setDetail({ title: `操作记录 #${item.id}`, data: item })} /> },
            { key: 'system', label: '系统诊断', children: <SystemPanel data={system.data} loading={system.isLoading} error={system.error as Error | null} onRefresh={() => system.refetch()} /> }
          ]}
        />
        </Card>
      </section>
      <Drawer title={detail?.title} open={detail !== null} onClose={() => setDetail(null)} width={680}>
        <Button icon={<CopyOutlined />} onClick={() => { navigator.clipboard.writeText(jsonText(detail?.data)); message.success('详情已复制'); }}>复制 JSON</Button>
        <div className="activity-rendered-detail"><RenderedData value={detail?.data} /></div>
      </Drawer>
    </div>
  );
}

function activityTime(value: string) {
  return formatTime(value);
}

function HistoryFilters({ tab, form, monitors, onApply, onClear }: { tab: HistoryTab; form: ReturnType<typeof Form.useForm<FilterValues>>[0]; monitors: Array<{ id: number; name: string }>; onApply: () => void; onClear: () => void }) {
  const monitorOptions = monitors.map((item) => ({ value: item.id, label: item.name }));
  const statusOptions = tab === 'runs'
    ? ['ok', 'error', 'running'].map(option)
    : tab === 'evaluations'
      ? ['matched', 'not_matched', 'skipped', 'error'].map(option)
      : ['sent', 'failed'].map(option);
  return <Card size="small" className="history-filters">
    <Form form={form} layout="inline" onFinish={onApply}>
      {(tab === 'runs' || tab === 'events') && <Form.Item name="monitorId" label="监控"><Select allowClear showSearch optionFilterProp="label" options={monitorOptions} placeholder="全部监控" style={{ width: 180 }} /></Form.Item>}
      {tab === 'events' && <><Form.Item name="eventId" label="事件 ID"><InputNumber min={1} /></Form.Item><Form.Item name="checkRunId" label="检查 ID"><InputNumber min={1} /></Form.Item><Form.Item name="type" label="事件类型"><Input allowClear placeholder="rss.item" /></Form.Item></>}
      {tab === 'runs' && <><Form.Item name="status" label="状态"><Select allowClear options={statusOptions} style={{ width: 130 }} /></Form.Item><Form.Item name="trigger" label="触发"><Select allowClear options={[{ value: 'manual', label: '手动' }, { value: 'scheduled', label: '定时' }]} style={{ width: 120 }} /></Form.Item></>}
      {tab === 'evaluations' && <><Form.Item name="eventId" label="事件 ID"><InputNumber min={1} /></Form.Item><Form.Item name="ruleId" label="规则 ID"><InputNumber min={1} /></Form.Item><Form.Item name="status" label="状态"><Select allowClear options={statusOptions} style={{ width: 145 }} /></Form.Item></>}
      {tab === 'attempts' && <><Form.Item name="monitorId" label="监控"><Select allowClear showSearch optionFilterProp="label" options={monitorOptions} placeholder="全部监控" style={{ width: 180 }} /></Form.Item><Form.Item name="eventId" label="事件 ID"><InputNumber min={1} /></Form.Item><Form.Item name="channelId" label="渠道 ID"><InputNumber min={1} /></Form.Item><Form.Item name="status" label="状态"><Select allowClear options={statusOptions} style={{ width: 120 }} /></Form.Item><Form.Item name="kind" label="类型"><Select allowClear options={[{ value: 'delivery', label: '事件通知' }, { value: 'test', label: '渠道测试' }, { value: 'monitor_failure', label: '监控故障' }, { value: 'monitor_recovery', label: '监控恢复' }]} style={{ width: 130 }} /></Form.Item></>}
      {tab === 'audit' && <><Form.Item name="entityId" label="对象 ID"><InputNumber min={1} /></Form.Item><Form.Item name="action" label="动作"><Select allowClear options={['create', 'update', 'delete', 'check', 'test', 'retry', 'export', 'import'].map(option)} style={{ width: 120 }} /></Form.Item><Form.Item name="entityType" label="对象类型"><Input allowClear placeholder="monitor" /></Form.Item></>}
      <Form.Item name="from" label="开始"><Input type="datetime-local" /></Form.Item>
      <Form.Item name="to" label="结束"><Input type="datetime-local" /></Form.Item>
      <Form.Item><Space><Button htmlType="submit" type="primary" icon={<SearchOutlined />}>筛选</Button><Button onClick={onClear}>清除</Button></Space></Form.Item>
    </Form>
  </Card>;
}

function option(value: string) {
  return { value, label: value };
}

function normalizeFilters(values: FilterValues): FilterValues {
  const result = { ...values };
  for (const key of ['from', 'to'] as const) {
    const value = result[key];
    if (value) result[key] = new Date(value).toISOString();
  }
  Object.entries(result).forEach(([key, value]) => {
    if (typeof value === 'string' && value.trim() === '') delete (result as Record<string, unknown>)[key];
  });
  return result;
}

function filtersToForm(values: FilterValues): FilterValues {
  const result = { ...values };
  for (const key of ['from', 'to'] as const) {
    const value = result[key];
    if (!value) continue;
    const date = new Date(value);
    result[key] = new Date(date.getTime() - date.getTimezoneOffset() * 60_000).toISOString().slice(0, 16);
  }
  return result;
}

function pagination(page: PageMeta | undefined, onPage: (page: number, pageSize: number) => void) {
  return { current: page?.page ?? 1, pageSize: page?.pageSize ?? 15, total: page?.total ?? 0, showSizeChanger: true, pageSizeOptions: [15, 30, 50, 100], showTotal: (total: number) => `共 ${total} 条`, onChange: onPage };
}

function RunsTable({ data, page, loading, onPage, onDetail }: { data: CheckRun[]; page?: PageMeta; loading: boolean; onPage: (page: number, pageSize: number) => void; onDetail: (item: CheckRun) => void }) {
  return <Table<CheckRun> rowKey="id" loading={loading} dataSource={data} scroll={{ x: 980 }} pagination={pagination(page, onPage)} columns={[
    { title: 'ID', dataIndex: 'id', width: 75, render: (id, item) => <Button type="link" onClick={() => onDetail(item)}>#{id}</Button> },
    { title: '监控', dataIndex: 'monitorName', width: 180 }, { title: '触发', dataIndex: 'trigger', width: 90, render: (value) => <StatusTag status={value} /> },
    { title: '状态', dataIndex: 'status', width: 90, render: (value) => <StatusTag status={value} /> },
    { title: '消息 / 错误', width: 300, ellipsis: true, render: (_, item) => <Text type={item.error ? 'danger' : undefined}>{item.error || item.message || '—'}</Text> },
    { title: '事件', dataIndex: 'eventCount', width: 75 }, { title: '耗时', dataIndex: 'durationMs', width: 85, render: formatDuration },
    { title: '开始时间', dataIndex: 'startedAt', width: 150, render: (value) => <span title={formatDate(value)}>{relativeDate(value)}</span> }
  ]} />;
}

function EventsTable({ data, page, loading, monitorByID, onPage, onDetail }: { data: EventRecord[]; page?: PageMeta; loading: boolean; monitorByID: Map<number, string>; onPage: (page: number, pageSize: number) => void; onDetail: (item: EventRecord) => void }) {
  return <Table<EventRecord> rowKey="id" loading={loading} dataSource={data} scroll={{ x: 880 }} pagination={pagination(page, onPage)} columns={[
    { title: 'ID', dataIndex: 'id', width: 75, render: (id, item) => <Button type="link" onClick={() => onDetail(item)}>#{id}</Button> },
    { title: '检查', dataIndex: 'checkRunId', width: 85, render: (id) => id ? `#${id}` : '—' },
    { title: '监控', dataIndex: 'monitorId', width: 170, render: (id) => monitorByID.get(id) ?? `已归档监控 #${id}` },
    { title: '类型', dataIndex: 'type', width: 160, render: (value) => <Tag>{value}</Tag> },
    { title: '摘要', dataIndex: 'payload', ellipsis: true, render: (value) => eventSummary(value) },
    { title: '产生时间', dataIndex: 'createdAt', width: 150, render: (value) => <span title={formatDate(value)}>{relativeDate(value)}</span> }
  ]} />;
}

function EvaluationsTable({ data, page, loading, onPage, onDetail }: { data: RuleEvaluation[]; page?: PageMeta; loading: boolean; onPage: (page: number, pageSize: number) => void; onDetail: (item: RuleEvaluation) => void }) {
  return <Table<RuleEvaluation> rowKey="id" loading={loading} dataSource={data} scroll={{ x: 820 }} pagination={pagination(page, onPage)} columns={[
    { title: 'ID', dataIndex: 'id', width: 75, render: (id, item) => <Button type="link" onClick={() => onDetail(item)}>#{id}</Button> },
    { title: '事件', dataIndex: 'eventId', width: 85, render: (id) => `#${id}` }, { title: '规则', dataIndex: 'ruleName', width: 190 },
    { title: '结果', dataIndex: 'status', width: 100, render: (value) => <StatusTag status={value} /> },
    { title: '原因', dataIndex: 'reason', ellipsis: true }, { title: '时间', dataIndex: 'createdAt', width: 150, render: relativeDate }
  ]} />;
}

function AttemptsTable({ data, page, loading, retrying, monitorByID, onPage, onRetry, onDetail, onOpenEvent }: { data: NotificationAttempt[]; page?: PageMeta; loading: boolean; retrying?: number; monitorByID: Map<number, string>; onPage: (page: number, pageSize: number) => void; onRetry: (id: number) => void; onDetail: (item: NotificationAttempt) => void; onOpenEvent: (eventId: number) => void }) {
  return <Table<NotificationAttempt> rowKey="id" loading={loading} dataSource={data} scroll={{ x: 1220 }} pagination={pagination(page, onPage)} columns={[
    { title: 'ID', dataIndex: 'id', width: 75, render: (id, item) => <Button type="link" onClick={() => onDetail(item)}>#{id}</Button> },
    { title: '监控', dataIndex: 'monitorId', width: 160, render: (id) => id ? monitorByID.get(id) ?? `已归档监控 #${id}` : '—' },
    { title: '关联', width: 150, render: (_, item) => attemptKindLabel(item, onOpenEvent) },
    { title: '渠道', dataIndex: 'channelName', width: 160 }, { title: '状态', dataIndex: 'status', width: 90, render: (value) => <StatusTag status={value} /> },
    { title: '尝试', dataIndex: 'attemptNo', width: 75, render: (value) => `第 ${value} 次` },
    { title: '错误', dataIndex: 'error', ellipsis: true, render: (value) => <Text type={value ? 'danger' : undefined}>{value || '—'}</Text> },
    { title: '重试状态', width: 150, render: (_, item) => attemptRetryState(item) },
    { title: '耗时', dataIndex: 'durationMs', width: 85, render: formatDuration }, { title: '时间', dataIndex: 'createdAt', width: 140, render: relativeDate },
    { title: '操作', width: 100, fixed: 'right', render: (_, item) => item.status === 'failed' && item.retriable && !item.resolved ? (
      <Popconfirm title="手动重试这次通知？" description="将立即使用原通知内容和当前渠道配置再次发送。" okText="确认重试" cancelText="取消" onConfirm={() => onRetry(item.id)}>
        <Button size="small" icon={<ReloadOutlined />} loading={retrying === item.id}>重试</Button>
      </Popconfirm>
    ) : null }
  ]} />;
}

function attemptRetryState(item: NotificationAttempt) {
  if (item.resolved) return <Tag color="blue">已被后续尝试取代</Tag>;
  if (item.nextRetryAt) return <span title={formatDate(item.nextRetryAt)}>计划 {relativeDate(item.nextRetryAt)}</span>;
  if (item.status === 'failed' && item.retriable) return <Tag color="warning">可手动重试</Tag>;
  return '—';
}

function attemptKindLabel(item: NotificationAttempt, onOpenEvent: (eventId: number) => void) {
  switch (item.kind) {
    case 'test': return <Tag color="blue">渠道测试</Tag>;
    case 'monitor_failure': return <Tag color="error">监控故障</Tag>;
    case 'monitor_recovery': return <Tag color="success">监控恢复</Tag>;
    default: return item.eventId ? <Button type="link" className="table-link" onClick={() => onOpenEvent(item.eventId!)}>事件 #{item.eventId}</Button> : <Tag>事件通知</Tag>;
  }
}

function AuditTable({ data, page, loading, onPage, onDetail }: { data: AuditLog[]; page?: PageMeta; loading: boolean; onPage: (page: number, pageSize: number) => void; onDetail: (item: AuditLog) => void }) {
  return <Table<AuditLog> rowKey="id" loading={loading} dataSource={data} scroll={{ x: 760 }} pagination={pagination(page, onPage)} columns={[
    { title: 'ID', dataIndex: 'id', width: 75, render: (id, item) => <Button type="link" onClick={() => onDetail(item)}>#{id}</Button> },
    { title: '用户', dataIndex: 'actor', width: 120 }, { title: '动作', dataIndex: 'action', width: 100, render: (value) => <Tag>{auditActionLabel(value)}</Tag> },
    { title: '对象', width: 150, render: (_, item) => `${auditEntityLabel(item.entityType)}${item.entityId ? ` #${item.entityId}` : ''}` },
    { title: '摘要', dataIndex: 'summary' }, { title: '时间', dataIndex: 'createdAt', width: 150, render: relativeDate }
  ]} />;
}

function SystemPanel({ data, loading, error, onRefresh }: { data: Awaited<ReturnType<typeof api.systemStatus>> | undefined; loading: boolean; error: Error | null; onRefresh: () => void }) {
  const { message, modal } = AntApp.useApp();
  const queryClient = useQueryClient();
  const [deadLetterPaging, setDeadLetterPaging] = useState({ page: 1, pageSize: 10 });
  const deadLetters = useQuery({
    queryKey: ['deadLetters', deadLetterPaging],
    queryFn: () => api.listDeadLettersPage(deadLetterPaging),
    refetchInterval: 30_000,
    placeholderData: (previous) => previous
  });
  const retryDeadLetter = useMutation({
    mutationFn: api.retryDeadLetter,
    onSuccess: async () => {
      await Promise.all([queryClient.invalidateQueries({ queryKey: ['deadLetters'] }), queryClient.invalidateQueries({ queryKey: ['systemStatus'] })]);
      message.success('死信事件已重新入队，将在下一轮调度中处理');
    },
    onError: (retryError: Error) => message.error(retryError.message)
  });
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
  const configExport = useMutation({
    mutationFn: api.exportConfig,
    onSuccess: (value) => {
      downloadJSON(value, `watchbell-config-${value.includesSecrets ? 'full' : 'redacted'}-${new Date().toISOString()}.json`);
      message.success(value.includesSecrets ? '已导出包含密钥的完整配置，请安全保管' : '已导出脱敏配置');
    },
    onError: (error: Error) => message.error(error.message)
  });
  const configImport = useMutation({
    mutationFn: api.importConfig,
    onSuccess: async (report) => {
      await Promise.all(['monitors', 'rules', 'channels', 'templates', 'dashboard', 'auditLogs'].map((queryKey) => queryClient.invalidateQueries({ queryKey: [queryKey] })));
      const created = Object.values(report.created).reduce((sum, value) => sum + value, 0);
      const updated = Object.values(report.updated).reduce((sum, value) => sum + value, 0);
      modal.success({ title: '配置导入完成', content: <div><p>新建 {created} 项，更新 {updated} 项。现有配置与历史不会被删除。</p>{report.warnings.map((warning) => <Alert key={warning} type="warning" message={warning} showIcon />)}</div> });
    },
    onError: (error: Error) => message.error(error.message)
  });
  const selectBackup = async (file: File) => {
    try {
      if (file.size > 2 * 1024 * 1024) throw new Error('文件超过 2 MiB 导入上限');
      const backup = JSON.parse(await file.text()) as ConfigBackup;
      modal.confirm({
        title: '合并导入这份配置？',
        content: <Space direction="vertical"><Text>导入会按名称合并监控、规则、渠道和模板，不删除现有数据。</Text>{!backup.includesSecrets && <Alert type="warning" showIcon message="这是脱敏备份" description="只能保留目标实例中同名配置的现有密钥；全新恢复需要含密钥备份。" />}</Space>,
        okText: '确认导入', cancelText: '取消', okButtonProps: { danger: true },
        onOk: () => configImport.mutateAsync(backup)
      });
    } catch (error) {
      message.error(error instanceof Error ? `无法读取备份：${error.message}` : '无法读取备份');
    }
    return false;
  };
  if (loading) return <Card loading />;
  return (
    <Space direction="vertical" size={16} className="full-width">
      <PageError error={(error || deadLetters.error) as Error | null} onRetry={() => { onRefresh(); deadLetters.refetch(); }} />
      <Alert type={data?.database === 'ok' && data.scheduler.lastTickAt && !data.outbox?.dead_letter ? 'success' : 'error'} showIcon message={data?.outbox?.dead_letter ? `有 ${data.outbox.dead_letter} 个事件多次处理失败` : data?.database === 'ok' ? '数据库连接正常' : '数据库异常'} description={`调度器最近心跳：${relativeDate(data?.scheduler.lastTickAt)}；待处理事件：${data?.outbox?.pending ?? 0}`} />
      <div className="system-metrics">
        <Card><Statistic title="工作线程" value={data?.scheduler.workerCount ?? 0} /></Card>
        <Card><Statistic title="正在检查" value={data?.scheduler.inFlight ?? 0} /></Card>
        <Card><Statistic title="待处理事件" value={(data?.outbox?.pending ?? 0) + (data?.outbox?.processing ?? 0)} /></Card>
        <Card><Statistic title="死信事件" value={data?.outbox?.dead_letter ?? 0} valueStyle={data?.outbox?.dead_letter ? { color: '#cf1322' } : undefined} /></Card>
        <Card><Statistic title="调度器运行时间" value={relativeDate(data?.scheduler.startedAt)} /></Card>
      </div>
      {(deadLetters.data?.total ?? data?.outbox?.dead_letter ?? 0) > 0 && <Card title="死信事件" extra={<Text type="secondary">连续处理失败后停止自动重试，可检查错误并手动重新入队</Text>}>
        <Table<DeadLetter>
          rowKey="eventId"
          size="small"
          loading={deadLetters.isFetching}
          dataSource={deadLetters.data?.items ?? []}
          scroll={{ x: 820 }}
          pagination={{
            current: deadLetters.data?.page ?? deadLetterPaging.page,
            pageSize: deadLetters.data?.pageSize ?? deadLetterPaging.pageSize,
            total: deadLetters.data?.total ?? 0,
            showSizeChanger: true,
            onChange: (page, pageSize) => setDeadLetterPaging({ page, pageSize })
          }}
          columns={[
            { title: '事件', dataIndex: 'eventId', width: 90, render: (id) => `#${id}` },
            { title: '监控', dataIndex: 'monitorName', width: 170 },
            { title: '类型', dataIndex: 'eventType', width: 150, render: (value) => <Tag>{value}</Tag> },
            { title: '失败次数', dataIndex: 'attempts', width: 95 },
            { title: '最后错误', dataIndex: 'lastError', ellipsis: true, render: (value) => <Text type="danger" title={value}>{value}</Text> },
            { title: '更新时间', dataIndex: 'updatedAt', width: 140, render: relativeDate },
            { title: '操作', width: 110, fixed: 'right', render: (_, item) => <Popconfirm title="重新处理这个事件？" description="规则会按当前配置重新判断，可能产生通知。" onConfirm={() => retryDeadLetter.mutate(item.eventId)}><Button size="small" icon={<ReloadOutlined />} loading={retryDeadLetter.isPending && retryDeadLetter.variables === item.eventId}>重新入队</Button></Popconfirm> }
          ]}
        />
      </Card>}
      <Space wrap>
        <Button icon={<ReloadOutlined />} onClick={onRefresh}>刷新状态</Button>
        <Button icon={<DownloadOutlined />} loading={diagnostics.isPending} onClick={() => diagnostics.mutate()}>导出诊断包</Button>
        <Button icon={<DownloadOutlined />} loading={configExport.isPending && configExport.variables === false} onClick={() => configExport.mutate(false)}>导出脱敏配置</Button>
        <Popconfirm title="导出包含密钥的完整配置？" description="文件中包含 Token、密码和设备密钥，泄露后可导致账号被盗用。" onConfirm={() => configExport.mutate(true)}><Button danger icon={<DownloadOutlined />} loading={configExport.isPending && configExport.variables === true}>导出完整配置</Button></Popconfirm>
        <Upload accept="application/json,.json" showUploadList={false} beforeUpload={(file) => { void selectBackup(file); return false; }}><Button type="primary" icon={<UploadOutlined />} loading={configImport.isPending}>导入配置</Button></Upload>
      </Space>
    </Space>
  );
}

function downloadJSON(value: unknown, filename: string) {
  const blob = new Blob([JSON.stringify(value, null, 2)], { type: 'application/json' });
  const link = document.createElement('a');
  link.href = URL.createObjectURL(blob);
  link.download = filename;
  link.click();
  URL.revokeObjectURL(link.href);
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
  return ({ create: '创建', update: '修改', delete: '归档', check: '检查', test: '测试', retry: '重试', export: '导出', import: '导入' } as Record<string, string>)[value] ?? value;
}

function auditEntityLabel(value: string) {
  return ({ monitor: '监控', rule: '规则', channel: '渠道', template: '模板', notification_attempt: '通知尝试', dead_letter: '死信事件', config: '系统配置' } as Record<string, string>)[value] ?? value;
}
