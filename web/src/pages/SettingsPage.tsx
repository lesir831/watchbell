import { useEffect, useState } from 'react';
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
  Tag
} from 'antd';
import { ArrowRightOutlined, CheckCircleOutlined, CloseCircleOutlined, DeleteOutlined, EditOutlined, LockOutlined, PlusOutlined, ReloadOutlined, SaveOutlined } from '@ant-design/icons';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import type { FormInstance } from 'antd';
import { api, APIError } from '../api';
import { PageError, PageHeader, relativeDate } from '../components/Common';
import type { ChangePasswordInput, ProxyProfile, ProxyProfileInput, ProxyType, RuntimeSettingsInput } from '../types';

const proxyTypeLabels: Record<ProxyType, string> = {
  http: 'HTTP',
  https: 'HTTPS',
  socks5: 'SOCKS5'
};

export default function SettingsPage(props: { authEnabled: boolean; username: string; onNavigate: (page: string) => void }) {
  const queryClient = useQueryClient();
  const { message } = AntApp.useApp();
  const [passwordForm] = Form.useForm<ChangePasswordInput>();
  const [proxyForm] = Form.useForm<ProxyProfileInput>();
  const [editing, setEditing] = useState<ProxyProfile | null>(null);
  const [proxyDrawerOpen, setProxyDrawerOpen] = useState(false);
  const [passwordExpanded, setPasswordExpanded] = useState(false);
  const [runtimeSettings, setRuntimeSettings] = useState<RuntimeSettingsInput>({ sessionTimeoutHours: 8, historyRetentionDays: 90 });
  const overview = useQuery({ queryKey: ['settings'], queryFn: api.settings });
  const proxies = useQuery({ queryKey: ['proxies'], queryFn: api.listProxies });
  const authEnabled = overview.data?.authEnabled ?? props.authEnabled;
  const username = overview.data?.username ?? props.username;

  useEffect(() => {
    if (!overview.data) return;
    setRuntimeSettings({
      sessionTimeoutHours: overview.data.sessionTimeoutHours,
      historyRetentionDays: overview.data.historyRetentionDays
    });
  }, [overview.data]);

  const saveRuntimeSettings = useMutation({
    mutationFn: api.updateRuntimeSettings,
    onSuccess: async (saved) => {
      queryClient.setQueryData(['settings'], saved);
      setRuntimeSettings({ sessionTimeoutHours: saved.sessionTimeoutHours, historyRetentionDays: saved.historyRetentionDays });
      await queryClient.invalidateQueries({ queryKey: ['auditLogs'] });
      message.success('运行与安全设置已保存');
    },
    onError: (error: Error) => message.error(error.message)
  });
  const networkCheck = useMutation({
    mutationFn: api.networkCheck,
    onSuccess: async (report) => {
      await queryClient.invalidateQueries({ queryKey: ['auditLogs'] });
      if (report.status === 'ok') message.success('DNS 与 HTTPS 连接均正常');
      else message.warning('网络自检发现异常，请查看检查结果');
    },
    onError: (error: Error) => message.error(error.message)
  });

  const passwordMutation = useMutation({
    mutationFn: api.changePassword,
    onSuccess: async (_, variables) => {
      passwordForm.resetFields();
      setPasswordExpanded(false);
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
    <div className="design-page">
      <PageHeader
        eyebrow="实例配置"
        title="设置"
        description="管理当前私有实例的调度、数据保留、安全会话与出站网络。"
        actions={<Space><Button icon={<ReloadOutlined />} onClick={() => { overview.refetch(); proxies.refetch(); }}>刷新</Button><Button className="design-dark" icon={<SaveOutlined />} loading={saveRuntimeSettings.isPending} disabled={!validRuntimeSettings(runtimeSettings, overview.data)} onClick={() => saveRuntimeSettings.mutate(runtimeSettings)}>保存设置</Button></Space>}
      />
      <PageError error={(overview.error || proxies.error) as Error | null} onRetry={() => { overview.refetch(); proxies.refetch(); }} />
      <div className="settings-grid">
        <div className="settings-stack">
          <section className="settings-panel">
            <header><h2>运行与保留</h2><p>运行策略由当前部署环境与内置队列共同管理。</p></header>
            <div className="setting-row"><div className="setting-copy"><strong>时区</strong><span>活动时间、摘要周期和计划任务使用浏览器与部署环境时区显示。</span></div><span className="setting-value">{Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC'}</span></div>
            <div className="setting-row"><div className="setting-copy"><strong>活动保留期</strong><span>超过期限的检查快照、事件、投递记录和审计日志会自动清理。</span></div><Select className="settings-select" aria-label="活动保留期" value={runtimeSettings.historyRetentionDays || undefined} placeholder="选择保留期" options={historyRetentionOptions(runtimeSettings.historyRetentionDays)} onChange={(historyRetentionDays) => setRuntimeSettings((current) => ({ ...current, historyRetentionDays }))} /></div>
            <div className="setting-row"><div className="setting-copy"><strong>失败通知自动重试</strong><span>发送队列使用退避策略重新投递，达到上限后进入死信队列。</span></div><Switch checked disabled aria-label="失败通知自动重试已启用" /></div>
          </section>

          <section className="settings-panel">
            <header className="settings-panel-action"><div><h2>出站网络</h2><p>监控可单独选择代理；未选择时使用部署环境网络。</p></div><Button icon={<PlusOutlined />} onClick={openNewProxy}>添加代理</Button></header>
            <div className="setting-row"><div className="setting-copy"><strong>默认网络</strong><span>未指定代理的监控沿用 HTTP_PROXY / HTTPS_PROXY 等部署设置。</span></div><span className="status-chip standalone"><span className="status-dot" />可用</span></div>
            <div className="setting-row"><div className="setting-copy"><strong>网络自检</strong><span>使用实例的默认出站网络测试 example.com 的 DNS 解析与 HTTPS 连接。</span></div><Button icon={<ReloadOutlined />} loading={networkCheck.isPending} onClick={() => networkCheck.mutate()}>开始检查</Button></div>
            {networkCheck.data && <div className={`network-check-report ${networkCheck.data.status}`}>
              {networkCheck.data.checks.map((check) => <div key={check.name} className={check.status}><span>{check.status === 'ok' ? <CheckCircleOutlined /> : <CloseCircleOutlined />}{check.name}</span><strong>{check.detail}</strong><small>{check.durationMs} ms</small></div>)}
            </div>}
            {(proxies.data ?? []).map((item) => <div key={item.id} className="setting-row proxy-setting-row">
              <div className="setting-copy"><strong>{item.name}</strong><span>{proxyTypeLabels[item.type]} · {proxyAddress(item)} · 更新于 {relativeDate(item.updatedAt)}</span></div>
              <div className="setting-actions"><Tag>{proxyTypeLabels[item.type]}</Tag>{proxyActions(item)}</div>
            </div>)}
            {!proxies.data?.length && !proxies.isLoading && <div className="setting-empty">尚未配置代理；所有监控使用部署环境的默认网络。</div>}
          </section>
        </div>

        <div className="settings-stack">
          <section className="settings-panel">
            <header><h2>账户与安全</h2><p>当前实例为单用户模式。</p></header>
            <div className="setting-row"><div className="setting-copy"><strong>管理员账户</strong><span>{username || 'admin'} · 当前会话已签名</span></div><Button icon={<LockOutlined />} disabled={!authEnabled} onClick={() => setPasswordExpanded((value) => !value)}>{passwordExpanded ? '收起' : '修改密码'}</Button></div>
            <div className="setting-row"><div className="setting-copy"><strong>闲置会话过期</strong><span>每次已认证操作会刷新闲置计时；超时后需要重新登录。</span></div><Select className="settings-select" aria-label="闲置会话过期时间" disabled={!authEnabled} value={runtimeSettings.sessionTimeoutHours || undefined} placeholder="选择过期时间" options={sessionTimeoutOptions(runtimeSettings.sessionTimeoutHours)} onChange={(sessionTimeoutHours) => setRuntimeSettings((current) => ({ ...current, sessionTimeoutHours }))} /></div>
            {authEnabled ? (
              passwordExpanded && <div className="settings-form">
                <Alert className="form-intro" type="info" showIcon message={`当前管理员：${username}`} description="修改后，除当前浏览器外的既有登录会话会立即失效。" />
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
              </div>
            ) : (
              <Alert type="warning" showIcon message="身份认证已关闭" description="当前实例通过 WATCHBELL_AUTH_DISABLED 关闭了登录认证，因此不能在网页中修改密码。" />
            )}
          </section>

          <section className="settings-panel">
            <header><h2>配置迁移</h2><p>配置备份、诊断包和导入报告集中在活动与诊断页面。</p></header>
            <div className="setting-row"><div className="setting-copy"><strong>导出或导入配置</strong><span>支持脱敏备份、完整备份和合并导入，不会删除现有数据。</span></div><Button onClick={() => props.onNavigate('activity')}>打开诊断<ArrowRightOutlined /></Button></div>
          </section>

          <section className="settings-panel danger-zone">
            <header><h2>安全边界</h2><p>敏感字段不会通过 API 回显。</p></header>
            <div className="setting-row"><div className="setting-copy"><strong>密钥与密码</strong><span>设备密钥、SMTP 密码、代理密码和签名密钥只保存在服务器端。</span></div><LockOutlined className="setting-lock" /></div>
          </section>
        </div>
      </div>
      <ProxyDrawer
        open={proxyDrawerOpen}
        record={editing}
        form={proxyForm}
        saving={saveProxy.isPending}
        error={saveProxy.error as Error | null}
        onClose={() => { proxyForm.resetFields(); setProxyDrawerOpen(false); setEditing(null); saveProxy.reset(); }}
        onSave={(input) => saveProxy.mutate({ id: editing?.id, input })}
      />
    </div>
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

function sessionTimeoutOptions(current: number) {
  const options = [
    { value: 1, label: '1 小时' },
    { value: 8, label: '8 小时' },
    { value: 24, label: '24 小时' }
  ];
  if (current > 0 && !options.some((option) => option.value === current)) {
    options.push({ value: current, label: `${current} 小时（部署值）` });
  }
  return options;
}

function historyRetentionOptions(current: number) {
  const options = [
    { value: 30, label: '30 天' },
    { value: 90, label: '90 天' },
    { value: 180, label: '180 天' }
  ];
  if (current > 0 && !options.some((option) => option.value === current)) {
    options.push({ value: current, label: `${current} 天（部署值）` });
  }
  return options;
}

function validRuntimeSettings(settings: RuntimeSettingsInput, current?: RuntimeSettingsInput) {
  const sessionValid = [1, 8, 24].includes(settings.sessionTimeoutHours) || settings.sessionTimeoutHours === current?.sessionTimeoutHours;
  const historyValid = [30, 90, 180].includes(settings.historyRetentionDays) || settings.historyRetentionDays === current?.historyRetentionDays;
  return sessionValid && historyValid;
}

function scrubPasswordInput(input: ChangePasswordInput) {
  input.currentPassword = '';
  input.newPassword = '';
  input.confirmPassword = '';
}
