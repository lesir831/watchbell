import { useState } from 'react';
import {
  Alert,
  App as AntApp,
  Button,
  Card,
  Drawer,
  Form,
  Grid,
  Input,
  Popconfirm,
  Select,
  Space,
  Switch,
  Table,
  Tag,
  Typography
} from 'antd';
import { DeleteOutlined, EditOutlined, PlusOutlined, SendOutlined } from '@ant-design/icons';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, APIError } from '../api';
import ConfigFields from '../components/ConfigFields';
import { AdvancedConfigField, ConfigMode, parseConfigJSON } from '../components/ConfigMode';
import { EmptyState, PageError, relativeDate, StatusTag } from '../components/Common';
import type { ChannelType, NotifyChannel, NotifyChannelInput, PluginConfigField } from '../types';

const { Text, Title } = Typography;

const channelSchemas: Record<ChannelType, { name: string; description: string; fields: PluginConfigField[]; defaults: Record<string, unknown> }> = {
  bark: {
    name: 'Bark', description: '向 iPhone 或自托管 Bark Server 推送即时通知。',
    fields: [
      { key: 'serverUrl', label: '服务地址', type: 'url', description: '默认使用 https://api.day.app' },
      { key: 'deviceKey', label: '设备密钥', type: 'secret', secret: true, required: true },
      { key: 'group', label: '分组', type: 'string' }, { key: 'sound', label: '提示音', type: 'string' },
      { key: 'icon', label: '图标 URL', type: 'url' }, { key: 'url', label: '点击跳转 URL', type: 'string', description: '支持 ${rss.link}、${github.release.url} 等模板变量' }
    ],
    defaults: { serverUrl: 'https://api.day.app', deviceKey: '', group: 'WatchBell', sound: '', icon: '', url: '' }
  },
  email: {
    name: '邮件', description: '通过 SMTP 发送纯文本邮件，支持 STARTTLS 和隐式 TLS。',
    fields: [
      { key: 'host', label: 'SMTP 主机', type: 'string', required: true }, { key: 'port', label: '端口', type: 'number', required: true },
      { key: 'username', label: '用户名', type: 'string' }, { key: 'password', label: '密码', type: 'secret', secret: true },
      { key: 'from', label: '发件人', type: 'string', required: true }, { key: 'to', label: '收件人', type: 'string-list', required: true },
      { key: 'startTls', label: '启用 STARTTLS', type: 'boolean' }, { key: 'implicitTls', label: '启用隐式 TLS', type: 'boolean' }
    ],
    defaults: { host: '', port: 587, username: '', password: '', from: '', to: [], startTls: true, implicitTls: false }
  },
  webhook: {
    name: 'Webhook', description: '向任意 HTTP 服务发送模板化请求，可用于 Telegram、Discord、ntfy、飞书、企业微信等集成。',
    fields: [
      { key: 'url', label: '请求地址', type: 'secret', secret: true, required: true, description: '支持 ${...} 模板变量；路径或查询中可能含 Token，因此不会回显' },
      { key: 'method', label: 'HTTP 方法', type: 'string', description: 'POST、PUT、PATCH、DELETE 或 GET' },
      { key: 'headers', label: '请求头', type: 'json', secret: true, description: 'JSON 对象；Authorization 等敏感值不会回显' },
      { key: 'bodyTemplate', label: '请求正文模板', type: 'textarea', description: '支持 ${message.subject}、${message.body} 和事件变量' },
      { key: 'allowPrivate', label: '允许内网地址', type: 'boolean', description: '仅在连接你信任的内网/本机服务时开启；默认阻止 SSRF' }
    ],
    defaults: { url: '', method: 'POST', headers: { 'Content-Type': 'application/json' }, bodyTemplate: '{\n  "title": "${message.subject}",\n  "body": "${message.body}",\n  "event": "${event.type}"\n}', allowPrivate: false }
  }
};

export default function ChannelsPage() {
  const mobile = !Grid.useBreakpoint().md;
  const queryClient = useQueryClient();
  const { message } = AntApp.useApp();
  const [editing, setEditing] = useState<NotifyChannel | null>(null);
  const [drawerOpen, setDrawerOpen] = useState(false);
  const channels = useQuery({ queryKey: ['channels'], queryFn: api.listChannels, refetchInterval: 30_000 });
  const refresh = async () => Promise.all([
    queryClient.invalidateQueries({ queryKey: ['channels'] }), queryClient.invalidateQueries({ queryKey: ['dashboard'] }),
    queryClient.invalidateQueries({ queryKey: ['notificationAttempts'] }), queryClient.invalidateQueries({ queryKey: ['auditLogs'] })
  ]);
  const saveMutation = useMutation({
    mutationFn: async (payload: { id?: number; input: NotifyChannelInput; testAfter: boolean }) => {
      const item = payload.id ? await api.updateChannel(payload.id, payload.input) : await api.createChannel(payload.input);
      return { item, testAfter: payload.testAfter };
    },
    onSuccess: async ({ item, testAfter }) => {
      setDrawerOpen(false); setEditing(null); await refresh(); message.success('通知渠道已保存');
      if (testAfter) {
        try { await api.testChannel(item.id); message.success('测试通知已发送'); } catch (error) { message.error((error as Error).message); } finally { await refresh(); }
      }
    }
  });
  const testMutation = useMutation({
    mutationFn: api.testChannel,
    onSuccess: async () => { await refresh(); message.success('测试通知已发送，结果已记录'); },
    onError: async (error: APIError) => { await refresh(); message.error(error.message); }
  });
  const deleteMutation = useMutation({
    mutationFn: api.deleteChannel,
    onSuccess: async () => { await refresh(); message.success('渠道已归档，历史发送记录仍会保留'); },
    onError: (error: Error) => message.error(error.message)
  });
  const openNew = () => { setEditing(null); setDrawerOpen(true); };
  const actions = (record: NotifyChannel) => (
    <Space wrap>
      <Button icon={<SendOutlined />} loading={testMutation.isPending && testMutation.variables === record.id} onClick={() => testMutation.mutate(record.id)}>测试</Button>
      <Button icon={<EditOutlined />} onClick={() => { setEditing(record); setDrawerOpen(true); }}>编辑</Button>
      <Popconfirm title="归档这个渠道？" description="关联会从规则和故障告警中移除；失去全部渠道的规则将一并归档。历史发送记录会保留。" onConfirm={() => deleteMutation.mutate(record.id)}><Button danger icon={<DeleteOutlined />} aria-label={`归档 ${record.name}`} /></Popconfirm>
    </Space>
  );
  return (
    <Space direction="vertical" size={16} className="full-width">
      <PageError error={channels.error as Error | null} onRetry={() => channels.refetch()} />
      <div className="page-toolbar"><Button type="primary" icon={<PlusOutlined />} onClick={openNew}>新建渠道</Button></div>
      {!channels.data?.length && !channels.isLoading ? (
        <Card><EmptyState title="还没有通知渠道" description="先配置 Bark 或 SMTP，并发送一次测试通知。" action={<Button type="primary" onClick={openNew}>创建第一个渠道</Button>} /></Card>
      ) : mobile ? (
        <div className="mobile-card-list">{(channels.data ?? []).map((item) => (
          <Card key={item.id} className="entity-card">
            <div className="entity-card-head"><div><Title level={5}>{item.name}</Title><Text type="secondary">{channelSchemas[item.type].name}</Text></div><StatusTag status={item.enabled ? 'ok' : 'disabled'} /></div>
            <Text type="secondary">更新于 {relativeDate(item.updatedAt)}</Text>
            <div className="entity-actions">{actions(item)}</div>
          </Card>
        ))}</div>
      ) : (
        <Table<NotifyChannel> rowKey="id" loading={channels.isLoading} dataSource={channels.data ?? []} scroll={{ x: 760 }} columns={[
          { title: '名称', dataIndex: 'name' },
          { title: '类型', dataIndex: 'type', render: (value: ChannelType) => <Tag>{channelSchemas[value].name}</Tag> },
          { title: '状态', dataIndex: 'enabled', render: (value) => <StatusTag status={value ? 'ok' : 'disabled'} /> },
          { title: '敏感配置', dataIndex: 'configuredSecrets', render: (values: string[] | undefined) => values?.length ? <Tag color="green">已安全配置</Tag> : <Tag>未配置</Tag> },
          { title: '最近更新', dataIndex: 'updatedAt', render: relativeDate },
          { title: '操作', width: 280, render: (_, record) => actions(record) }
        ]} />
      )}
      <ChannelDrawer open={drawerOpen} record={editing} saving={saveMutation.isPending} error={saveMutation.error as Error | null} onClose={() => { setDrawerOpen(false); saveMutation.reset(); }} onSave={(input, testAfter) => saveMutation.mutate({ id: editing?.id, input, testAfter })} />
    </Space>
  );
}

function ChannelDrawer(props: { open: boolean; record: NotifyChannel | null; saving: boolean; error: Error | null; onClose: () => void; onSave: (input: NotifyChannelInput, testAfter: boolean) => void }) {
  const [form] = Form.useForm();
  const [advanced, setAdvanced] = useState(false);
  const selectedType = Form.useWatch<ChannelType>('type', form) ?? props.record?.type ?? 'bark';
  const schema = channelSchemas[selectedType];
  const setInitial = () => {
    const config = props.record?.config ?? channelSchemas.bark.defaults;
    setAdvanced(false);
    form.setFieldsValue({ name: props.record?.name ?? '', type: props.record?.type ?? 'bark', enabled: props.record?.enabled ?? true, config, rawConfig: JSON.stringify(config, null, 2) });
  };
  const submit = async (testAfter: boolean) => {
    const values = await form.validateFields();
    const knownConfig = Object.fromEntries(schema.fields.map((field) => [field.key, values.config?.[field.key]]).filter(([, value]) => value !== undefined));
    const config = advanced ? parseConfigJSON(values.rawConfig) : { ...(props.record?.config ?? {}), ...knownConfig };
    props.onSave({ name: values.name.trim(), type: values.type, enabled: values.enabled, config }, testAfter);
  };
  return (
    <Drawer title={props.record ? '编辑通知渠道' : '新建通知渠道'} open={props.open} onClose={props.onClose} width={680} destroyOnClose afterOpenChange={(open) => { if (open) setInitial(); }} footer={<div className="drawer-footer"><Button onClick={props.onClose}>取消</Button><Space><Button loading={props.saving} onClick={() => submit(false)}>保存</Button><Button type="primary" icon={<SendOutlined />} loading={props.saving} onClick={() => submit(true)}>保存并测试</Button></Space></div>}>
      <PageError error={props.error} />
      <Form form={form} layout="vertical" requiredMark="optional" onValuesChange={(changed) => {
        if (changed.type && !props.record) {
          const config = channelSchemas[changed.type as ChannelType].defaults;
          form.setFieldsValue({ config, rawConfig: JSON.stringify(config, null, 2) });
        }
      }}>
        <Alert className="form-intro" type="info" showIcon message={schema.name} description={schema.description} />
        <Form.Item name="name" label="名称" rules={[{ required: true, whitespace: true }]}><Input placeholder="例如：我的 iPhone" /></Form.Item>
        <Form.Item name="type" label="渠道类型" extra={props.record ? '已创建渠道的类型不可修改。' : undefined}><Select disabled={Boolean(props.record)} options={Object.entries(channelSchemas).map(([value, item]) => ({ label: item.name, value }))} /></Form.Item>
        <Form.Item name="enabled" label="启用渠道" valuePropName="checked"><Switch /></Form.Item>
        <ConfigMode form={form} advanced={advanced} onChange={setAdvanced} baseConfig={props.record?.config} />
        {advanced ? <AdvancedConfigField /> : <ConfigFields fields={schema.fields} configuredSecrets={props.record?.configuredSecrets} />}
      </Form>
    </Drawer>
  );
}
