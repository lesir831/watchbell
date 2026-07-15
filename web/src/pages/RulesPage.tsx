import { useMemo, useState } from 'react';
import {
  Alert,
  App as AntApp,
  Button,
  Card,
  Col,
  Drawer,
  Form,
  Grid,
  Input,
  InputNumber,
  Popconfirm,
  Row,
  Select,
  Space,
  Switch,
  Table,
  Tag,
  Typography
} from 'antd';
import { DeleteOutlined, EditOutlined, MinusCircleOutlined, PlusOutlined, SearchOutlined } from '@ant-design/icons';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from '../api';
import { EmptyState, PageError, relativeDate, StatusTag } from '../components/Common';
import type { Monitor, MonitorPlugin, NotificationTemplate, NotifyChannel, Rule, RuleInput } from '../types';

const { Text, Title } = Typography;

type ConditionRow = { field: string; operator: string; value?: string };

export default function RulesPage() {
  const mobile = !Grid.useBreakpoint().md;
  const queryClient = useQueryClient();
  const { message } = AntApp.useApp();
  const [editing, setEditing] = useState<Rule | null>(null);
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [search, setSearch] = useState('');
  const rules = useQuery({ queryKey: ['rules'], queryFn: api.listRules });
  const monitors = useQuery({ queryKey: ['monitors'], queryFn: api.listMonitors });
  const channels = useQuery({ queryKey: ['channels'], queryFn: api.listChannels });
  const templates = useQuery({ queryKey: ['templates'], queryFn: api.listTemplates });
  const plugins = useQuery({ queryKey: ['plugins'], queryFn: api.listPlugins });
  const monitorByID = useMemo(() => new Map((monitors.data ?? []).map((item) => [item.id, item])), [monitors.data]);
  const channelByID = useMemo(() => new Map((channels.data ?? []).map((item) => [item.id, item])), [channels.data]);

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
  const filtered = (rules.data ?? []).filter((item) => item.name.toLowerCase().includes(search.toLowerCase()));
  const canCreate = Boolean(monitors.data?.length && channels.data?.length);
  const openNew = () => { setEditing(null); setDrawerOpen(true); };
  const actions = (record: Rule) => (
    <Space>
      <Button icon={<EditOutlined />} onClick={() => { setEditing(record); setDrawerOpen(true); }}>编辑</Button>
      <Popconfirm title="归档这条规则？" description="既有规则判断与通知历史会继续保留。" onConfirm={() => deleteMutation.mutate(record.id)}><Button danger icon={<DeleteOutlined />} aria-label={`归档 ${record.name}`} /></Popconfirm>
    </Space>
  );

  return (
    <Space direction="vertical" size={16} className="full-width">
      <PageError error={(rules.error || monitors.error || channels.error) as Error | null} onRetry={() => { rules.refetch(); monitors.refetch(); channels.refetch(); }} />
      {!canCreate && <Alert type="warning" showIcon message="创建规则前需要至少一个监控和一个通知渠道" description="规则负责把监控产生的事件发送到指定渠道。" />}
      <div className="page-toolbar responsive-toolbar">
        <Input prefix={<SearchOutlined />} allowClear placeholder="搜索规则" value={search} onChange={(event) => setSearch(event.target.value)} style={{ maxWidth: 320 }} />
        <Button type="primary" icon={<PlusOutlined />} disabled={!canCreate} onClick={openNew}>新建规则</Button>
      </div>
      {filtered.length === 0 && !rules.isLoading ? (
        <Card><EmptyState title={rules.data?.length ? '没有符合条件的规则' : '还没有触发规则'} description="用可视化条件决定哪些事件需要通知。" action={canCreate && !rules.data?.length ? <Button type="primary" onClick={openNew}>创建第一条规则</Button> : undefined} /></Card>
      ) : mobile ? (
        <div className="mobile-card-list">{filtered.map((item) => (
          <Card key={item.id} className="entity-card">
            <div className="entity-card-head"><div><Title level={5}>{item.name}</Title><Text type="secondary">{monitorByID.get(item.monitorId)?.name ?? `监控 #${item.monitorId}`}</Text></div><StatusTag status={item.enabled ? 'ok' : 'disabled'} /></div>
            <div className="tag-row">{item.notifyChannelIds.map((id) => <Tag key={id}>{channelByID.get(id)?.name ?? `渠道 #${id}`}</Tag>)}</div>
            <Text type="secondary">上次触发：{relativeDate(item.lastFiredAt)}</Text>
            <div className="entity-actions">{actions(item)}</div>
          </Card>
        ))}</div>
      ) : (
        <Table<Rule>
          rowKey="id" loading={rules.isLoading} dataSource={filtered} scroll={{ x: 900 }} pagination={{ pageSize: 12, showSizeChanger: false }}
          columns={[
            { title: '名称', dataIndex: 'name', width: 190 },
            { title: '监控', dataIndex: 'monitorId', width: 180, render: (id) => monitorByID.get(id)?.name ?? `#${id}` },
            { title: '状态', dataIndex: 'enabled', width: 90, render: (value) => <StatusTag status={value ? 'ok' : 'disabled'} /> },
            { title: '通知渠道', dataIndex: 'notifyChannelIds', render: (ids: number[]) => <Space wrap>{ids.map((id) => <Tag key={id}>{channelByID.get(id)?.name ?? `#${id}`}</Tag>)}</Space> },
            { title: '上次触发', dataIndex: 'lastFiredAt', width: 130, render: relativeDate },
            { title: '操作', width: 190, render: (_, record) => actions(record) }
          ]}
        />
      )}
      <RuleDrawer
        open={drawerOpen} record={editing} monitors={monitors.data ?? []} channels={channels.data ?? []}
        templates={templates.data ?? []} plugins={plugins.data ?? []} saving={saveMutation.isPending} error={saveMutation.error as Error | null}
        onClose={() => { setDrawerOpen(false); saveMutation.reset(); }} onSave={(input) => saveMutation.mutate({ id: editing?.id, input })}
      />
    </Space>
  );
}

function RuleDrawer(props: {
  open: boolean; record: Rule | null; monitors: Monitor[]; channels: NotifyChannel[]; templates: NotificationTemplate[]; plugins: MonitorPlugin[];
  saving: boolean; error: Error | null; onClose: () => void; onSave: (input: RuleInput) => void;
}) {
  const [form] = Form.useForm();
  const monitorId = Form.useWatch<number>('monitorId', form);
  const allEvents = Form.useWatch<boolean>('allEvents', form);
  const monitor = props.monitors.find((item) => item.id === monitorId);
  const plugin = props.plugins.find((item) => item.id === monitor?.type);
  const fieldOptions = (plugin?.templateVariables ?? []).map((value) => ({ label: value, value }));

  const setInitial = () => {
    const condition = props.record?.condition as { match?: string; conditions?: ConditionRow[] } | undefined;
    const isAllEvents = !condition || Object.keys(condition).length === 0;
    const initialMonitor = props.monitors.find((item) => item.id === (props.record?.monitorId ?? props.monitors[0]?.id));
    const initialPlugin = props.plugins.find((item) => item.id === initialMonitor?.type);
    form.setFieldsValue({
      name: props.record?.name ?? '', monitorId: props.record?.monitorId ?? props.monitors[0]?.id,
      enabled: props.record?.enabled ?? true, cooldownSeconds: props.record?.cooldownSeconds ?? 0,
      notifyChannelIds: props.record?.notifyChannelIds ?? [], templateId: props.record?.templateId ?? 1,
      allEvents: isAllEvents, match: condition?.match ?? 'all',
      conditions: condition?.conditions?.length ? condition.conditions : [{ field: initialPlugin?.templateVariables[0], operator: 'contains', value: '' }]
    });
  };
  const submit = async () => {
    const values = await form.validateFields();
    const condition = values.allEvents ? {} : { match: values.match, conditions: values.conditions };
    props.onSave({ name: values.name.trim(), monitorId: values.monitorId, enabled: values.enabled, cooldownSeconds: values.cooldownSeconds ?? 0, notifyChannelIds: values.notifyChannelIds, templateId: values.templateId ?? null, condition });
  };
  return (
    <Drawer title={props.record ? '编辑规则' : '新建规则'} open={props.open} onClose={props.onClose} width={720} destroyOnClose afterOpenChange={(open) => { if (open) setInitial(); }} footer={<div className="drawer-footer"><Button onClick={props.onClose}>取消</Button><Button type="primary" loading={props.saving} onClick={submit}>保存规则</Button></div>}>
      <PageError error={props.error} />
      <Form form={form} layout="vertical" requiredMark="optional" onValuesChange={(changed) => {
        if (changed.monitorId && !props.record) {
          const nextMonitor = props.monitors.find((item) => item.id === changed.monitorId);
          const nextPlugin = props.plugins.find((item) => item.id === nextMonitor?.type);
          form.setFieldValue('conditions', [{ field: nextPlugin?.templateVariables[0], operator: 'contains', value: '' }]);
        }
      }}>
        <Form.Item name="name" label="规则名称" rules={[{ required: true, whitespace: true }]}><Input placeholder="例如：标题包含 TestFlight" /></Form.Item>
        <Form.Item name="monitorId" label="关联监控" rules={[{ required: true }]}><Select options={props.monitors.map((item) => ({ label: item.name, value: item.id }))} /></Form.Item>
        {plugin && <Alert className="form-intro" type="info" showIcon message={`监听 ${plugin.events.join('、')}`} description="条件字段会根据所选监控自动限制，避免保存无法匹配的规则。" />}
        <Form.Item name="allEvents" label="匹配所有新事件" valuePropName="checked"><Switch /></Form.Item>
        {!allEvents && (
          <div className="condition-builder">
            <Form.Item name="match" label="条件关系"><Select options={[{ label: '满足全部条件', value: 'all' }, { label: '满足任一条件', value: 'any' }]} /></Form.Item>
            <Form.List name="conditions">
              {(fields, { add, remove }) => (
                <Space direction="vertical" className="full-width">
                  {fields.map((field, index) => (
                    <Row gutter={8} key={field.key} align="top">
                      <Col xs={24} md={9}><Form.Item {...field} name={[field.name, 'field']} label={index === 0 ? '事件字段' : undefined} rules={[{ required: true }]}><Select showSearch options={fieldOptions} /></Form.Item></Col>
                      <Col xs={10} md={6}><Form.Item {...field} name={[field.name, 'operator']} label={index === 0 ? '判断方式' : undefined} rules={[{ required: true }]}><Select options={operatorOptions} /></Form.Item></Col>
                      <Col xs={12} md={8}><Form.Item noStyle shouldUpdate={(previous, current) => previous.conditions?.[index]?.operator !== current.conditions?.[index]?.operator}>{({ getFieldValue }) => getFieldValue(['conditions', index, 'operator']) === 'exists' ? null : <Form.Item {...field} name={[field.name, 'value']} label={index === 0 ? '值' : undefined} rules={[{ required: true }]}><Input /></Form.Item>}</Form.Item></Col>
                      <Col xs={2} md={1} className={index === 0 ? 'condition-remove labeled' : 'condition-remove'}><Button type="text" danger icon={<MinusCircleOutlined />} disabled={fields.length === 1} onClick={() => remove(field.name)} aria-label="删除条件" /></Col>
                    </Row>
                  ))}
                  <Button type="dashed" block icon={<PlusOutlined />} onClick={() => add({ field: fieldOptions[0]?.value, operator: 'contains', value: '' })}>添加条件</Button>
                </Space>
              )}
            </Form.List>
          </div>
        )}
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

const operatorOptions = [
  { label: '包含', value: 'contains' }, { label: '不包含', value: 'not_contains' },
  { label: '等于', value: 'equals' }, { label: '正则表达式', value: 'regex' }, { label: '字段存在', value: 'exists' }
];
