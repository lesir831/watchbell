import { useState } from 'react';
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
import { DeleteOutlined, EditOutlined, LockOutlined, PlusOutlined } from '@ant-design/icons';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import type { FormInstance } from 'antd';
import { api, APIError } from '../api';
import { EmptyState, PageError, relativeDate } from '../components/Common';
import type { ChangePasswordInput, ProxyProfile, ProxyProfileInput, ProxyType } from '../types';

const { Text, Title } = Typography;

const proxyTypeLabels: Record<ProxyType, string> = {
  http: 'HTTP',
  https: 'HTTPS',
  socks5: 'SOCKS5'
};

export default function SettingsPage(props: { authEnabled: boolean; username: string }) {
  const mobile = !Grid.useBreakpoint().md;
  const queryClient = useQueryClient();
  const { message } = AntApp.useApp();
  const [passwordForm] = Form.useForm<ChangePasswordInput>();
  const [proxyForm] = Form.useForm<ProxyProfileInput>();
  const [editing, setEditing] = useState<ProxyProfile | null>(null);
  const [proxyDrawerOpen, setProxyDrawerOpen] = useState(false);
  const overview = useQuery({ queryKey: ['settings'], queryFn: api.settings });
  const proxies = useQuery({ queryKey: ['proxies'], queryFn: api.listProxies });
  const authEnabled = overview.data?.authEnabled ?? props.authEnabled;
  const username = overview.data?.username ?? props.username;

  const passwordMutation = useMutation({
    mutationFn: api.changePassword,
    onSuccess: async (_, variables) => {
      passwordForm.resetFields();
      scrubPasswordInput(variables);
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['authMe'] }),
        queryClient.invalidateQueries({ queryKey: ['auditLogs'] })
      ]);
      message.success('密码已修改，其他已登录会话已失效');
    },
    onError: (error: Error, variables) => {
      const fields = error instanceof APIError
        ? (['currentPassword', 'newPassword', 'confirmPassword'] as const).flatMap((name) => error.fields[name] ? [{ name, errors: [error.fields[name]] }] : [])
        : [];
      passwordForm.setFieldsValue({ currentPassword: '', newPassword: '', confirmPassword: '' });
      if (fields.length) {
        passwordForm.setFields(fields);
      }
      scrubPasswordInput(variables);
      message.error(error.message);
    }
  });

  const refreshProxies = async () => Promise.all([
    queryClient.invalidateQueries({ queryKey: ['proxies'] }),
    queryClient.invalidateQueries({ queryKey: ['monitors'] }),
    queryClient.invalidateQueries({ queryKey: ['auditLogs'] })
  ]);
  const saveProxy = useMutation({
    mutationFn: (payload: { id?: number; input: ProxyProfileInput }) => payload.id ? api.updateProxy(payload.id, payload.input) : api.createProxy(payload.input),
    onSuccess: async (_, variables) => {
      variables.input.password = '';
      proxyForm.resetFields();
      setProxyDrawerOpen(false);
      setEditing(null);
      await refreshProxies();
      message.success('代理配置已保存');
    },
    onError: (error: Error, variables) => {
      const names = ['name', 'type', 'host', 'port', 'username', 'password'] as const;
      const fields = error instanceof APIError ? names.flatMap((name) => error.fields[name] ? [{ name, errors: [error.fields[name]] }] : []) : [];
      proxyForm.setFieldValue('password', '');
      if (fields.length) proxyForm.setFields(fields);
      variables.input.password = '';
      message.error(error.message);
    }
  });
  const deleteProxy = useMutation({
    mutationFn: api.deleteProxy,
    onSuccess: async () => { await refreshProxies(); message.success('代理配置已归档'); },
    onError: (error: Error) => message.error(error.message)
  });

  const openNewProxy = () => { setEditing(null); setProxyDrawerOpen(true); };
  const proxyActions = (record: ProxyProfile) => (
    <Space wrap>
      <Button icon={<EditOutlined />} onClick={() => { setEditing(record); setProxyDrawerOpen(true); }}>编辑</Button>
      <Popconfirm
        title="归档这个代理？"
        description="仍被监控使用时无法归档，请先为相关监控切换为默认网络或其他代理。"
        onConfirm={() => deleteProxy.mutate(record.id)}
      >
        <Button danger icon={<DeleteOutlined />} aria-label={`归档 ${record.name}`} />
      </Popconfirm>
    </Space>
  );

  return (
    <Space direction="vertical" size={16} className="full-width">
      <PageError error={(overview.error || proxies.error) as Error | null} onRetry={() => { overview.refetch(); proxies.refetch(); }} />
      <Row gutter={[16, 16]}>
        <Col xs={24} xl={10}>
          <Card title={<Space><LockOutlined />账户安全</Space>} className="settings-card">
            {authEnabled ? (
              <>
                <Alert className="form-intro" type="info" showIcon message={`当前管理员：${username}`} description="修改后，除当前浏览器外的既有登录会话会立即失效。新密码保存在 WatchBell 数据库中，并优先于启动环境变量。" />
                <Form<ChangePasswordInput> form={passwordForm} layout="vertical" requiredMark="optional" onFinish={(values) => passwordMutation.mutate(values)}>
                  <Form.Item name="currentPassword" label="当前密码" rules={[{ required: true, message: '请输入当前密码' }]}>
                    <Input.Password autoComplete="current-password" />
                  </Form.Item>
                  <Form.Item name="newPassword" label="新密码" rules={[{ required: true, min: 8, message: '新密码至少需要 8 个字符' }]}>
                    <Input.Password autoComplete="new-password" />
                  </Form.Item>
                  <Form.Item name="confirmPassword" label="确认新密码" dependencies={['newPassword']} rules={[
                    { required: true, message: '请再次输入新密码' },
                    ({ getFieldValue }) => ({ validator: (_, value) => !value || getFieldValue('newPassword') === value ? Promise.resolve() : Promise.reject(new Error('两次输入的新密码不一致')) })
                  ]}>
                    <Input.Password autoComplete="new-password" />
                  </Form.Item>
                  <Button type="primary" htmlType="submit" loading={passwordMutation.isPending}>修改密码</Button>
                </Form>
              </>
            ) : (
              <Alert type="warning" showIcon message="身份认证已关闭" description="当前实例通过 WATCHBELL_AUTH_DISABLED 关闭了登录认证，因此不能在网页中修改密码。" />
            )}
          </Card>
        </Col>
        <Col xs={24} xl={14}>
          <Card title="网络代理" className="settings-card" extra={<Button type="primary" icon={<PlusOutlined />} onClick={openNewProxy}>添加代理</Button>}>
            <Alert className="form-intro" type="info" showIcon message="代理按监控启用" description="保存代理后，在监控编辑页选择它。未选择时使用部署环境的默认网络设置（可能包含 HTTP_PROXY / HTTPS_PROXY）；已选代理不可用时检查会失败，不会绕过它。" />
            {!proxies.data?.length && !proxies.isLoading ? (
              <EmptyState title="还没有代理配置" description="添加 HTTP、HTTPS 或 SOCKS5 代理，然后为需要的监控单独启用。" action={<Button type="primary" onClick={openNewProxy}>添加第一个代理</Button>} />
            ) : mobile ? (
              <div className="mobile-card-list">{(proxies.data ?? []).map((item) => (
                <Card key={item.id} size="small" className="entity-card">
                  <div className="entity-card-head"><div><Title level={5}>{item.name}</Title><Text type="secondary">{proxyAddress(item)}</Text></div><Tag>{proxyTypeLabels[item.type]}</Tag></div>
                  <div className="tag-row">{item.username && <Tag>用户 {item.username}</Tag>}{item.configuredSecrets?.includes('password') && <Tag color="green">密码已配置</Tag>}</div>
                  <div className="entity-actions">{proxyActions(item)}</div>
                </Card>
              ))}</div>
            ) : (
              <Table<ProxyProfile> rowKey="id" loading={proxies.isLoading} dataSource={proxies.data ?? []} pagination={false} scroll={{ x: 720 }} columns={[
                { title: '名称', dataIndex: 'name' },
                { title: '类型', dataIndex: 'type', width: 100, render: (value: ProxyType) => <Tag>{proxyTypeLabels[value]}</Tag> },
                { title: '地址', render: (_, item) => <Text code>{proxyAddress(item)}</Text> },
                { title: '认证', width: 120, render: (_, item) => item.username ? <Tag color={item.configuredSecrets?.includes('password') ? 'green' : 'default'}>{item.configuredSecrets?.includes('password') ? '用户 + 密码' : '仅用户名'}</Tag> : <Text type="secondary">无</Text> },
                { title: '最近更新', dataIndex: 'updatedAt', width: 120, render: relativeDate },
                { title: '操作', width: 180, render: (_, item) => proxyActions(item) }
              ]} />
            )}
          </Card>
        </Col>
      </Row>
      <ProxyDrawer
        open={proxyDrawerOpen}
        record={editing}
        form={proxyForm}
        saving={saveProxy.isPending}
        error={saveProxy.error as Error | null}
        onClose={() => { proxyForm.resetFields(); setProxyDrawerOpen(false); setEditing(null); saveProxy.reset(); }}
        onSave={(input) => saveProxy.mutate({ id: editing?.id, input })}
      />
    </Space>
  );
}

function ProxyDrawer(props: { open: boolean; record: ProxyProfile | null; form: FormInstance<ProxyProfileInput>; saving: boolean; error: Error | null; onClose: () => void; onSave: (input: ProxyProfileInput) => void }) {
  const form = props.form;
  const clearPassword = Form.useWatch('clearPassword', form);
  const configuredPassword = props.record?.configuredSecrets?.includes('password') === true;
  const setInitial = () => {
    form.resetFields();
    form.setFieldsValue({
      name: props.record?.name ?? '',
      type: props.record?.type ?? 'http',
      host: props.record?.host ?? '',
      port: props.record?.port ?? 8080,
      username: props.record?.username ?? '',
      password: '',
      clearPassword: false
    });
  };
  return (
    <Drawer
      title={props.record ? '编辑代理' : '添加代理'}
      open={props.open}
      onClose={props.onClose}
      width={560}
      destroyOnClose
      afterOpenChange={(open) => { if (open) setInitial(); }}
      footer={<div className="drawer-footer"><Button onClick={props.onClose}>取消</Button><Button type="primary" loading={props.saving} onClick={() => form.submit()}>保存</Button></div>}
    >
      <PageError error={props.error} />
      <Alert className="form-intro" type="info" showIcon message="出站代理" description="主机字段不要包含协议或端口。代理密码只用于建立代理连接，不会通过 API 回显。" />
      <Form<ProxyProfileInput> form={form} layout="vertical" requiredMark="optional" onFinish={props.onSave} onValuesChange={(changed) => {
        if (changed.type && !props.record) form.setFieldValue('port', changed.type === 'socks5' ? 1080 : 8080);
      }}>
        <Form.Item name="name" label="名称" rules={[{ required: true, whitespace: true, message: '请输入代理名称' }]}><Input placeholder="例如：海外出口" /></Form.Item>
        <Row gutter={16}>
          <Col xs={24} sm={10}><Form.Item name="type" label="类型" rules={[{ required: true }]}><Select options={Object.entries(proxyTypeLabels).map(([value, label]) => ({ value, label }))} /></Form.Item></Col>
          <Col xs={24} sm={14}><Form.Item name="host" label="主机" rules={[{ required: true, whitespace: true, message: '请输入代理主机' }]}><Input placeholder="proxy.example.com" /></Form.Item></Col>
        </Row>
        <Form.Item name="port" label="端口" rules={[{ required: true, message: '请输入端口' }]}><InputNumber min={1} max={65535} className="full-width" /></Form.Item>
        <Form.Item name="username" label="用户名"><Input autoComplete="off" placeholder="无认证可留空" /></Form.Item>
        <Form.Item name="password" label="密码" extra={configuredPassword && !clearPassword ? '密码已配置；留空将保持原值。' : undefined}>
          <Input.Password autoComplete="new-password" disabled={clearPassword} placeholder={configuredPassword ? '已配置，留空保持原值' : '无认证可留空'} />
        </Form.Item>
        {configuredPassword && <Form.Item name="clearPassword" label="清除已保存的密码" valuePropName="checked"><Switch /></Form.Item>}
      </Form>
    </Drawer>
  );
}

function proxyAddress(item: Pick<ProxyProfile, 'host' | 'port'>) {
  const host = item.host.includes(':') ? `[${item.host}]` : item.host;
  return `${host}:${item.port}`;
}

function scrubPasswordInput(input: ChangePasswordInput) {
  input.currentPassword = '';
  input.newPassword = '';
  input.confirmPassword = '';
}
