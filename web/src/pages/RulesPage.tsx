import { useEffect, useMemo, useRef, useState } from 'react';
import {
  Alert,
  App as AntApp,
  Button,
  Col,
  Drawer,
  Form,
  Input,
  InputNumber,
  Popconfirm,
  Row,
  Select,
  Space,
  Switch,
  Tag,
  Typography
} from 'antd';
import { BranchesOutlined, DeleteOutlined, EditOutlined, ExperimentOutlined, PlusOutlined, SearchOutlined } from '@ant-design/icons';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from '../api';
import ConditionBuilder, { defaultConditionGroup, normalizeConditionGroup, validateConditionGroup } from '../components/ConditionBuilder';
import { EmptyState, PageError, PageHeader, relativeDate } from '../components/Common';
import type { Monitor, MonitorPlugin, NotificationTemplate, NotifyChannel, Rule, RuleConditionGroup, RuleInput } from '../types';

const { Text } = Typography;

const browserTimezone = Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC';
const clockPattern = /^(?:[01]\d|2[0-3]):[0-5]\d$/;

export default function RulesPage({ editRuleId }: { editRuleId?: number }) {
  const queryClient = useQueryClient();
  const { message } = AntApp.useApp();
  const [editing, setEditing] = useState<Rule | null>(null);
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [search, setSearch] = useState('');
  const [status, setStatus] = useState<'all' | 'enabled' | 'disabled'>('all');
  const handledRuleLink = useRef<number>();
  const rules = useQuery({ queryKey: ['rules'], queryFn: api.listRules, refetchInterval: 30_000 });
  const monitors = useQuery({ queryKey: ['monitors'], queryFn: api.listMonitors, refetchInterval: 30_000 });
  const channels = useQuery({ queryKey: ['channels'], queryFn: api.listChannels, refetchInterval: 30_000 });
  const templates = useQuery({ queryKey: ['templates'], queryFn: api.listTemplates, refetchInterval: 30_000 });
  const plugins = useQuery({ queryKey: ['plugins'], queryFn: api.listPlugins });
  const settings = useQuery({ queryKey: ['settings'], queryFn: api.settings, staleTime: 60_000 });
  const monitorByID = useMemo(() => new Map((monitors.data ?? []).map((item) => [item.id, item])), [monitors.data]);
  const templateByID = useMemo(() => new Map((templates.data ?? []).map((item) => [item.id, item])), [templates.data]);
  const pluginByID = useMemo(() => new Map((plugins.data ?? []).map((item) => [item.id, item])), [plugins.data]);

  const refresh = async () => Promise.all([
    queryClient.invalidateQueries({ queryKey: ['rules'] }),
    queryClient.invalidateQueries({ queryKey: ['dashboard'] }),
    queryClient.invalidateQueries({ queryKey: ['auditLogs'] })
  ]);
  const saveMutation = useMutation({
    mutationFn: (payload: { id?: number; input: RuleInput }) => payload.id ? api.updateRule(payload.id, payload.input) : api.createRule(payload.input),
    onSuccess: async () => { await refresh(); setDrawerOpen(false); setEditing(null); message.success('规则已保存'); }
  });
  const deleteMutation = useMutation({
    mutationFn: api.deleteRule,
    onSuccess: async () => { await refresh(); message.success('规则已归档，历史判断记录仍会保留'); },
    onError: (error: Error) => message.error(error.message)
  });
  const toggleMutation = useMutation({
    mutationFn: ({ record, enabled }: { record: Rule; enabled: boolean }) => api.updateRule(record.id, {
      monitorId: record.monitorId, name: record.name, enabled, condition: record.condition,
      notifyChannelIds: record.notifyChannelIds, templateId: record.templateId ?? null,
      cooldownSeconds: record.cooldownSeconds, quietHours: record.quietHours
    }),
    onSuccess: async (item) => { await refresh(); message.success(item.enabled ? '规则已启用' : '规则已停用'); },
    onError: (error: Error) => message.error(error.message)
  });
  const testMutation = useMutation({
    mutationFn: (record: Rule) => api.testRule({ monitorId: record.monitorId, condition: record.condition, limit: 20 }),
    onSuccess: (result) => message.success(`测试 ${result.tested} 条事件，命中 ${result.matched} 条`),
    onError: (error: Error) => message.error(error.message)
  });
  const normalizedSearch = search.trim().toLowerCase();
  const filtered = (rules.data ?? []).filter((item) => {
    const monitorName = monitorByID.get(item.monitorId)?.name ?? '';
    return (status === 'all' || (status === 'enabled' ? item.enabled : !item.enabled))
      && `${item.name} ${monitorName}`.toLowerCase().includes(normalizedSearch);
  });
  const canCreate = Boolean(monitors.data?.length && channels.data?.some((item) => item.enabled));
  const openNew = () => { setEditing(null); setDrawerOpen(true); };
  useEffect(() => {
    if (!editRuleId || !rules.data || handledRuleLink.current === editRuleId) return;
    handledRuleLink.current = editRuleId;
    const record = rules.data.find((item) => item.id === editRuleId);
    if (!record) {
      message.warning(`规则 #${editRuleId} 不存在或已归档`);
      return;
    }
    setEditing(record);
    setDrawerOpen(true);
  }, [editRuleId, message, rules.data]);
  return (
    <div className="design-page">
      <PageHeader
        eyebrow="判断与路由"
        title="规则"
        description="把监控事件变成明确条件，再指定模板和通知渠道。测试结果在保存前即可预览。"
        actions={<Button className="design-primary" type="primary" icon={<PlusOutlined />} disabled={!canCreate} onClick={openNew}>新建规则</Button>}
      />
      <PageError error={(rules.error || monitors.error || channels.error || templates.error || plugins.error) as Error | null} onRetry={() => { rules.refetch(); monitors.refetch(); channels.refetch(); templates.refetch(); plugins.refetch(); }} />
      {!canCreate && <Alert type="warning" showIcon message="创建规则前需要至少一个监控和一个已启用通知渠道" description="规则负责把监控产生的事件发送到指定渠道；停用渠道不会接收通知。" />}
      <div className="design-toolbar">
        <div className="filter-group" role="group" aria-label="规则状态筛选">
          {[['all', '全部'], ['enabled', '已启用'], ['disabled', '已停用']].map(([value, label]) => <button key={value} type="button" className={`filter-button ${status === value ? 'active' : ''}`} onClick={() => setStatus(value as typeof status)}>{label}</button>)}
        </div>
        <label className="search-box"><SearchOutlined /><span className="sr-only">搜索规则</span><input type="search" placeholder="搜索规则或监控" value={search} onChange={(event) => setSearch(event.target.value)} /></label>
      </div>
      {filtered.length === 0 && !rules.isLoading ? (
        <div className="empty-panel"><EmptyState title={rules.data?.length ? '没有符合条件的规则' : '还没有触发规则'} description="用可视化条件决定哪些事件需要通知。" action={canCreate && !rules.data?.length ? <Button type="primary" onClick={openNew}>创建第一条规则</Button> : undefined} /></div>
      ) : (
        <div className="collection-grid">
          {filtered.map((item) => {
            const monitor = monitorByID.get(item.monitorId);
            const eventLabel = pluginByID.get(monitor?.type ?? 'rss')?.events?.[0] ?? monitor?.type ?? '全部事件';
            return <article key={item.id} className="resource-card">
              <div className="resource-card-head">
                <div className="resource-card-title"><span className="type-mark"><BranchesOutlined /></span><div><h2>{item.name}</h2><p>{monitor?.name ?? `监控 #${item.monitorId}`}</p></div></div>
                <Switch checked={item.enabled} loading={toggleMutation.isPending && toggleMutation.variables?.record.id === item.id} aria-label={`${item.enabled ? '停用' : '启用'} ${item.name}`} onChange={(enabled) => toggleMutation.mutate({ record: item, enabled })} />
              </div>
              <p className="resource-description">{conditionSummary(item)}，命中后通过 {item.notifyChannelIds.length} 个渠道发送通知。</p>
              <div className="resource-meta">
                <div><span>事件</span><strong className="number">{eventLabel}</strong></div>
                <div><span>最近匹配</span><strong>{relativeDate(item.lastFiredAt)}</strong></div>
                <div><span>模板</span><strong>{item.templateId ? templateByID.get(item.templateId)?.name ?? `模板 #${item.templateId}` : '系统默认'}</strong></div>
                <div><span>渠道</span><strong>{item.notifyChannelIds.length} 个</strong></div>
              </div>
              <div className="resource-actions">
                <Button className="mini-action" icon={<ExperimentOutlined />} loading={testMutation.isPending && testMutation.variables?.id === item.id} onClick={() => testMutation.mutate(item)}>测试规则</Button>
                <Button className="mini-action" icon={<EditOutlined />} onClick={() => { setEditing(item); setDrawerOpen(true); }}>编辑</Button>
                <Popconfirm title="归档这条规则？" description="既有规则判断与通知历史会继续保留。" onConfirm={() => deleteMutation.mutate(item.id)}><Button className="mini-action icon-only" danger icon={<DeleteOutlined />} aria-label={`归档 ${item.name}`} /></Popconfirm>
              </div>
            </article>;
          })}
        </div>
      )}
      <RuleDrawer
        open={drawerOpen} record={editing} monitors={monitors.data ?? []} channels={channels.data ?? []}
        templates={templates.data ?? []} plugins={plugins.data ?? []} defaultTimezone={settings.data?.timezone ?? browserTimezone} saving={saveMutation.isPending} error={saveMutation.error as Error | null}
        onClose={() => { setDrawerOpen(false); saveMutation.reset(); }} onSave={(input) => saveMutation.mutate({ id: editing?.id, input })}
      />
    </div>
  );
}

function conditionSummary(rule: Rule) {
  const conditions = (rule.condition as Partial<RuleConditionGroup>).conditions;
  if (!Array.isArray(conditions) || conditions.length === 0) return '匹配该监控产生的全部新事件';
  return `${(rule.condition as RuleConditionGroup).match === 'any' ? '任一' : '全部'}满足 ${conditions.length} 个条件`;
}

function RuleDrawer(props: {
  open: boolean; record: Rule | null; monitors: Monitor[]; channels: NotifyChannel[]; templates: NotificationTemplate[]; plugins: MonitorPlugin[];
  defaultTimezone: string; saving: boolean; error: Error | null; onClose: () => void; onSave: (input: RuleInput) => void;
}) {
  const [form] = Form.useForm();
  const { message } = AntApp.useApp();
  const [conditionTree, setConditionTree] = useState<RuleConditionGroup>(() => defaultConditionGroup());
  const monitorId = Form.useWatch<number>('monitorId', form);
  const allEvents = Form.useWatch<boolean>('allEvents', form);
  const quietHoursEnabled = Form.useWatch<boolean>(['quietHours', 'enabled'], form);
  const monitor = props.monitors.find((item) => item.id === monitorId);
  const plugin = props.plugins.find((item) => item.id === monitor?.type);
  const defaultTemplateId = props.templates.find((item) => item.isDefault)?.id;
  const conditionFields = plugin?.templateVariables ?? [];
  const testRule = useMutation({
    mutationFn: async () => {
      const values = await form.validateFields(['monitorId', 'allEvents']);
      const conditionError = values.allEvents ? null : validateConditionGroup(conditionTree);
      if (conditionError) throw new Error(conditionError);
      const condition = values.allEvents ? {} : conditionTree;
      return api.testRule({ monitorId: values.monitorId, condition, limit: 20 });
    },
    onError: (error: Error) => message.error(error.message)
  });

  const setInitial = () => {
    testRule.reset();
    const condition = props.record?.condition;
    const storedConditions = (condition as Partial<RuleConditionGroup> | undefined)?.conditions;
    const isAllEvents = !condition || Object.keys(condition).length === 0 || (Array.isArray(storedConditions) && storedConditions.length === 0);
    const initialMonitor = props.monitors.find((item) => item.id === (props.record?.monitorId ?? props.monitors[0]?.id));
    const initialPlugin = props.plugins.find((item) => item.id === initialMonitor?.type);
    setConditionTree(normalizeConditionGroup(condition, initialPlugin?.templateVariables[0] ?? ''));
    form.setFieldsValue({
      name: props.record?.name ?? '', monitorId: props.record?.monitorId ?? props.monitors[0]?.id,
      enabled: props.record?.enabled ?? true, cooldownSeconds: props.record?.cooldownSeconds ?? 0,
      notifyChannelIds: props.record?.notifyChannelIds ?? [], templateId: props.record?.templateId ?? defaultTemplateId,
      allEvents: isAllEvents,
      quietHours: { enabled: false, start: '22:00', end: '08:00', timezone: props.defaultTimezone, ...props.record?.quietHours }
    });
  };
  const submit = async () => {
    const values = await form.validateFields();
    const conditionError = values.allEvents ? null : validateConditionGroup(conditionTree);
    if (conditionError) {
      message.error(conditionError);
      return;
    }
    const condition = values.allEvents ? {} : conditionTree;
    props.onSave({
      name: values.name.trim(), monitorId: values.monitorId, enabled: values.enabled,
      cooldownSeconds: values.cooldownSeconds ?? 0, notifyChannelIds: values.notifyChannelIds,
      templateId: values.templateId ?? null, condition,
      quietHours: {
        enabled: Boolean(values.quietHours?.enabled),
        start: values.quietHours?.start,
        end: values.quietHours?.end,
        timezone: values.quietHours?.timezone?.trim()
      }
    });
  };
  return (
    <Drawer title={props.record ? '编辑规则' : '新建规则'} open={props.open} onClose={props.onClose} width={920} destroyOnClose afterOpenChange={(open) => { if (open) setInitial(); }} footer={<div className="drawer-footer"><Button onClick={props.onClose}>取消</Button><Button type="primary" loading={props.saving} onClick={submit}>保存规则</Button></div>}>
      <PageError error={props.error} />
      <Form form={form} layout="vertical" requiredMark="optional" onValuesChange={(changed) => {
        testRule.reset();
        if (changed.monitorId && !props.record) {
          const nextMonitor = props.monitors.find((item) => item.id === changed.monitorId);
          const nextPlugin = props.plugins.find((item) => item.id === nextMonitor?.type);
          setConditionTree(defaultConditionGroup(nextPlugin?.templateVariables[0] ?? ''));
        }
      }}>
        <Form.Item name="name" label="规则名称" rules={[{ required: true, whitespace: true }]}><Input placeholder="例如：标题包含 TestFlight" /></Form.Item>
        <Form.Item name="monitorId" label="关联监控" rules={[{ required: true }]}><Select options={props.monitors.map((item) => ({ label: item.name, value: item.id }))} /></Form.Item>
        {plugin && <Alert className="form-intro" type="info" showIcon message={`监听 ${plugin.events.join('、')}`} description="条件字段会根据所选监控自动限制，避免保存无法匹配的规则。" />}
        <Form.Item name="allEvents" label="匹配所有新事件" valuePropName="checked"><Switch /></Form.Item>
        {!allEvents && (
          <div className="condition-builder">
            <Alert type="info" showIcon message="支持嵌套条件组" description="条件组可以继续包含 AND / OR 子组；“在最近时间内”使用 2m、30s、1h 等时长。" />
            <ConditionBuilder value={conditionTree} fields={conditionFields} onChange={(value) => { setConditionTree(value); testRule.reset(); }} />
          </div>
        )}
        <div className="rule-test-row">
          <Button icon={<ExperimentOutlined />} loading={testRule.isPending} onClick={() => testRule.mutate()}>用最近事件测试</Button>
          <Text type="secondary">只读取最近 20 条事件，不会发送通知或修改规则。</Text>
        </div>
        {testRule.data && <Alert className="rule-test-result" type={testRule.data.matched ? 'success' : 'warning'} showIcon message={`测试 ${testRule.data.tested} 条事件，命中 ${testRule.data.matched} 条`} description={testRule.data.results.length ? <Space wrap>{testRule.data.results.map((item) => <Tag key={item.eventId}>事件 #{item.eventId} · {item.eventType}</Tag>)}</Space> : '当前最近事件没有符合该条件的记录。'} />}
        <div className="condition-builder">
          <Form.Item name={['quietHours', 'enabled']} label="免打扰时段" valuePropName="checked" extra="命中的事件仍会留下规则判断记录，但不会发送通知。"><Switch /></Form.Item>
          {quietHoursEnabled && (
            <>
              <Row gutter={16}>
                <Col xs={24} md={12}>
                  <Form.Item name={['quietHours', 'start']} label="开始时间" rules={[{ required: true, message: '请选择开始时间' }, { pattern: clockPattern, message: '请使用 HH:mm 格式' }]}>
                    <Input type="time" step={60} />
                  </Form.Item>
                </Col>
                <Col xs={24} md={12}>
                  <Form.Item dependencies={[["quietHours", "start"]]} name={['quietHours', 'end']} label="结束时间" rules={[
                    { required: true, message: '请选择结束时间' },
                    { pattern: clockPattern, message: '请使用 HH:mm 格式' },
                    ({ getFieldValue }) => ({ validator: (_, value) => value && value === getFieldValue(['quietHours', 'start']) ? Promise.reject(new Error('结束时间不能与开始时间相同')) : Promise.resolve() })
                  ]}>
                    <Input type="time" step={60} />
                  </Form.Item>
                </Col>
              </Row>
              <Form.Item name={['quietHours', 'timezone']} label="时区" extra="使用 IANA 时区名；跨午夜和夏令时会按该时区自动处理。" rules={[{ required: true, whitespace: true, message: '请输入 IANA 时区' }]}>
                <Input placeholder="例如 Asia/Shanghai" />
              </Form.Item>
            </>
          )}
        </div>
        <Form.Item name="notifyChannelIds" label="通知渠道" rules={[{ required: true, type: 'array', min: 1, message: '至少选择一个渠道' }]}><Select mode="multiple" options={props.channels.map((item) => ({ label: `${item.name}${item.enabled ? '' : '（已停用）'}`, value: item.id, disabled: !item.enabled }))} /></Form.Item>
        <Form.Item name="templateId" label="通知模板"><Select allowClear options={props.templates.map((item) => ({ label: item.name, value: item.id }))} /></Form.Item>
        <Row gutter={16}>
          <Col span={12}><Form.Item name="cooldownSeconds" label="冷却时间（秒）" extra="同一规则在冷却期内不会重复通知。"><InputNumber min={0} max={31_536_000} className="full-width" /></Form.Item></Col>
          <Col span={12}><Form.Item name="enabled" label="启用规则" valuePropName="checked"><Switch /></Form.Item></Col>
        </Row>
      </Form>
    </Drawer>
  );
}
