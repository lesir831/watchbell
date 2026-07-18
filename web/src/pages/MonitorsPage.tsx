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
import { DeleteOutlined, EditOutlined, EyeOutlined, PlayCircleOutlined, PlusOutlined, SearchOutlined } from '@ant-design/icons';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from '../api';
import ConfigFields from '../components/ConfigFields';
import { AdvancedConfigField, ConfigMode, parseConfigJSON } from '../components/ConfigMode';
import { EmptyState, formatDate, formatInterval, PageError, relativeDate, StatusTag } from '../components/Common';
import type { Monitor, MonitorInput, MonitorPlugin, MonitorType, NotifyChannel, ProxyProfile } from '../types';

const { Text, Title } = Typography;

export default function MonitorsPage({ onNavigate }: { onNavigate: (page: string) => void }) {
  const screens = Grid.useBreakpoint();
  const mobile = !screens.md;
  const queryClient = useQueryClient();
  const { message } = AntApp.useApp();
  const [editing, setEditing] = useState<Monitor | null>(null);
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [search, setSearch] = useState('');
  const [status, setStatus] = useState<string>('all');
  const monitors = useQuery({ queryKey: ['monitors'], queryFn: api.listMonitors, refetchInterval: 20_000 });
  const plugins = useQuery({ queryKey: ['plugins'], queryFn: api.listPlugins });
  const channels = useQuery({ queryKey: ['channels'], queryFn: api.listChannels, refetchInterval: 30_000 });
  const proxies = useQuery({ queryKey: ['proxies'], queryFn: api.listProxies, refetchInterval: 30_000 });
  const pluginByID = useMemo(() => new Map((plugins.data ?? []).map((item) => [item.id, item])), [plugins.data]);
  const proxyByID = useMemo(() => new Map((proxies.data ?? []).map((item) => [item.id, item])), [proxies.data]);

  const refresh = async () => {
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: ['monitors'] }),
      queryClient.invalidateQueries({ queryKey: ['dashboard'] }),
      queryClient.invalidateQueries({ queryKey: ['checkRuns'] }),
      queryClient.invalidateQueries({ queryKey: ['events'] })
    ]);
  };
  const saveMutation = useMutation({
    mutationFn: async (payload: { id?: number; input: MonitorInput; checkAfter: boolean }) => {
      const item = payload.id ? await api.updateMonitor(payload.id, payload.input) : await api.createMonitor(payload.input);
      return { item, checkAfter: payload.checkAfter };
    },
    onSuccess: async ({ item, checkAfter }) => {
      setDrawerOpen(false);
      setEditing(null);
      await refresh();
      message.success('监控已保存');
      if (checkAfter) {
        const hide = message.loading('正在执行首次检查…', 0);
        try {
          const result = await api.checkMonitor(item.id);
          message.success(result.eventCount > 0 ? `检查完成，发现 ${result.eventCount} 个新事件` : '检查完成，未发现新事件');
        } catch (error) {
          message.error((error as Error).message);
        } finally {
          hide();
          await refresh();
        }
      }
    }
  });
  const checkMutation = useMutation({
    mutationFn: api.checkMonitor,
    onSuccess: async (result) => { await refresh(); message.success(result.eventCount > 0 ? `检查完成，发现 ${result.eventCount} 个新事件` : '检查完成，未发现新事件'); },
    onError: async (error: Error) => { await refresh(); message.error(error.message); }
  });
  const deleteMutation = useMutation({
    mutationFn: api.deleteMonitor,
    onSuccess: async () => { await refresh(); message.success('监控已归档，历史记录仍会保留'); },
    onError: (error: Error) => message.error(error.message)
  });
  const toggleMutation = useMutation({
    mutationFn: ({ record, enabled }: { record: Monitor; enabled: boolean }) => api.updateMonitor(record.id, {
      name: record.name, type: record.type, proxyId: record.proxyId ?? null, enabled, intervalSeconds: record.intervalSeconds, config: record.config,
      failureAlertAfter: record.failureAlertAfter, failureNotifyChannelIds: record.failureNotifyChannelIds
    }),
    onSuccess: async (item) => { await refresh(); message.success(item.enabled ? '监控已启用' : '监控已停用'); },
    onError: (error: Error) => message.error(error.message)
  });

  const filtered = (monitors.data ?? []).filter((item) => {
    const matchesSearch = item.name.toLowerCase().includes(search.toLowerCase()) || item.type.includes(search.toLowerCase()) || (item.proxyId ? proxyByID.get(item.proxyId)?.name.toLowerCase().includes(search.toLowerCase()) === true : false);
    const matchesStatus = status === 'all' || (status === 'enabled' ? item.enabled : status === 'disabled' ? !item.enabled : item.lastStatus === status);
    return matchesSearch && matchesStatus;
  });

  const openNew = () => { setEditing(null); setDrawerOpen(true); };
  const actions = (record: Monitor) => (
    <Space wrap>
      <Button icon={<EyeOutlined />} onClick={() => onNavigate(`monitors/${record.id}`)}>详情</Button>
      <Button icon={<PlayCircleOutlined />} loading={checkMutation.isPending && checkMutation.variables === record.id} onClick={() => checkMutation.mutate(record.id)}>检查</Button>
      <Button icon={<EditOutlined />} onClick={() => { setEditing(record); setDrawerOpen(true); }}>编辑</Button>
      <Popconfirm title="归档这个监控？" description="监控及其规则将停止运行，既有检查、事件和通知历史会保留。" onConfirm={() => deleteMutation.mutate(record.id)}>
        <Button danger icon={<DeleteOutlined />} aria-label={`归档 ${record.name}`} />
      </Popconfirm>
    </Space>
  );

  return (
    <Space direction="vertical" size={16} className="full-width">
      <PageError error={(monitors.error || plugins.error || channels.error || proxies.error) as Error | null} onRetry={() => { monitors.refetch(); plugins.refetch(); channels.refetch(); proxies.refetch(); }} />
      <div className="page-toolbar responsive-toolbar">
        <Space wrap>
          <Input prefix={<SearchOutlined />} allowClear placeholder="搜索名称或类型" value={search} onChange={(event) => setSearch(event.target.value)} />
          <Select value={status} onChange={setStatus} style={{ width: 132 }} options={[
            { label: '全部状态', value: 'all' }, { label: '已启用', value: 'enabled' }, { label: '已停用', value: 'disabled' },
            { label: '错误', value: 'error' }, { label: '警告', value: 'warning' }, { label: '正常', value: 'ok' }
          ]} />
        </Space>
        <Button type="primary" icon={<PlusOutlined />} onClick={openNew}>新建监控</Button>
      </div>

      {filtered.length === 0 && !monitors.isLoading ? (
        <Card><EmptyState title={monitors.data?.length ? '没有符合筛选条件的监控' : '还没有监控'} description="选择 RSS、网页、TestFlight 或 GitHub Release 开始监听变化。" action={!monitors.data?.length ? <Button type="primary" onClick={openNew}>创建第一个监控</Button> : undefined} /></Card>
      ) : mobile ? (
        <div className="mobile-card-list">
          {filtered.map((item) => (
            <Card key={item.id} className="entity-card">
              <div className="entity-card-head"><div><Title level={5}>{item.name}</Title><Text type="secondary">{pluginByID.get(item.type)?.name ?? item.type}</Text></div><Space direction="vertical" align="end" size={4}><StatusTag status={!item.enabled ? 'disabled' : item.lastStatus} />{item.failureAlertActive && <Tag color="error">故障告警中</Tag>}</Space></div>
              <div className="entity-meta"><span>检查频率 <strong>{formatInterval(item.intervalSeconds)}</strong></span><span>网络 <strong>{item.proxyId ? proxyByID.get(item.proxyId)?.name ?? `代理 #${item.proxyId}` : '默认网络'}</strong></span><span>上次检查 <strong>{relativeDate(item.lastCheckedAt)}</strong></span></div>
              {(item.lastError || item.lastMessage) && <Alert type={item.lastError ? 'error' : 'info'} showIcon message={item.lastError || item.lastMessage} />}
              <div className="entity-actions">{actions(item)}</div>
            </Card>
          ))}
        </div>
      ) : (
        <Table<Monitor>
          rowKey="id"
          loading={monitors.isLoading}
          dataSource={filtered}
          scroll={{ x: 980 }}
          pagination={{ pageSize: 12, showSizeChanger: false }}
          expandable={{
            expandedRowRender: (record) => (
              <div className="expanded-health">
                <div><Text type="secondary">最近消息</Text><br /><Text>{record.lastMessage || '—'}</Text></div>
                <div><Text type="secondary">错误详情</Text><br /><Text type={record.lastError ? 'danger' : undefined}>{record.lastError || '—'}</Text></div>
                <div><Text type="secondary">下次检查</Text><br /><Text>{record.enabled ? `${relativeDate(record.nextCheckAt)} · ${formatDate(record.nextCheckAt)}` : '已停用'}</Text></div>
              </div>
            ),
            rowExpandable: (record) => Boolean(record.lastError || record.lastMessage || record.nextCheckAt)
          }}
          columns={[
            { title: '名称', dataIndex: 'name', fixed: 'left', width: 180 },
            { title: '类型', dataIndex: 'type', width: 150, render: (value: MonitorType) => <Tag>{pluginByID.get(value)?.name ?? value}</Tag> },
            { title: '网络', dataIndex: 'proxyId', width: 130, render: (value: number | undefined) => value ? <Tag color="blue">{proxyByID.get(value)?.name ?? `代理 #${value}`}</Tag> : <Text type="secondary">默认网络</Text> },
            { title: '启用', width: 76, render: (_, record) => <Switch size="small" checked={record.enabled} loading={toggleMutation.isPending && toggleMutation.variables?.record.id === record.id} onChange={(enabled) => toggleMutation.mutate({ record, enabled })} /> },
            { title: '状态', width: 100, render: (_, record) => <StatusTag status={!record.enabled ? 'disabled' : record.lastStatus} /> },
            { title: '连续失败', dataIndex: 'consecutiveFailures', width: 100, render: (value) => value || '—' },
            { title: '故障告警', width: 110, render: (_, record) => record.failureAlertActive ? <Tag color="error">告警中</Tag> : record.failureAlertAfter > 0 ? <Tag color="green">{record.failureAlertAfter} 次触发</Tag> : <Text type="secondary">关闭</Text> },
            { title: '检查频率', dataIndex: 'intervalSeconds', width: 110, render: formatInterval },
            { title: '上次检查', dataIndex: 'lastCheckedAt', width: 150, render: relativeDate },
            { title: '操作', width: 370, render: (_, record) => actions(record) }
          ]}
        />
      )}

      <MonitorDrawer
        open={drawerOpen}
        record={editing}
        plugins={plugins.data ?? []}
        channels={channels.data ?? []}
        proxies={proxies.data ?? []}
        saving={saveMutation.isPending}
        error={saveMutation.error as Error | null}
        onClose={() => { setDrawerOpen(false); saveMutation.reset(); }}
        onSave={(input, checkAfter) => saveMutation.mutate({ id: editing?.id, input, checkAfter })}
      />
    </Space>
  );
}

function MonitorDrawer(props: {
  open: boolean;
  record: Monitor | null;
  plugins: MonitorPlugin[];
  channels: NotifyChannel[];
  proxies: ProxyProfile[];
  saving: boolean;
  error: Error | null;
  onClose: () => void;
  onSave: (input: MonitorInput, checkAfter: boolean) => void;
}) {
  const [form] = Form.useForm();
  const [advanced, setAdvanced] = useState(false);
  const selectedType = Form.useWatch<MonitorType>('type', form);
  const intervalSeconds = Form.useWatch<number>('intervalSeconds', form);
  const failureAlertsEnabled = Form.useWatch<boolean>('failureAlertsEnabled', form);
  const plugin = props.plugins.find((item) => item.id === selectedType) ?? props.plugins[0];
  const setInitial = () => {
    const initialPlugin = props.plugins.find((item) => item.id === props.record?.type) ?? props.plugins[0];
    const config = props.record?.config ?? initialPlugin?.defaultConfig ?? {};
    setAdvanced(false);
    form.resetFields();
    form.setFieldsValue({
      name: props.record?.name ?? '',
      type: initialPlugin?.id,
      enabled: props.record?.enabled ?? true,
      intervalSeconds: props.record?.intervalSeconds ?? initialPlugin?.defaultIntervalSeconds ?? 300,
      proxyId: props.record?.proxyId,
      failureAlertsEnabled: (props.record?.failureAlertAfter ?? 0) > 0,
      failureAlertAfter: props.record?.failureAlertAfter || 3,
      failureNotifyChannelIds: props.record?.failureNotifyChannelIds ?? []
    });
    form.setFieldValue('config', config);
    form.setFieldValue('rawConfig', JSON.stringify(config, null, 2));
  };
  const submit = async (checkAfter: boolean) => {
    const values = await form.validateFields();
    const config = advanced ? parseConfigJSON(values.rawConfig) : (form.getFieldValue('config') ?? {});
    props.onSave({
      name: values.name.trim(), type: values.type, proxyId: values.proxyId ?? null, enabled: values.enabled, intervalSeconds: values.intervalSeconds, config,
      failureAlertAfter: values.failureAlertsEnabled ? values.failureAlertAfter : 0,
      failureNotifyChannelIds: values.failureAlertsEnabled ? values.failureNotifyChannelIds : []
    }, checkAfter);
  };
  return (
    <Drawer title={props.record ? '编辑监控' : '新建监控'} open={props.open} onClose={props.onClose} width={680} destroyOnClose afterOpenChange={(open) => { if (open) setInitial(); }} footer={
      <div className="drawer-footer"><Button onClick={props.onClose}>取消</Button><Space><Button loading={props.saving} onClick={() => submit(false)}>保存</Button><Button type="primary" loading={props.saving} icon={<PlayCircleOutlined />} onClick={() => submit(true)}>保存并检查</Button></Space></div>
    }>
      <PageError error={props.error} />
      <Form form={form} layout="vertical" requiredMark="optional" onValuesChange={(changed) => {
        if (changed.type && !props.record) {
          const next = props.plugins.find((item) => item.id === changed.type);
          form.setFieldValue('intervalSeconds', next?.defaultIntervalSeconds);
          form.setFieldValue('config', next?.defaultConfig ?? {});
          form.setFieldValue('rawConfig', JSON.stringify(next?.defaultConfig ?? {}, null, 2));
        }
      }}>
        {plugin?.description && <Alert className="form-intro" type="info" showIcon message={plugin.name} description={plugin.description} />}
        <Form.Item name="name" label="名称" rules={[{ required: true, whitespace: true, message: '请输入监控名称' }]}><Input placeholder="例如：产品发布动态" /></Form.Item>
        <Row gutter={16}>
          <Col xs={24} sm={12}><Form.Item name="type" label="类型" rules={[{ required: true }]} extra={props.record ? '为保护历史状态，已创建监控的类型不可修改。' : undefined}><Select disabled={Boolean(props.record)} options={props.plugins.map((item) => ({ label: item.name, value: item.id }))} /></Form.Item></Col>
          <Col xs={24} sm={12}><Form.Item label="检查间隔" extra={intervalSeconds ? `当前：${formatInterval(intervalSeconds)}` : undefined}>
            <Space.Compact block>
              <Form.Item name="intervalSeconds" noStyle rules={[{ required: true }]}><InputNumber min={30} max={2_592_000} style={{ width: '58%' }} addonAfter="秒" /></Form.Item>
              <Select placeholder="快捷选择" style={{ width: '42%' }} onChange={(value) => form.setFieldValue('intervalSeconds', value)} options={[
                { value: 60, label: '1 分钟' }, { value: 300, label: '5 分钟' }, { value: 3600, label: '1 小时' },
                { value: 21600, label: '6 小时' }, { value: 86400, label: '1 天' }, { value: 604800, label: '7 天' }
              ]} />
            </Space.Compact>
          </Form.Item></Col>
        </Row>
        <Form.Item name="enabled" label="启用监控" valuePropName="checked"><Switch /></Form.Item>
        <Card size="small" title="网络连接" className="form-intro">
          <Form.Item name="proxyId" label="出站代理" extra={props.proxies.length ? '仅当前监控的 HTTP 请求会经过所选代理；留空沿用部署环境的默认网络设置。' : '请先在“设置 → 网络代理”中添加代理；当前监控将使用默认网络设置。'}>
            <Select allowClear placeholder="默认网络（沿用环境设置）" options={props.proxies.map((item) => ({ label: `${item.name} · ${item.type.toUpperCase()} · ${item.host}:${item.port}`, value: item.id }))} />
          </Form.Item>
        </Card>
        <Card size="small" title="监控自身故障告警" className="form-intro">
          <Form.Item name="failureAlertsEnabled" label="连续检查失败时通知" valuePropName="checked" extra="同一轮故障只告警一次；恢复正常后会再发送一条恢复通知。"><Switch /></Form.Item>
          {failureAlertsEnabled && <Row gutter={16}>
            <Col xs={24} sm={8}><Form.Item name="failureAlertAfter" label="连续失败次数" rules={[{ required: true, message: '请输入触发次数' }]}><InputNumber min={1} max={100} className="full-width" /></Form.Item></Col>
            <Col xs={24} sm={16}><Form.Item name="failureNotifyChannelIds" label="通知渠道" rules={[{ required: true, type: 'array', min: 1, message: '请至少选择一个已启用通知渠道' }]}><Select mode="multiple" placeholder="选择故障与恢复通知渠道" options={props.channels.map((item) => ({ label: `${item.name}${item.enabled ? '' : '（已停用）'}`, value: item.id, disabled: !item.enabled }))} /></Form.Item></Col>
          </Row>}
        </Card>
        <ConfigMode form={form} advanced={advanced} onChange={setAdvanced} />
        {advanced ? <AdvancedConfigField /> : plugin && <ConfigFields fields={plugin.configFields} configuredSecrets={props.record?.configuredSecrets} />}
      </Form>
    </Drawer>
  );
}
