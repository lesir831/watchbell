import { useMemo, useState } from 'react';
import {
  App as AntApp,
  Button,
  Card,
  Col,
  Descriptions,
  Drawer,
  Form,
  Input,
  InputNumber,
  Layout,
  Menu,
  Modal,
  Popconfirm,
  Row,
  Select,
  Spin,
  Space,
  Statistic,
  Switch,
  Table,
  Tabs,
  Tag,
  Typography
} from 'antd';
import type { ColumnsType } from 'antd/es/table';
import {
  BellOutlined,
  DashboardOutlined,
  FileTextOutlined,
  LockOutlined,
  LogoutOutlined,
  MailOutlined,
  PlayCircleOutlined,
  PlusOutlined,
  ReloadOutlined,
  SettingOutlined,
  UnorderedListOutlined
} from '@ant-design/icons';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, APIError } from './api';
import type {
  ChannelType,
  EventRecord,
  Monitor,
  MonitorInput,
  MonitorPlugin,
  MonitorType,
  NotificationLog,
  NotificationTemplate,
  NotificationTemplateInput,
  NotifyChannel,
  NotifyChannelInput,
  Rule,
  RuleInput
} from './types';

type PageKey = 'dashboard' | 'monitors' | 'rules' | 'channels' | 'templates' | 'logs';

const { Header, Sider, Content } = Layout;
const { Text, Title } = Typography;

const fallbackMonitorPlugins: MonitorPlugin[] = [
  fallbackPlugin('rss', 'RSS / Atom', 300, {
    url: 'https://example.com/feed.xml', timeoutSeconds: 15, notifyExisting: false, includeFullText: false
  }),
  fallbackPlugin('testflight', 'TestFlight', 60, {
    url: 'https://testflight.apple.com/join/example', timeoutSeconds: 15
  }),
  fallbackPlugin('webpage', 'Webpage', 300, {
    url: 'https://example.com', selector: '', timeoutSeconds: 15, ignorePatterns: []
  }),
  fallbackPlugin('github_release', 'GitHub Releases', 300, {
    repository: 'owner/repository',
    token: '',
    apiUrl: 'https://api.github.com',
    apiVersion: '2026-03-10',
    timeoutSeconds: 15,
    maxReleases: 20,
    includePrereleases: false,
    notifyExisting: false
  })
];

const channelTypeOptions = [
  { label: 'Bark', value: 'bark' },
  { label: 'Email', value: 'email' }
];

export default function App() {
  return <AuthGate />;
}

function AuthGate() {
  const status = useQuery({ queryKey: ['authStatus'], queryFn: api.authStatus, retry: false });
  const me = useQuery({
    queryKey: ['authMe'],
    queryFn: api.me,
    enabled: status.data?.enabled === true,
    retry: false
  });

  if (status.isLoading || (status.data?.enabled && me.isLoading)) {
    return (
      <div className="center-screen">
        <Spin />
      </div>
    );
  }

  if (status.isError) {
    return (
      <div className="center-screen">
        <Card>
          <Text type="danger">{(status.error as Error).message}</Text>
        </Card>
      </div>
    );
  }

  if (status.data?.enabled && me.isError) {
    const error = me.error as APIError;
    if (error.status === 401) {
      return <LoginPage username={status.data.username} />;
    }
    return (
      <div className="center-screen">
        <Card>
          <Text type="danger">{error.message}</Text>
        </Card>
      </div>
    );
  }

  return <Shell authEnabled={status.data?.enabled === true} username={me.data?.username ?? status.data?.username ?? ''} />;
}

function LoginPage(props: { username: string }) {
  const queryClient = useQueryClient();
  const { message } = AntApp.useApp();
  const login = useMutation({
    mutationFn: api.login,
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['authMe'] });
      message.success('Signed in');
    },
    onError: (error: Error) => message.error(error.message)
  });
  return (
    <div className="login-screen">
      <Card className="login-card">
        <div className="login-brand">
          <BellOutlined />
          <span>WatchBell</span>
        </div>
        <Form
          layout="vertical"
          initialValues={{ username: props.username }}
          onFinish={(values) => login.mutate({ username: values.username, password: values.password })}
        >
          <Form.Item name="username" label="Username" rules={[{ required: true }]}>
            <Input prefix={<LockOutlined />} autoComplete="username" />
          </Form.Item>
          <Form.Item name="password" label="Password" rules={[{ required: true }]}>
            <Input.Password autoComplete="current-password" />
          </Form.Item>
          <Button type="primary" htmlType="submit" loading={login.isPending} block>
            Sign In
          </Button>
        </Form>
      </Card>
    </div>
  );
}

function Shell(props: { authEnabled: boolean; username: string }) {
  const [page, setPage] = useState<PageKey>('dashboard');
  const queryClient = useQueryClient();
  const logout = useMutation({
    mutationFn: api.logout,
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['authMe'] });
    }
  });
  return (
    <Layout className="app-shell">
      <Sider className="app-sider" width={220}>
        <div className="brand">
          <BellOutlined />
          <span>WatchBell</span>
        </div>
        <Menu
          theme="dark"
          mode="inline"
          selectedKeys={[page]}
          onClick={({ key }) => setPage(key as PageKey)}
          items={[
            { key: 'dashboard', icon: <DashboardOutlined />, label: 'Dashboard' },
            { key: 'monitors', icon: <ReloadOutlined />, label: 'Monitors' },
            { key: 'rules', icon: <SettingOutlined />, label: 'Rules' },
            { key: 'channels', icon: <MailOutlined />, label: 'Channels' },
            { key: 'templates', icon: <FileTextOutlined />, label: 'Templates' },
            { key: 'logs', icon: <UnorderedListOutlined />, label: 'Logs' }
          ]}
        />
      </Sider>
      <Layout>
        <Header className="app-header">
          <Title level={4}>{titleForPage(page)}</Title>
          <div className="header-actions">
            {props.authEnabled && <Text type="secondary">{props.username}</Text>}
            {props.authEnabled && (
              <Button icon={<LogoutOutlined />} onClick={() => logout.mutate()}>
                Logout
              </Button>
            )}
          </div>
        </Header>
        <Content className="app-content">
          {page === 'dashboard' && <Dashboard />}
          {page === 'monitors' && <MonitorsPage />}
          {page === 'rules' && <RulesPage />}
          {page === 'channels' && <ChannelsPage />}
          {page === 'templates' && <TemplatesPage />}
          {page === 'logs' && <LogsPage />}
        </Content>
      </Layout>
    </Layout>
  );
}

function Dashboard() {
  const monitors = useQuery({ queryKey: ['monitors'], queryFn: api.listMonitors });
  const rules = useQuery({ queryKey: ['rules'], queryFn: api.listRules });
  const channels = useQuery({ queryKey: ['channels'], queryFn: api.listChannels });
  const events = useQuery({ queryKey: ['events'], queryFn: api.listEvents });
  const logs = useQuery({ queryKey: ['notificationLogs'], queryFn: api.listNotificationLogs });

  const failedLogs = (logs.data ?? []).filter((item) => item.status !== 'sent').length;

  return (
    <Space direction="vertical" size={16} className="full-width">
      <Row gutter={16}>
        <Col xs={24} md={6}>
          <Card>
            <Statistic title="Monitors" value={monitors.data?.length ?? 0} />
          </Card>
        </Col>
        <Col xs={24} md={6}>
          <Card>
            <Statistic title="Rules" value={rules.data?.length ?? 0} />
          </Card>
        </Col>
        <Col xs={24} md={6}>
          <Card>
            <Statistic title="Channels" value={channels.data?.length ?? 0} />
          </Card>
        </Col>
        <Col xs={24} md={6}>
          <Card>
            <Statistic title="Failed Sends" value={failedLogs} />
          </Card>
        </Col>
      </Row>
      <Card title="Recent Events">
        <EventsTable data={events.data ?? []} loading={events.isLoading} compact />
      </Card>
    </Space>
  );
}

function MonitorsPage() {
  const queryClient = useQueryClient();
  const { message } = AntApp.useApp();
  const [editing, setEditing] = useState<Monitor | null>(null);
  const [drawerOpen, setDrawerOpen] = useState(false);
  const monitors = useQuery({ queryKey: ['monitors'], queryFn: api.listMonitors });
  const plugins = useQuery({ queryKey: ['plugins'], queryFn: api.listPlugins });
  const monitorPlugins = plugins.data?.length ? plugins.data : fallbackMonitorPlugins;
  const pluginByID = useMemo(() => new Map(monitorPlugins.map((item) => [item.id, item])), [monitorPlugins]);

  const saveMutation = useMutation({
    mutationFn: (payload: { id?: number; input: MonitorInput }) =>
      payload.id ? api.updateMonitor(payload.id, payload.input) : api.createMonitor(payload.input),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['monitors'] });
      setDrawerOpen(false);
      setEditing(null);
      message.success('Saved');
    },
    onError: (error: Error) => message.error(error.message)
  });

  const deleteMutation = useMutation({
    mutationFn: api.deleteMonitor,
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['monitors'] });
      message.success('Deleted');
    },
    onError: (error: Error) => message.error(error.message)
  });

  const checkMutation = useMutation({
    mutationFn: api.checkMonitor,
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['monitors'] });
      await queryClient.invalidateQueries({ queryKey: ['events'] });
      message.success('Checked');
    },
    onError: (error: Error) => message.error(error.message)
  });

  const columns: ColumnsType<Monitor> = [
    { title: 'Name', dataIndex: 'name' },
    { title: 'Type', dataIndex: 'type', render: (value: MonitorType) => <Tag>{pluginByID.get(value)?.name ?? value}</Tag> },
    { title: 'Enabled', dataIndex: 'enabled', render: (value: boolean) => <StatusTag ok={value} okText="On" badText="Off" /> },
    { title: 'Interval', dataIndex: 'intervalSeconds', render: (value: number) => `${value}s` },
    { title: 'Last Status', dataIndex: 'lastStatus', render: (value: string) => <StatusTag ok={value !== 'error'} okText={value || 'pending'} badText="error" /> },
    { title: 'Last Checked', dataIndex: 'lastCheckedAt', render: formatDate },
    {
      title: 'Actions',
      width: 220,
      render: (_, record) => (
        <Space>
          <Button icon={<PlayCircleOutlined />} onClick={() => checkMutation.mutate(record.id)} />
          <Button onClick={() => { setEditing(record); setDrawerOpen(true); }}>Edit</Button>
          <Popconfirm title="Delete monitor?" onConfirm={() => deleteMutation.mutate(record.id)}>
            <Button danger>Delete</Button>
          </Popconfirm>
        </Space>
      )
    }
  ];

  return (
    <>
      <PageToolbar>
        <Button
          type="primary"
          icon={<PlusOutlined />}
          onClick={() => {
            setEditing(null);
            setDrawerOpen(true);
          }}
        >
          New Monitor
        </Button>
      </PageToolbar>
      <Table rowKey="id" loading={monitors.isLoading} columns={columns} dataSource={monitors.data ?? []} />
      <MonitorDrawer
        open={drawerOpen}
        record={editing}
        plugins={monitorPlugins}
        saving={saveMutation.isPending}
        onClose={() => setDrawerOpen(false)}
        onSave={(input) => saveMutation.mutate({ id: editing?.id, input })}
      />
    </>
  );
}

function RulesPage() {
  const queryClient = useQueryClient();
  const { message } = AntApp.useApp();
  const [editing, setEditing] = useState<Rule | null>(null);
  const [drawerOpen, setDrawerOpen] = useState(false);
  const rules = useQuery({ queryKey: ['rules'], queryFn: api.listRules });
  const monitors = useQuery({ queryKey: ['monitors'], queryFn: api.listMonitors });
  const channels = useQuery({ queryKey: ['channels'], queryFn: api.listChannels });
  const templates = useQuery({ queryKey: ['templates'], queryFn: api.listTemplates });

  const monitorByID = useMemo(() => new Map((monitors.data ?? []).map((item) => [item.id, item])), [monitors.data]);

  const saveMutation = useMutation({
    mutationFn: (payload: { id?: number; input: RuleInput }) =>
      payload.id ? api.updateRule(payload.id, payload.input) : api.createRule(payload.input),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['rules'] });
      setDrawerOpen(false);
      setEditing(null);
      message.success('Saved');
    },
    onError: (error: Error) => message.error(error.message)
  });

  const deleteMutation = useMutation({
    mutationFn: api.deleteRule,
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['rules'] });
      message.success('Deleted');
    },
    onError: (error: Error) => message.error(error.message)
  });

  const columns: ColumnsType<Rule> = [
    { title: 'Name', dataIndex: 'name' },
    { title: 'Monitor', dataIndex: 'monitorId', render: (id: number) => monitorByID.get(id)?.name ?? id },
    { title: 'Enabled', dataIndex: 'enabled', render: (value: boolean) => <StatusTag ok={value} okText="On" badText="Off" /> },
    { title: 'Channels', dataIndex: 'notifyChannelIds', render: (ids: number[]) => ids.length },
    { title: 'Cooldown', dataIndex: 'cooldownSeconds', render: (value: number) => `${value}s` },
    {
      title: 'Actions',
      width: 170,
      render: (_, record) => (
        <Space>
          <Button onClick={() => { setEditing(record); setDrawerOpen(true); }}>Edit</Button>
          <Popconfirm title="Delete rule?" onConfirm={() => deleteMutation.mutate(record.id)}>
            <Button danger>Delete</Button>
          </Popconfirm>
        </Space>
      )
    }
  ];

  return (
    <>
      <PageToolbar>
        <Button
          type="primary"
          icon={<PlusOutlined />}
          onClick={() => {
            setEditing(null);
            setDrawerOpen(true);
          }}
        >
          New Rule
        </Button>
      </PageToolbar>
      <Table rowKey="id" loading={rules.isLoading} columns={columns} dataSource={rules.data ?? []} />
      <RuleDrawer
        open={drawerOpen}
        record={editing}
        monitors={monitors.data ?? []}
        channels={channels.data ?? []}
        templates={templates.data ?? []}
        saving={saveMutation.isPending}
        onClose={() => setDrawerOpen(false)}
        onSave={(input) => saveMutation.mutate({ id: editing?.id, input })}
      />
    </>
  );
}

function ChannelsPage() {
  const queryClient = useQueryClient();
  const { message } = AntApp.useApp();
  const [editing, setEditing] = useState<NotifyChannel | null>(null);
  const [drawerOpen, setDrawerOpen] = useState(false);
  const channels = useQuery({ queryKey: ['channels'], queryFn: api.listChannels });

  const saveMutation = useMutation({
    mutationFn: (payload: { id?: number; input: NotifyChannelInput }) =>
      payload.id ? api.updateChannel(payload.id, payload.input) : api.createChannel(payload.input),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['channels'] });
      setDrawerOpen(false);
      setEditing(null);
      message.success('Saved');
    },
    onError: (error: Error) => message.error(error.message)
  });

  const deleteMutation = useMutation({
    mutationFn: api.deleteChannel,
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['channels'] });
      message.success('Deleted');
    },
    onError: (error: Error) => message.error(error.message)
  });

  const testMutation = useMutation({
    mutationFn: api.testChannel,
    onSuccess: () => message.success('Sent'),
    onError: (error: Error) => message.error(error.message)
  });

  const columns: ColumnsType<NotifyChannel> = [
    { title: 'Name', dataIndex: 'name' },
    { title: 'Type', dataIndex: 'type', render: (value: ChannelType) => <Tag>{value}</Tag> },
    { title: 'Enabled', dataIndex: 'enabled', render: (value: boolean) => <StatusTag ok={value} okText="On" badText="Off" /> },
    {
      title: 'Actions',
      width: 240,
      render: (_, record) => (
        <Space>
          <Button onClick={() => testMutation.mutate(record.id)}>Test</Button>
          <Button onClick={() => { setEditing(record); setDrawerOpen(true); }}>Edit</Button>
          <Popconfirm title="Delete channel?" onConfirm={() => deleteMutation.mutate(record.id)}>
            <Button danger>Delete</Button>
          </Popconfirm>
        </Space>
      )
    }
  ];

  return (
    <>
      <PageToolbar>
        <Button
          type="primary"
          icon={<PlusOutlined />}
          onClick={() => {
            setEditing(null);
            setDrawerOpen(true);
          }}
        >
          New Channel
        </Button>
      </PageToolbar>
      <Table rowKey="id" loading={channels.isLoading} columns={columns} dataSource={channels.data ?? []} />
      <ChannelDrawer
        open={drawerOpen}
        record={editing}
        saving={saveMutation.isPending}
        onClose={() => setDrawerOpen(false)}
        onSave={(input) => saveMutation.mutate({ id: editing?.id, input })}
      />
    </>
  );
}

function TemplatesPage() {
  const queryClient = useQueryClient();
  const { message } = AntApp.useApp();
  const [editing, setEditing] = useState<NotificationTemplate | null>(null);
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [preview, setPreview] = useState<{ subject: string; body: string } | null>(null);
  const templates = useQuery({ queryKey: ['templates'], queryFn: api.listTemplates });

  const saveMutation = useMutation({
    mutationFn: (payload: { id?: number; input: NotificationTemplateInput }) =>
      payload.id ? api.updateTemplate(payload.id, payload.input) : api.createTemplate(payload.input),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['templates'] });
      setDrawerOpen(false);
      setEditing(null);
      message.success('Saved');
    },
    onError: (error: Error) => message.error(error.message)
  });

  const deleteMutation = useMutation({
    mutationFn: api.deleteTemplate,
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['templates'] });
      message.success('Deleted');
    },
    onError: (error: Error) => message.error(error.message)
  });

  const previewMutation = useMutation({
    mutationFn: api.previewTemplate,
    onSuccess: setPreview,
    onError: (error: Error) => message.error(error.message)
  });

  const columns: ColumnsType<NotificationTemplate> = [
    { title: 'Name', dataIndex: 'name' },
    { title: 'Subject', dataIndex: 'subjectTemplate' },
    {
      title: 'Actions',
      width: 260,
      render: (_, record) => (
        <Space>
          <Button onClick={() => previewMutation.mutate(record)}>Preview</Button>
          <Button onClick={() => { setEditing(record); setDrawerOpen(true); }}>Edit</Button>
          <Popconfirm title="Delete template?" onConfirm={() => deleteMutation.mutate(record.id)} disabled={record.id === 1}>
            <Button danger disabled={record.id === 1}>Delete</Button>
          </Popconfirm>
        </Space>
      )
    }
  ];

  return (
    <>
      <PageToolbar>
        <Button
          type="primary"
          icon={<PlusOutlined />}
          onClick={() => {
            setEditing(null);
            setDrawerOpen(true);
          }}
        >
          New Template
        </Button>
      </PageToolbar>
      <Table rowKey="id" loading={templates.isLoading} columns={columns} dataSource={templates.data ?? []} />
      <TemplateDrawer
        open={drawerOpen}
        record={editing}
        saving={saveMutation.isPending}
        onClose={() => setDrawerOpen(false)}
        onSave={(input) => saveMutation.mutate({ id: editing?.id, input })}
      />
      <Modal open={preview !== null} title="Preview" footer={null} onCancel={() => setPreview(null)}>
        <Descriptions column={1} bordered size="small">
          <Descriptions.Item label="Subject">{preview?.subject}</Descriptions.Item>
          <Descriptions.Item label="Body">
            <pre className="json-block">{preview?.body}</pre>
          </Descriptions.Item>
        </Descriptions>
      </Modal>
    </>
  );
}

function LogsPage() {
  const events = useQuery({ queryKey: ['events'], queryFn: api.listEvents });
  const logs = useQuery({ queryKey: ['notificationLogs'], queryFn: api.listNotificationLogs });
  return (
    <Tabs
      items={[
        { key: 'events', label: 'Events', children: <EventsTable data={events.data ?? []} loading={events.isLoading} /> },
        { key: 'notifications', label: 'Notifications', children: <NotificationLogsTable data={logs.data ?? []} loading={logs.isLoading} /> }
      ]}
    />
  );
}

function MonitorDrawer(props: {
  open: boolean;
  record: Monitor | null;
  plugins: MonitorPlugin[];
  saving: boolean;
  onClose: () => void;
  onSave: (input: MonitorInput) => void;
}) {
  const { message } = AntApp.useApp();
  const [form] = Form.useForm();
  return (
    <Drawer
      title={props.record ? 'Edit Monitor' : 'New Monitor'}
      open={props.open}
      onClose={props.onClose}
      width={620}
      destroyOnClose
      afterOpenChange={(open) => {
        if (!open) return;
        const plugin = pluginFor(props.record?.type ?? 'rss', props.plugins);
        form.setFieldsValue({
          name: props.record?.name ?? '',
          type: plugin.id,
          enabled: props.record?.enabled ?? true,
          intervalSeconds: props.record?.intervalSeconds ?? plugin.defaultIntervalSeconds,
          configText: jsonText(props.record?.config ?? plugin.defaultConfig)
        });
      }}
      footer={
        <Space>
          <Button onClick={props.onClose}>Cancel</Button>
          <Button type="primary" loading={props.saving} onClick={() => form.submit()}>
            Save
          </Button>
        </Space>
      }
    >
      <Form
        form={form}
        layout="vertical"
        onValuesChange={(changed) => {
          if (changed.type) {
            const plugin = pluginFor(changed.type, props.plugins);
            form.setFieldsValue({
              intervalSeconds: plugin.defaultIntervalSeconds,
              configText: jsonText(plugin.defaultConfig)
            });
          }
        }}
        onFinish={(values) => {
          try {
            props.onSave({
              name: values.name,
              type: values.type,
              enabled: values.enabled,
              intervalSeconds: values.intervalSeconds,
              config: parseJSON(values.configText)
            });
          } catch (error) {
            message.error((error as Error).message);
          }
        }}
      >
        <Form.Item name="name" label="Name" rules={[{ required: true }]}>
          <Input />
        </Form.Item>
        <Row gutter={12}>
          <Col span={12}>
            <Form.Item name="type" label="Type" rules={[{ required: true }]}>
              <Select options={props.plugins.map((item) => ({ label: item.name, value: item.id }))} />
            </Form.Item>
          </Col>
          <Col span={12}>
            <Form.Item name="intervalSeconds" label="Interval Seconds" rules={[{ required: true }]}>
              <InputNumber min={30} className="full-width" />
            </Form.Item>
          </Col>
        </Row>
        <Form.Item name="enabled" label="Enabled" valuePropName="checked">
          <Switch />
        </Form.Item>
        <Form.Item name="configText" label="Config JSON" rules={[{ required: true }]}>
          <Input.TextArea rows={12} spellCheck={false} />
        </Form.Item>
      </Form>
    </Drawer>
  );
}

function RuleDrawer(props: {
  open: boolean;
  record: Rule | null;
  monitors: Monitor[];
  channels: NotifyChannel[];
  templates: NotificationTemplate[];
  saving: boolean;
  onClose: () => void;
  onSave: (input: RuleInput) => void;
}) {
  const { message } = AntApp.useApp();
  const [form] = Form.useForm();
  return (
    <Drawer
      title={props.record ? 'Edit Rule' : 'New Rule'}
      open={props.open}
      onClose={props.onClose}
      width={680}
      destroyOnClose
      afterOpenChange={(open) => {
        if (!open) return;
        form.setFieldsValue({
          name: props.record?.name ?? '',
          monitorId: props.record?.monitorId ?? props.monitors[0]?.id,
          enabled: props.record?.enabled ?? true,
          cooldownSeconds: props.record?.cooldownSeconds ?? 0,
          notifyChannelIds: props.record?.notifyChannelIds ?? [],
          templateId: props.record?.templateId ?? 1,
          conditionText: jsonText(props.record?.condition ?? defaultRuleCondition())
        });
      }}
      footer={
        <Space>
          <Button onClick={props.onClose}>Cancel</Button>
          <Button type="primary" loading={props.saving} onClick={() => form.submit()}>
            Save
          </Button>
        </Space>
      }
    >
      <Form
        form={form}
        layout="vertical"
        onFinish={(values) => {
          try {
            props.onSave({
              name: values.name,
              monitorId: values.monitorId,
              enabled: values.enabled,
              cooldownSeconds: values.cooldownSeconds,
              notifyChannelIds: values.notifyChannelIds ?? [],
              templateId: values.templateId ?? null,
              condition: parseJSON(values.conditionText)
            });
          } catch (error) {
            message.error((error as Error).message);
          }
        }}
      >
        <Form.Item name="name" label="Name" rules={[{ required: true }]}>
          <Input />
        </Form.Item>
        <Form.Item name="monitorId" label="Monitor" rules={[{ required: true }]}>
          <Select options={props.monitors.map((item) => ({ label: item.name, value: item.id }))} />
        </Form.Item>
        <Form.Item name="notifyChannelIds" label="Notify Channels" rules={[{ required: true }]}>
          <Select mode="multiple" options={props.channels.map((item) => ({ label: item.name, value: item.id }))} />
        </Form.Item>
        <Form.Item name="templateId" label="Template">
          <Select allowClear options={props.templates.map((item) => ({ label: item.name, value: item.id }))} />
        </Form.Item>
        <Row gutter={12}>
          <Col span={12}>
            <Form.Item name="cooldownSeconds" label="Cooldown Seconds">
              <InputNumber min={0} className="full-width" />
            </Form.Item>
          </Col>
          <Col span={12}>
            <Form.Item name="enabled" label="Enabled" valuePropName="checked">
              <Switch />
            </Form.Item>
          </Col>
        </Row>
        <Form.Item name="conditionText" label="Condition JSON" rules={[{ required: true }]}>
          <Input.TextArea rows={12} spellCheck={false} />
        </Form.Item>
      </Form>
    </Drawer>
  );
}

function ChannelDrawer(props: {
  open: boolean;
  record: NotifyChannel | null;
  saving: boolean;
  onClose: () => void;
  onSave: (input: NotifyChannelInput) => void;
}) {
  const { message } = AntApp.useApp();
  const [form] = Form.useForm();
  return (
    <Drawer
      title={props.record ? 'Edit Channel' : 'New Channel'}
      open={props.open}
      onClose={props.onClose}
      width={620}
      destroyOnClose
      afterOpenChange={(open) => {
        if (!open) return;
        form.setFieldsValue({
          name: props.record?.name ?? '',
          type: props.record?.type ?? 'bark',
          enabled: props.record?.enabled ?? true,
          configText: jsonText(props.record?.config ?? defaultChannelConfig('bark'))
        });
      }}
      footer={
        <Space>
          <Button onClick={props.onClose}>Cancel</Button>
          <Button type="primary" loading={props.saving} onClick={() => form.submit()}>
            Save
          </Button>
        </Space>
      }
    >
      <Form
        form={form}
        layout="vertical"
        onValuesChange={(changed) => {
          if (changed.type) {
            form.setFieldValue('configText', jsonText(defaultChannelConfig(changed.type)));
          }
        }}
        onFinish={(values) => {
          try {
            props.onSave({
              name: values.name,
              type: values.type,
              enabled: values.enabled,
              config: parseJSON(values.configText)
            });
          } catch (error) {
            message.error((error as Error).message);
          }
        }}
      >
        <Form.Item name="name" label="Name" rules={[{ required: true }]}>
          <Input />
        </Form.Item>
        <Form.Item name="type" label="Type" rules={[{ required: true }]}>
          <Select options={channelTypeOptions} />
        </Form.Item>
        <Form.Item name="enabled" label="Enabled" valuePropName="checked">
          <Switch />
        </Form.Item>
        <Form.Item name="configText" label="Config JSON" rules={[{ required: true }]}>
          <Input.TextArea rows={12} spellCheck={false} />
        </Form.Item>
      </Form>
    </Drawer>
  );
}

function TemplateDrawer(props: {
  open: boolean;
  record: NotificationTemplate | null;
  saving: boolean;
  onClose: () => void;
  onSave: (input: NotificationTemplateInput) => void;
}) {
  const [form] = Form.useForm();
  return (
    <Drawer
      title={props.record ? 'Edit Template' : 'New Template'}
      open={props.open}
      onClose={props.onClose}
      width={620}
      destroyOnClose
      afterOpenChange={(open) => {
        if (!open) return;
        form.setFieldsValue({
          name: props.record?.name ?? '',
          subjectTemplate: props.record?.subjectTemplate ?? '${monitor.name}: ${event.type}',
          bodyTemplate: props.record?.bodyTemplate ?? 'Monitor: ${monitor.name}\nTime: ${event.time}\n${rss.title}${testflight.message}${webpage.summary}${github.release.name} ${github.release.tagName}\n${github.release.url}'
        });
      }}
      footer={
        <Space>
          <Button onClick={props.onClose}>Cancel</Button>
          <Button type="primary" loading={props.saving} onClick={() => form.submit()}>
            Save
          </Button>
        </Space>
      }
    >
      <Form form={form} layout="vertical" onFinish={(values) => props.onSave(values)}>
        <Form.Item name="name" label="Name" rules={[{ required: true }]}>
          <Input />
        </Form.Item>
        <Form.Item name="subjectTemplate" label="Subject" rules={[{ required: true }]}>
          <Input />
        </Form.Item>
        <Form.Item name="bodyTemplate" label="Body" rules={[{ required: true }]}>
          <Input.TextArea rows={12} />
        </Form.Item>
      </Form>
    </Drawer>
  );
}

function EventsTable(props: { data: EventRecord[]; loading: boolean; compact?: boolean }) {
  const columns: ColumnsType<EventRecord> = [
    { title: 'ID', dataIndex: 'id', width: 80 },
    { title: 'Type', dataIndex: 'type', render: (value: string) => <Tag>{value}</Tag> },
    { title: 'Monitor', dataIndex: 'monitorId', width: 100 },
    { title: 'Created', dataIndex: 'createdAt', render: formatDate },
    {
      title: 'Payload',
      dataIndex: 'payload',
      render: (value: Record<string, unknown>) => <pre className="json-block">{jsonText(value)}</pre>
    }
  ];
  return <Table rowKey="id" loading={props.loading} columns={columns} dataSource={props.data} pagination={props.compact ? { pageSize: 5 } : { pageSize: 10 }} />;
}

function NotificationLogsTable(props: { data: NotificationLog[]; loading: boolean }) {
  const columns: ColumnsType<NotificationLog> = [
    { title: 'ID', dataIndex: 'id', width: 80 },
    { title: 'Event', dataIndex: 'eventId', width: 100 },
    { title: 'Channel', dataIndex: 'channelId', width: 100 },
    { title: 'Status', dataIndex: 'status', render: (value: string) => <StatusTag ok={value === 'sent'} okText={value} badText={value} /> },
    { title: 'Error', dataIndex: 'error' },
    { title: 'Created', dataIndex: 'createdAt', render: formatDate }
  ];
  return <Table rowKey="id" loading={props.loading} columns={columns} dataSource={props.data} />;
}

function PageToolbar(props: { children: React.ReactNode }) {
  return <div className="page-toolbar">{props.children}</div>;
}

function StatusTag(props: { ok: boolean; okText: string; badText: string }) {
  return <Tag color={props.ok ? 'green' : 'red'}>{props.ok ? props.okText : props.badText}</Tag>;
}

function titleForPage(page: PageKey) {
  return {
    dashboard: 'Dashboard',
    monitors: 'Monitors',
    rules: 'Rules',
    channels: 'Channels',
    templates: 'Templates',
    logs: 'Logs'
  }[page];
}

function fallbackPlugin(id: MonitorType, name: string, interval: number, config: Record<string, unknown>): MonitorPlugin {
  return {
    id,
    name,
    description: '',
    builtin: true,
    defaultIntervalSeconds: interval,
    defaultConfig: config,
    configFields: [],
    events: [],
    templateVariables: []
  };
}

function pluginFor(type: MonitorType, plugins: MonitorPlugin[]): MonitorPlugin {
  return plugins.find((item) => item.id === type) ?? plugins[0] ?? fallbackMonitorPlugins[0];
}

function defaultChannelConfig(type: ChannelType): Record<string, unknown> {
  if (type === 'email') {
    return {
      host: 'smtp.example.com',
      port: 587,
      username: '',
      password: '',
      from: '',
      to: ['you@example.com'],
      startTls: true,
      implicitTls: false
    };
  }
  return {
    serverUrl: 'https://api.day.app',
    deviceKey: '',
    group: 'WatchBell'
  };
}

function defaultRuleCondition(): Record<string, unknown> {
  return {
    match: 'any',
    conditions: [
      {
        field: 'rss.title',
        operator: 'contains',
        value: 'keyword'
      }
    ]
  };
}

function parseJSON(value: string): Record<string, unknown> {
  try {
    return JSON.parse(value || '{}') as Record<string, unknown>;
  } catch {
    throw new Error('Invalid JSON');
  }
}

function jsonText(value: unknown) {
  return JSON.stringify(value ?? {}, null, 2);
}

function formatDate(value?: string) {
  if (!value) {
    return <Text type="secondary">-</Text>;
  }
  return new Date(value).toLocaleString();
}
