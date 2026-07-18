import { useEffect, useMemo, useState } from 'react';
import {
  Alert,
  App as AntApp,
  Button,
  Card,
  Col,
  Drawer,
  Form,
  Input,
  InputNumber,
  Popconfirm,
  Row,
  Select,
  Space,
  Switch
} from 'antd';
import { ArrowRightOutlined, DeleteOutlined, EditOutlined, EyeOutlined, GithubOutlined, GlobalOutlined, PlayCircleOutlined, PlusOutlined, RadarChartOutlined, RocketOutlined, SearchOutlined } from '@ant-design/icons';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from '../api';
import ConfigFields from '../components/ConfigFields';
import { AdvancedConfigField, ConfigMode, parseConfigJSON } from '../components/ConfigMode';
import { EmptyState, formatInterval, PageError, PageHeader, relativeDate, StatusTag } from '../components/Common';
import type { Monitor, MonitorInput, MonitorPlugin, MonitorType, NotifyChannel, ProxyProfile } from '../types';

export default function MonitorsPage({ onNavigate, createRequest = 0 }: { onNavigate: (page: string) => void; createRequest?: number }) {
  const queryClient = useQueryClient();
  const { message } = AntApp.useApp();
  const [editing, setEditing] = useState<Monitor | null>(null);
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [search, setSearch] = useState('');
  const [status, setStatus] = useState<string>('all');

  useEffect(() => {
    if (createRequest > 0) {
      setEditing(null);
      setDrawerOpen(true);
    }
  }, [createRequest]);

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
  const filtered = (monitors.data ?? []).filter((item) => {
    const matchesSearch = item.name.toLowerCase().includes(search.toLowerCase()) || item.type.includes(search.toLowerCase()) || (item.proxyId ? proxyByID.get(item.proxyId)?.name.toLowerCase().includes(search.toLowerCase()) === true : false);
    const state = !item.enabled ? 'disabled' : ['error', 'warning'].includes(item.lastStatus ?? '') ? 'attention' : 'healthy';
    const matchesStatus = status === 'all' || status === state;
    return matchesSearch && matchesStatus;
  });

  const openNew = () => { setEditing(null); setDrawerOpen(true); };
  const actions = (record: Monitor) => (
    <div className="row-actions">
      <Button className="mini-action" icon={<PlayCircleOutlined />} loading={checkMutation.isPending && checkMutation.variables === record.id} onClick={() => checkMutation.mutate(record.id)}>检查</Button>
      <Button className="mini-action" icon={<EyeOutlined />} onClick={() => onNavigate(`monitors/${record.id}`)}>详情<ArrowRightOutlined /></Button>
      <Button className="mini-action icon-only" icon={<EditOutlined />} aria-label={`编辑 ${record.name}`} onClick={() => { setEditing(record); setDrawerOpen(true); }} />
      <Popconfirm title="归档这个监控？" description="监控及其规则将停止运行，既有检查、事件和通知历史会保留。" onConfirm={() => deleteMutation.mutate(record.id)}>
        <Button className="mini-action icon-only" danger icon={<DeleteOutlined />} aria-label={`归档 ${record.name}`} />
      </Popconfirm>
    </div>
  );

  return (
    <div className="design-page">
      <PageHeader
        eyebrow="监控资产"
        title="监控"
        description="判断哪些信号正常、哪些需要处理，再进入单项详情追踪检查、事件与通知链路。"
        actions={<Button className="design-primary" type="primary" icon={<PlusOutlined />} onClick={openNew}>新建监控</Button>}
      />
      <PageError error={(monitors.error || plugins.error || channels.error || proxies.error) as Error | null} onRetry={() => { monitors.refetch(); plugins.refetch(); channels.refetch(); proxies.refetch(); }} />
      <div className="design-toolbar">
        <div className="filter-group" role="group" aria-label="监控状态筛选">
          {[['all', '全部'], ['healthy', '正常'], ['attention', '需关注'], ['disabled', '已停用']].map(([value, label]) => (
            <button key={value} type="button" className={`filter-button ${status === value ? 'active' : ''}`} onClick={() => setStatus(value)}>{label}</button>
          ))}
        </div>
        <label className="search-box"><SearchOutlined /><span className="sr-only">搜索监控</span><input type="search" placeholder="搜索名称或类型" value={search} onChange={(event) => setSearch(event.target.value)} /></label>
      </div>

      {filtered.length === 0 && !monitors.isLoading ? (
        <div className="empty-panel"><EmptyState title={monitors.data?.length ? '没有符合筛选条件的监控' : '还没有监控'} description="选择 RSS、网页、TestFlight 或 GitHub Release 开始监听变化。" action={!monitors.data?.length ? <Button type="primary" onClick={openNew}>创建第一个监控</Button> : undefined} /></div>
      ) : (
        <>
        <div className="design-table-wrap monitor-table-desktop">
          <table className="design-data-table">
            <thead><tr><th>监控</th><th>状态</th><th>检查频率</th><th>最近检查</th><th className="align-right">操作</th></tr></thead>
            <tbody>{filtered.map((item) => (
              <tr key={item.id}>
                <td><div className="entity-name"><span className="type-mark">{monitorTypeIcon(item.type)}</span><span className="name-stack"><strong>{item.name}</strong><span>{pluginByID.get(item.type)?.name ?? item.type}</span></span></div></td>
                <td><div className="status-stack"><StatusTag status={!item.enabled ? 'disabled' : item.lastStatus} />{item.failureAlertActive && <span className="inline-warning">故障告警中</span>}</div></td>
                <td className="number">每 {formatInterval(item.intervalSeconds)}</td>
                <td><span className="number">{relativeDate(item.lastCheckedAt)}</span><small>{item.lastError || item.lastMessage || (item.enabled ? `下次 ${relativeDate(item.nextCheckAt)}` : '手动停用')}</small></td>
                <td>{actions(item)}</td>
              </tr>
            ))}</tbody>
          </table>
        </div>
        <div className="monitor-cards-mobile">
          {filtered.map((item) => (
            <article key={item.id} className="mobile-entity-card">
              <div className="mobile-card-head"><div className="entity-name"><span className="type-mark">{monitorTypeIcon(item.type)}</span><span className="name-stack"><strong>{item.name}</strong><span>{pluginByID.get(item.type)?.name ?? item.type}</span></span></div><StatusTag status={!item.enabled ? 'disabled' : item.lastStatus} /></div>
              <div className="mobile-card-meta"><div><span>检查频率</span><strong>每 {formatInterval(item.intervalSeconds)}</strong></div><div><span>最近检查</span><strong>{relativeDate(item.lastCheckedAt)}</strong></div><div><span>网络</span><strong>{item.proxyId ? proxyByID.get(item.proxyId)?.name ?? `代理 #${item.proxyId}` : '默认网络'}</strong></div><div><span>连续失败</span><strong>{item.consecutiveFailures || '—'}</strong></div></div>
              {(item.lastError || item.lastMessage) && <Alert type={item.lastError ? 'error' : 'info'} showIcon message={item.lastError || item.lastMessage} />}
              {actions(item)}
            </article>
          ))}
        </div>
        </>
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
    </div>
  );
}

function monitorTypeIcon(type: MonitorType) {
  if (type === 'github_release') return <GithubOutlined />;
  if (type === 'rss') return <RadarChartOutlined />;
  if (type === 'webpage') return <GlobalOutlined />;
  return <RocketOutlined />;
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
              <Form.Item name="intervalSeconds" noStyle rules={[{ required: true }]}><InputNumber min={30} max={2_592_000} style={{ width: '58%' }} suffix="秒" /></Form.Item>
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
