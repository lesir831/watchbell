import { useState } from 'react';
import {
  Alert,
  App as AntApp,
  Button,
  Drawer,
  Form,
  Input,
  Popconfirm,
  Select,
  Space,
  Switch
} from 'antd';
import { BellOutlined, CodeOutlined, DeleteOutlined, EditOutlined, MailOutlined, PlusOutlined, SendOutlined } from '@ant-design/icons';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, APIError } from '../api';
import ConfigFields from '../components/ConfigFields';
import { AdvancedConfigField, ConfigMode, parseConfigJSON } from '../components/ConfigMode';
import { EmptyState, PageError, PageHeader, relativeDate, StatusTag } from '../components/Common';
import type { ChannelType, NotifyChannel, NotifyChannelInput, PluginConfigField } from '../types';

const channelSchemas: Record<ChannelType, { name: string; description: string; fields: PluginConfigField[]; defaults: Record<string, unknown> }> = {
  bark: {
    name: 'Bark', description: '向 iPhone 或自托管 Bark Server 推送即时通知。',
    fields: [
      { key: 'serverUrl', label: '服务地址', type: 'url', description: '默认使用 https://api.day.app' },
      { key: 'deviceKey', label: '设备密钥', type: 'secret', secret: true, required: true },
      { key: 'group', label: '分组', type: 'string' }, { key: 'sound', label: '提示音', type: 'string' },
      { key: 'icon', label: '图标 URL', type: 'url' }, { key: 'url', label: '点击跳转 URL', type: 'string', description: '可显式使用跨模块变量 ${url}；该值会发送给 Bark 服务，私有或带访问凭据的 URL 请谨慎使用' }
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
      { key: 'bodyTemplate', label: '请求正文模板', type: 'textarea', description: '留空时由后端生成安全 JSON；自定义 JSON 时请用 ${json:message.subject}、${json:message.body} 对值进行 JSON 转义。' },
      { key: 'allowPrivate', label: '允许内网地址', type: 'boolean', description: '仅在连接你信任的内网/本机服务时开启；默认阻止 SSRF' }
    ],
    defaults: { url: '', method: 'POST', headers: { 'Content-Type': 'application/json' }, bodyTemplate: '', allowPrivate: false }
  }
};

export default function ChannelsPage() {
  const queryClient = useQueryClient();
  const { message } = AntApp.useApp();
  const [editing, setEditing] = useState<NotifyChannel | null>(null);
  const [drawerOpen, setDrawerOpen] = useState(false);
  const channels = useQuery({ queryKey: ['channels'], queryFn: api.listChannels, refetchInterval: 30_000 });
  const rules = useQuery({ queryKey: ['rules'], queryFn: api.listRules, refetchInterval: 30_000 });
  const attempts = useQuery({ queryKey: ['notificationAttempts', 'channels'], queryFn: () => api.listNotificationAttemptsPage({ page: 1, pageSize: 100 }), refetchInterval: 30_000 });
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
    <div className="resource-actions">
      <Button className="mini-action" icon={<SendOutlined />} loading={testMutation.isPending && testMutation.variables === record.id} onClick={() => testMutation.mutate(record.id)}>发送测试</Button>
      <Button className="mini-action" icon={<EditOutlined />} onClick={() => { setEditing(record); setDrawerOpen(true); }}>编辑</Button>
      <Popconfirm title="归档这个渠道？" description="关联会从规则和故障告警中移除；失去全部渠道的规则将一并归档。历史发送记录会保留。" onConfirm={() => deleteMutation.mutate(record.id)}><Button danger icon={<DeleteOutlined />} aria-label={`归档 ${record.name}`} /></Popconfirm>
    </div>
  );
  return (
    <div className="design-page">
      <PageHeader
        eyebrow="通知出口"
        title="通知渠道"
        description="集中管理发送端点、验证连通性，并清楚看到最后一次投递结果。"
        actions={<Button className="design-primary" type="primary" icon={<PlusOutlined />} onClick={openNew}>新建渠道</Button>}
      />
      <PageError error={(channels.error || rules.error || attempts.error) as Error | null} onRetry={() => { channels.refetch(); rules.refetch(); attempts.refetch(); }} />
      {!channels.data?.length && !channels.isLoading ? (
        <div className="empty-panel"><EmptyState title="还没有通知渠道" description="先配置 Bark 或 SMTP，并发送一次测试通知。" action={<Button type="primary" onClick={openNew}>创建第一个渠道</Button>} /></div>
      ) : (
        <div className="collection-grid">
          {(channels.data ?? []).map((item) => {
            const latest = attempts.data?.items.find((attempt) => attempt.channelId === item.id);
            const relatedRules = (rules.data ?? []).filter((rule) => rule.notifyChannelIds.includes(item.id)).length;
            return <article key={item.id} className="resource-card">
              <div className="resource-card-head">
                <div className="resource-card-title"><span className="type-mark">{channelIcon(item.type)}</span><div><h2>{item.name}</h2><p>{channelSchemas[item.type].name}</p></div></div>
                <StatusTag status={item.enabled ? 'available' : 'disabled'} />
              </div>
              <p className="resource-description">{channelSchemas[item.type].description} 敏感配置只在服务端安全保存，不会在界面回显。</p>
              <div className="resource-meta">
                <div><span>最近投递</span><strong>{relativeDate(latest?.createdAt ?? item.updatedAt)}</strong></div>
                <div><span>投递结果</span><strong>{latest ? (latest.status === 'sent' ? '已送达' : latest.status === 'failed' ? '发送失败' : latest.status) : '尚无记录'}</strong></div>
                <div><span>关联规则</span><strong>{relatedRules} 条</strong></div>
                <div><span>状态</span><strong>{item.enabled ? '已启用' : '已停用'}</strong></div>
              </div>
              {actions(item)}
            </article>;
          })}
        </div>
      )}
      <ChannelDrawer open={drawerOpen} record={editing} saving={saveMutation.isPending} error={saveMutation.error as Error | null} onClose={() => { setDrawerOpen(false); saveMutation.reset(); }} onSave={(input, testAfter) => saveMutation.mutate({ id: editing?.id, input, testAfter })} />
    </div>
  );
}

function channelIcon(type: ChannelType) {
  if (type === 'bark') return <BellOutlined />;
  if (type === 'email') return <MailOutlined />;
  return <CodeOutlined />;
}

function ChannelDrawer(props: { open: boolean; record: NotifyChannel | null; saving: boolean; error: Error | null; onClose: () => void; onSave: (input: NotifyChannelInput, testAfter: boolean) => void }) {
  const [form] = Form.useForm();
  const [advanced, setAdvanced] = useState(false);
  const selectedType = Form.useWatch<ChannelType>('type', form) ?? props.record?.type ?? 'bark';
  const schema = channelSchemas[selectedType];
  const setInitial = () => {
    const config = props.record?.config ?? channelSchemas.bark.defaults;
    setAdvanced(false);
    form.resetFields();
    form.setFieldsValue({ name: props.record?.name ?? '', type: props.record?.type ?? 'bark', enabled: props.record?.enabled ?? true });
    form.setFieldValue('config', config);
    form.setFieldValue('rawConfig', JSON.stringify(config, null, 2));
  };
  const submit = async (testAfter: boolean) => {
    const values = await form.validateFields();
    const config = advanced ? parseConfigJSON(values.rawConfig) : (form.getFieldValue('config') ?? {});
    props.onSave({ name: values.name.trim(), type: values.type, enabled: values.enabled, config }, testAfter);
  };
  return (
    <Drawer title={props.record ? '编辑通知渠道' : '新建通知渠道'} open={props.open} onClose={props.onClose} width={680} destroyOnClose afterOpenChange={(open) => { if (open) setInitial(); }} footer={<div className="drawer-footer"><Button onClick={props.onClose}>取消</Button><Space><Button loading={props.saving} onClick={() => submit(false)}>保存</Button><Button type="primary" icon={<SendOutlined />} loading={props.saving} onClick={() => submit(true)}>保存并测试</Button></Space></div>}>
      <PageError error={props.error} />
      <Form form={form} layout="vertical" requiredMark="optional" onValuesChange={(changed) => {
        if (changed.type && !props.record) {
          const config = channelSchemas[changed.type as ChannelType].defaults;
          form.setFieldValue('config', config);
          form.setFieldValue('rawConfig', JSON.stringify(config, null, 2));
        }
      }}>
        <Alert className="form-intro" type="info" showIcon message={schema.name} description={schema.description} />
        <Form.Item name="name" label="名称" rules={[{ required: true, whitespace: true }]}><Input placeholder="例如：我的 iPhone" /></Form.Item>
        <Form.Item name="type" label="渠道类型" extra={props.record ? '已创建渠道的类型不可修改。' : undefined}><Select disabled={Boolean(props.record)} options={Object.entries(channelSchemas).map(([value, item]) => ({ label: item.name, value }))} /></Form.Item>
        <Form.Item name="enabled" label="启用渠道" valuePropName="checked"><Switch /></Form.Item>
        <ConfigMode form={form} advanced={advanced} onChange={setAdvanced} />
        {advanced ? <AdvancedConfigField /> : <ConfigFields fields={schema.fields} configuredSecrets={props.record?.configuredSecrets} />}
      </Form>
    </Drawer>
  );
}
