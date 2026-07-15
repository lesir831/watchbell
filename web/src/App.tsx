import { lazy, Suspense, useEffect, useState } from 'react';
import {
  App as AntApp,
  Button,
  Card,
  Drawer,
  Grid,
  Layout,
  Menu,
  Spin,
  Typography
} from 'antd';
import {
  BellOutlined,
  DashboardOutlined,
  FileTextOutlined,
  LogoutOutlined,
  MailOutlined,
  MenuOutlined,
  ReloadOutlined,
  SettingOutlined,
  UnorderedListOutlined
} from '@ant-design/icons';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, APIError } from './api';

const DashboardPage = lazy(() => import('./pages/DashboardPage'));
const MonitorsPage = lazy(() => import('./pages/MonitorsPage'));
const RulesPage = lazy(() => import('./pages/RulesPage'));
const ChannelsPage = lazy(() => import('./pages/ChannelsPage'));
const TemplatesPage = lazy(() => import('./pages/TemplatesPage'));
const ActivityPage = lazy(() => import('./pages/ActivityPage'));

type PageKey = 'dashboard' | 'monitors' | 'rules' | 'channels' | 'templates' | 'activity';

const { Header, Sider, Content } = Layout;
const { Text, Title } = Typography;

const pageItems = [
  { key: 'dashboard', icon: <DashboardOutlined />, label: '概览' },
  { key: 'monitors', icon: <ReloadOutlined />, label: '监控' },
  { key: 'rules', icon: <SettingOutlined />, label: '规则' },
  { key: 'channels', icon: <MailOutlined />, label: '通知渠道' },
  { key: 'templates', icon: <FileTextOutlined />, label: '通知模板' },
  { key: 'activity', icon: <UnorderedListOutlined />, label: '活动与诊断' }
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
    return <LoadingScreen />;
  }
  if (status.isError) {
    return <ErrorScreen error={status.error as Error} />;
  }
  if (status.data?.enabled && me.isError) {
    const error = me.error as APIError;
    if (error.status === 401) {
      return <LoginPage username={status.data.username} />;
    }
    return <ErrorScreen error={error} />;
  }

  return <Shell authEnabled={status.data?.enabled === true} username={me.data?.username ?? status.data?.username ?? ''} />;
}

function LoginPage(props: { username: string }) {
  const queryClient = useQueryClient();
  const { message } = AntApp.useApp();
  const [username, setUsername] = useState(props.username);
  const [password, setPassword] = useState('');
  const login = useMutation({
    mutationFn: api.login,
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['authMe'] });
      message.success('登录成功');
    },
    onError: (error: Error) => message.error(error.message)
  });
  return (
    <div className="login-screen">
      <Card className="login-card" bordered={false}>
        <div className="login-brand">
          <img src="/watchbell-icon-192.png" alt="" />
          <div>
            <div>WatchBell</div>
            <Text type="secondary">让重要变化及时响铃</Text>
          </div>
        </div>
        <label className="field-label" htmlFor="username">用户名</label>
        <input id="username" className="native-input" autoComplete="username" value={username} onChange={(event) => setUsername(event.target.value)} />
        <label className="field-label" htmlFor="password">密码</label>
        <input id="password" className="native-input" type="password" autoComplete="current-password" value={password} onChange={(event) => setPassword(event.target.value)} onKeyDown={(event) => {
          if (event.key === 'Enter') login.mutate({ username, password });
        }} />
        <Button type="primary" size="large" loading={login.isPending} block onClick={() => login.mutate({ username, password })}>
          登录
        </Button>
      </Card>
    </div>
  );
}

function Shell(props: { authEnabled: boolean; username: string }) {
  const screens = Grid.useBreakpoint();
  const mobile = !screens.md;
  const [mobileMenuOpen, setMobileMenuOpen] = useState(false);
  const [page, setPage] = useState<PageKey>(() => pageFromHash());
  const queryClient = useQueryClient();
  const logout = useMutation({
    mutationFn: api.logout,
    onSuccess: async () => queryClient.invalidateQueries({ queryKey: ['authMe'] })
  });

  useEffect(() => {
    const onHashChange = () => setPage(pageFromHash());
    window.addEventListener('hashchange', onHashChange);
    if (!window.location.hash) window.history.replaceState(null, '', '#/dashboard');
    return () => window.removeEventListener('hashchange', onHashChange);
  }, []);

  const navigate = (key: string) => {
    window.location.hash = `#/${key}`;
    setMobileMenuOpen(false);
  };
  const menu = (
    <Menu theme="dark" mode="inline" selectedKeys={[page]} onClick={({ key }) => navigate(key)} items={pageItems} />
  );

  return (
    <Layout className="app-shell">
      {!mobile && (
        <Sider className="app-sider" width={236}>
          <Brand />
          {menu}
          <div className="sider-foot"><BellOutlined /> 自托管监控中心</div>
        </Sider>
      )}
      <Drawer className="mobile-nav" placement="left" width={284} open={mobileMenuOpen} onClose={() => setMobileMenuOpen(false)} closable={false} styles={{ body: { padding: 0, background: '#0b1220' } }}>
        <Brand />
        {menu}
      </Drawer>
      <Layout>
        <Header className="app-header">
          <div className="header-title">
            {mobile && <Button type="text" icon={<MenuOutlined />} aria-label="打开导航" onClick={() => setMobileMenuOpen(true)} />}
            <Title level={4}>{titleForPage(page)}</Title>
          </div>
          <div className="header-actions">
            {props.authEnabled && <Text type="secondary" className="header-user">{props.username}</Text>}
            {props.authEnabled && (
              <Button icon={<LogoutOutlined />} loading={logout.isPending} onClick={() => logout.mutate()}>
                {!mobile && '退出'}
              </Button>
            )}
          </div>
        </Header>
        <Content className="app-content">
          <Suspense fallback={<div className="page-loading"><Spin /></div>}>
            {page === 'dashboard' && <DashboardPage onNavigate={navigate} />}
            {page === 'monitors' && <MonitorsPage />}
            {page === 'rules' && <RulesPage />}
            {page === 'channels' && <ChannelsPage />}
            {page === 'templates' && <TemplatesPage />}
            {page === 'activity' && <ActivityPage />}
          </Suspense>
        </Content>
      </Layout>
    </Layout>
  );
}

function Brand() {
  return (
    <div className="brand">
      <img src="/watchbell-icon-192.png" alt="" />
      <span>WatchBell</span>
    </div>
  );
}

function LoadingScreen() {
  return <div className="center-screen"><Spin size="large" /></div>;
}

function ErrorScreen({ error }: { error: Error }) {
  return <div className="center-screen"><Card><Text type="danger">{error.message}</Text></Card></div>;
}

function pageFromHash(): PageKey {
  const value = window.location.hash.replace(/^#\/?/, '') as PageKey;
  return pageItems.some((item) => item.key === value) ? value : 'dashboard';
}

function titleForPage(page: PageKey) {
  return {
    dashboard: '运行概览', monitors: '监控', rules: '规则', channels: '通知渠道', templates: '通知模板', activity: '活动与诊断'
  }[page];
}
