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
import { DeleteOutlined, EditOutlined, PlayCircleOutlined, PlusOutlined, SearchOutlined } from '@ant-design/icons';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from '../api';
import ConfigFields from '../components/ConfigFields';
import { EmptyState, formatDate, formatInterval, PageError, relativeDate, StatusTag } from '../components/Common';
import type { Monitor, MonitorInput, MonitorPlugin, MonitorType } from '../types';

const { Text, Title } = Typography;

export default function MonitorsPage() {
  const screens = Grid.useBreakpoint();
  const mobile = !screens.md;
  const queryClient = useQueryClient();
  const { message } = AntApp.useApp();
  const [editing, setEditing] = useState<Monitor | null>(null);
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [search, setSearch] = useState('');
  const [status, setStatus] = useState<string>('all');
  const monitors = useQuery({ queryKey: ['monitors'], queryFn: api.listMonitors });
  const plugins = useQuery({ queryKey: ['plugins'], queryFn: api.listPlugins });
  const pluginByID = useMemo(() => new Map((plugins.data ?? []).map((item) => [item.id, item])), [plugins.data]);

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
          await api.checkMonitor(item.id);
          message.success('检查完成，可在活动页面查看运行详情');
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
    onSuccess: async () => { await refresh(); message.success('检查完成'); },
    onError: async (error: Error) => { await refresh(); message.error(error.message); }
  });
  const deleteMutation = useMutation({
    mutationFn: api.deleteMonitor,
    onSuccess: async () => { await refresh(); message.success('监控已归档，历史记录仍会保留'); },
    onError: (error: Error) => message.error(error.message)
  });

  const filtered = (monitors.data ?? []).filter((item) => {
    const matchesSearch = item.name.toLowerCase().includes(search.toLowerCase()) || item.type.includes(search.toLowerCase());
    const matchesStatus = status === 'all' || (status === 'enabled' ? item.enabled : status === 'disabled' ? !item.enabled : item.lastStatus === status);
    return matchesSearch && matchesStatus;
  });

  const openNew = () => { setEditing(null); setDrawerOpen(true); };
  const actions = (record: Monitor) => (
    <Space wrap>
      <Button icon={<PlayCircleOutlined />} loading={checkMutation.isPending && checkMutation.variables === record.id} onClick={() => checkMutation.mutate(record.id)}>检查</Button>
      <Button icon={<EditOutlined />} onClick={() => { setEditing(record); setDrawerOpen(true); }}>编辑</Button>
      <Popconfirm title="归档这个监控？" description="监控将停止运行，既有检查、事件和通知历史会保留。" onConfirm={() => deleteMutation.mutate(record.id)}>
        <Button danger icon={<DeleteOutlined />} aria-label={`归档 ${record.name}`} />
      </Popconfirm>
    </Space>
  );

  return (
    <Space direction="vertical" size={16} className="full-width">
      <PageError error={(monitors.error || plugins.error) as Error | null} onRetry={() => { monitors.refetch(); plugins.refetch(); }} />
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
              <div className="entity-card-head"><div><Title level={5}>{item.name}</Title><Text type="secondary">{pluginByID.get(item.type)?.name ?? item.type}</Text></div><StatusTag status={!item.enabled ? 'disabled' : item.lastStatus} /></div>
              <div className="entity-meta"><span>检查频率 <strong>{formatInterval(item.intervalSeconds)}</strong></span><span>上次检查 <strong>{relativeDate(item.lastCheckedAt)}</strong></span></div>
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
            { title: '状态', width: 110, render: (_, record) => <StatusTag status={!record.enabled ? 'disabled' : record.lastStatus} /> },
            { title: '连续失败', dataIndex: 'consecutiveFailures', width: 100, render: (value) => value || '—' },
            { title: '检查频率', dataIndex: 'intervalSeconds', width: 110, render: formatInterval },
            { title: '上次检查', dataIndex: 'lastCheckedAt', width: 150, render: relativeDate },
            { title: '操作', width: 290, render: (_, record) => actions(record) }
          ]}
        />
      )}

      <MonitorDrawer
        open={drawerOpen}
        record={editing}
        plugins={plugins.data ?? []}
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
  saving: boolean;
  error: Error | null;
  onClose: () => void;
  onSave: (input: MonitorInput, checkAfter: boolean) => void;
}) {
  const [form] = Form.useForm();
  const selectedType = Form.useWatch<MonitorType>('type', form);
  const plugin = props.plugins.find((item) => item.id === selectedType) ?? props.plugins[0];
  const setInitial = () => {
    const initialPlugin = props.plugins.find((item) => item.id === props.record?.type) ?? props.plugins[0];
    form.setFieldsValue({
      name: props.record?.name ?? '',
      type: initialPlugin?.id,
      enabled: props.record?.enabled ?? true,
      intervalSeconds: props.record?.intervalSeconds ?? initialPlugin?.defaultIntervalSeconds ?? 300,
      config: props.record?.config ?? initialPlugin?.defaultConfig ?? {}
    });
  };
  const submit = async (checkAfter: boolean) => {
    const values = await form.validateFields();
    const activePlugin = props.plugins.find((item) => item.id === values.type);
    const knownConfig = Object.fromEntries((activePlugin?.configFields ?? []).map((field) => [field.key, values.config?.[field.key]]).filter(([, value]) => value !== undefined));
    const config = { ...(props.record?.config ?? {}), ...knownConfig };
    props.onSave({ name: values.name.trim(), type: values.type, enabled: values.enabled, intervalSeconds: values.intervalSeconds, config }, checkAfter);
  };
  return (
    <Drawer title={props.record ? '编辑监控' : '新建监控'} open={props.open} onClose={props.onClose} width={680} destroyOnClose afterOpenChange={(open) => { if (open) setInitial(); }} footer={
      <div className="drawer-footer"><Button onClick={props.onClose}>取消</Button><Space><Button loading={props.saving} onClick={() => submit(false)}>保存</Button><Button type="primary" loading={props.saving} icon={<PlayCircleOutlined />} onClick={() => submit(true)}>保存并检查</Button></Space></div>
    }>
      <PageError error={props.error} />
      <Form form={form} layout="vertical" requiredMark="optional" onValuesChange={(changed) => {
        if (changed.type && !props.record) {
          const next = props.plugins.find((item) => item.id === changed.type);
          form.setFieldsValue({ intervalSeconds: next?.defaultIntervalSeconds, config: next?.defaultConfig });
        }
      }}>
        {plugin?.description && <Alert className="form-intro" type="info" showIcon message={plugin.name} description={plugin.description} />}
        <Form.Item name="name" label="名称" rules={[{ required: true, whitespace: true, message: '请输入监控名称' }]}><Input placeholder="例如：产品发布动态" /></Form.Item>
        <Row gutter={16}>
          <Col xs={24} sm={12}><Form.Item name="type" label="类型" rules={[{ required: true }]} extra={props.record ? '为保护历史状态，已创建监控的类型不可修改。' : undefined}><Select disabled={Boolean(props.record)} options={props.plugins.map((item) => ({ label: item.name, value: item.id }))} /></Form.Item></Col>
          <Col xs={24} sm={12}><Form.Item name="intervalSeconds" label="检查间隔（秒）" rules={[{ required: true }]}><InputNumber min={30} max={2_592_000} className="full-width" /></Form.Item></Col>
        </Row>
        <Form.Item name="enabled" label="启用监控" valuePropName="checked"><Switch /></Form.Item>
        {plugin && <ConfigFields fields={plugin.configFields} configuredSecrets={props.record?.configuredSecrets} />}
      </Form>
    </Drawer>
  );
}
