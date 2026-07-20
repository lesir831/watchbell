import { lazy, Suspense, useEffect, useState, type ReactNode } from 'react';
import {
  App as AntApp,
  Button,
  Card,
  Drawer,
  Grid,
  Layout,
  Modal,
  Spin,
  Typography
} from 'antd';
import {
  ArrowRightOutlined,
  EyeInvisibleOutlined,
  EyeOutlined,
  LockOutlined,
  LogoutOutlined,
  MenuOutlined,
  MoreOutlined,
  PlusOutlined,
  ReloadOutlined,
  SafetyCertificateOutlined,
  SearchOutlined,
  UserOutlined
} from '@ant-design/icons';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, APIError, AUTH_EXPIRED_EVENT } from './api';
import { configureDateTimePreferences } from './components/Common';

const DashboardPage = lazy(() => import('./pages/DashboardPage'));
const MonitorsPage = lazy(() => import('./pages/MonitorsPage'));
const MonitorDetailPage = lazy(() => import('./pages/MonitorDetailPage'));
const RulesPage = lazy(() => import('./pages/RulesPage'));
const ChannelsPage = lazy(() => import('./pages/ChannelsPage'));
const TemplatesPage = lazy(() => import('./pages/TemplatesPage'));
const ActivityPage = lazy(() => import('./pages/ActivityPage'));
const HelpPage = lazy(() => import('./pages/HelpPage'));
const SettingsPage = lazy(() => import('./pages/SettingsPage'));

type PageKey = 'dashboard' | 'monitors' | 'monitorDetail' | 'rules' | 'channels' | 'templates' | 'activity' | 'settings' | 'help';
type RouteState = { page: PageKey; monitorId?: number; ruleId?: number; activityTab?: 'events' | 'evaluations' | 'attempts'; eventId?: number };
type NavigationPageKey = Exclude<PageKey, 'monitorDetail'>;

type NavigationItem = {
  key: NavigationPageKey;
  icon: ReactNode;
  label: string;
  description: string;
  search: string;
};

const { Header, Sider, Content } = Layout;
const { Text } = Typography;

type DesignIconName = 'bell' | 'grid' | 'radar' | 'rule' | 'channel' | 'template' | 'activity' | 'settings' | 'help' | 'search';

function DesignIcon({ name, className = '' }: { name: DesignIconName; className?: string }) {
  const paths: Record<DesignIconName, ReactNode> = {
    bell: <><path d="M18 8a6 6 0 0 0-12 0c0 7-3 7-3 9h18c0-2-3-2-3-9" /><path d="M10 21h4" /><path d="M8.5 12h2l1.2-3 2.1 6 1.2-3h1.5" /></>,
    grid: <><rect x="3" y="3" width="7" height="7" rx="1.5" /><rect x="14" y="3" width="7" height="7" rx="1.5" /><rect x="3" y="14" width="7" height="7" rx="1.5" /><rect x="14" y="14" width="7" height="7" rx="1.5" /></>,
    radar: <><circle cx="12" cy="12" r="8" /><circle cx="12" cy="12" r="3" /><path d="m14.2 9.8 4-4M4 20l5.2-5.2" /></>,
    rule: <><circle cx="6" cy="5" r="2" /><circle cx="18" cy="7" r="2" /><circle cx="18" cy="17" r="2" /><path d="M8 5h3a3 3 0 0 1 3 3v6a3 3 0 0 0 3 3M14 10a3 3 0 0 0 3-3" /></>,
    channel: <><path d="M4 5h16v12H8l-4 3V5Z" /><path d="M8 9h8M8 13h5" /></>,
    template: <><path d="M6 3h9l4 4v14H6V3Z" /><path d="M14 3v5h5M9 12h6M9 16h6" /></>,
    activity: <path d="M3 12h4l2-6 4 12 2-6h6" />,
    settings: <><circle cx="12" cy="12" r="3" /><path d="M19.4 15a1.7 1.7 0 0 0 .34 1.88l.06.06-2.83 2.83-.06-.06A1.7 1.7 0 0 0 15 19.4a1.7 1.7 0 0 0-1 .6 1.7 1.7 0 0 0-.4 1.1V21H9.6v-.1a1.7 1.7 0 0 0-1.1-1.5 1.7 1.7 0 0 0-1.88.34l-.06.06-2.83-2.83.06-.06A1.7 1.7 0 0 0 4.6 15a1.7 1.7 0 0 0-.6-1 1.7 1.7 0 0 0-1.1-.4H3V9.6h.1A1.7 1.7 0 0 0 4.6 8.5a1.7 1.7 0 0 0-.34-1.88l-.06-.06 2.83-2.83.06.06A1.7 1.7 0 0 0 9 4.6a1.7 1.7 0 0 0 1-.6 1.7 1.7 0 0 0 .4-1.1V3h4v.1A1.7 1.7 0 0 0 15.5 4a1.7 1.7 0 0 0 1.88-.34l.06-.06 2.83 2.83-.06.06A1.7 1.7 0 0 0 19.4 8.5c.39.27.64.7.68 1.17V9.6h.1v4h-.1A1.7 1.7 0 0 0 19.4 15Z" /></>,
    help: <><circle cx="12" cy="12" r="9" /><path d="M9.8 9a2.4 2.4 0 1 1 3.6 2.1c-.9.5-1.4 1-1.4 2M12 17h.01" /></>,
    search: <><circle cx="11" cy="11" r="7" /><path d="m20 20-4-4" /></>
  };
  return <svg className={`design-icon ${className}`} viewBox="0 0 24 24" aria-hidden="true">{paths[name]}</svg>;
}

const pageItems: NavigationItem[] = [
  { key: 'dashboard', icon: <DesignIcon name="grid" />, label: '运行概览', description: '查看运行脉冲与处理队列', search: '概览 总览 首页 状态' },
  { key: 'monitors', icon: <DesignIcon name="radar" />, label: '监控', description: '搜索、筛选和检查监控', search: '监控 列表 搜索 检查' },
  { key: 'rules', icon: <DesignIcon name="rule" />, label: '规则', description: '编辑条件、模板和通知路由', search: '规则 条件 判断 路由' },
  { key: 'channels', icon: <DesignIcon name="channel" />, label: '通知渠道', description: '管理端点并发送测试通知', search: '通知 渠道 bark 邮件 webhook' },
  { key: 'templates', icon: <DesignIcon name="template" />, label: '通知模板', description: '预览消息并检查模板变量', search: '模板 消息 变量 预览' },
  { key: 'activity', icon: <DesignIcon name="activity" />, label: '活动与诊断', description: '追踪检查、事件和通知', search: '活动 诊断 日志 通知' },
  { key: 'settings', icon: <DesignIcon name="settings" />, label: '设置', description: '配置实例、网络与安全选项', search: '设置 配置 代理 安全 密码' },
  { key: 'help', icon: <DesignIcon name="help" />, label: '帮助', description: '检查实时变量与规则字段', search: '帮助 变量 实时检查 规则 模板' }
];

export default function App() {
  return <AuthGate />;
}

function AuthGate() {
  const queryClient = useQueryClient();
  const status = useQuery({ queryKey: ['authStatus'], queryFn: api.authStatus, retry: false });
  const me = useQuery({
    queryKey: ['authMe'],
    queryFn: api.me,
    enabled: status.data?.enabled === true,
    retry: false
  });

  useEffect(() => {
    const expireSession = () => { void queryClient.resetQueries({ queryKey: ['authMe'], exact: true }); };
    window.addEventListener(AUTH_EXPIRED_EVENT, expireSession);
    return () => window.removeEventListener(AUTH_EXPIRED_EVENT, expireSession);
  }, [queryClient]);

  if (status.isLoading || (status.data?.enabled && me.isLoading)) {
    return <LoadingScreen />;
  }
  if (status.isError) {
    return <ErrorScreen error={status.error as Error} />;
  }
  if (status.data?.enabled && me.isError) {
    const error = me.error as APIError;
    if (error.status === 401) {
      return <LoginPage />;
    }
    return <ErrorScreen error={error} />;
  }

  return <Shell authEnabled={status.data?.enabled === true} username={me.data?.username ?? status.data?.username ?? ''} />;
}

function LoginPage() {
  const queryClient = useQueryClient();
  const { message } = AntApp.useApp();
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [showPassword, setShowPassword] = useState(false);
  const login = useMutation({
    mutationFn: api.login,
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['authMe'] });
      message.success('登录成功');
    },
    onError: (error: Error) => message.error(error.message)
  });

  const submit = (event: React.FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (!username.trim() || !password || login.isPending) return;
    login.mutate({ username: username.trim(), password });
  };

  return (
    <main className="login-screen" data-od-id="login-screen">
      <section className="login-signal-panel" data-od-id="login-signal-panel">
        <div className="login-signal-brand">
          <img src="/watchbell-icon-192.png" alt="WatchBell" />
          <div>
            <strong>WatchBell</strong>
            <span>自托管变化监控</span>
          </div>
        </div>

        <div className="signal-message">
          <span className="signal-kicker">持续监控 · 精准通知</span>
          <h1 data-od-id="login-headline">重要变化，<br />及时响铃。</h1>
          <p>把数据源、规则与通知链路收拢到一处。只有值得关注的变化，才会打破安静。</p>
        </div>

        <div className="signal-visual" aria-hidden="true">
          <svg viewBox="0 0 620 180" role="presentation">
            <path className="signal-grid-line" d="M0 90H620" />
            <path className="signal-trace" d="M0 90H110L137 90L159 42L184 138L212 90H318L341 90L362 66L384 112L406 90H620" />
            <circle className="signal-ping" cx="362" cy="66" r="6" />
          </svg>
        </div>

        <div className="signal-footnotes">
          <span><i />单用户模式</span>
          <span><SafetyCertificateOutlined />HttpOnly 签名会话</span>
        </div>
      </section>

      <section className="login-access-panel" data-od-id="login-access-panel">
        <form className="login-form-shell" onSubmit={submit}>
          <div className="access-label"><span />私有实例访问</div>
          <h2>欢迎回来</h2>
          <p className="access-intro">使用实例管理员账户进入 WatchBell 控制台。</p>

          <div className="secure-note">
            <LockOutlined />
            <span>凭据只发送到当前实例，不会离开你的部署环境。</span>
          </div>

          <label className="field-label" htmlFor="username">用户名</label>
          <div className="input-shell">
            <UserOutlined />
            <input
              id="username"
              className="native-input"
              autoComplete="username"
              autoFocus
              required
              value={username}
              onChange={(event) => setUsername(event.target.value)}
            />
          </div>

          <label className="field-label" htmlFor="password">密码</label>
          <div className="input-shell">
            <LockOutlined />
            <input
              id="password"
              className="native-input"
              type={showPassword ? 'text' : 'password'}
              autoComplete="current-password"
              required
              value={password}
              onChange={(event) => setPassword(event.target.value)}
            />
            <button
              type="button"
              className="password-toggle"
              aria-label={showPassword ? '隐藏密码' : '显示密码'}
              aria-pressed={showPassword}
              onClick={() => setShowPassword((value) => !value)}
            >
              {showPassword ? <EyeInvisibleOutlined /> : <EyeOutlined />}
            </button>
          </div>

          {login.isError && <div className="login-error" role="alert">{(login.error as Error).message}</div>}

          <Button
            className="login-submit"
            type="primary"
            size="large"
            htmlType="submit"
            loading={login.isPending}
            disabled={!username.trim() || !password}
            block
            data-od-id="login-submit"
          >
            进入控制台
          </Button>

          <details className="reset-help">
            <summary>无法登录？</summary>
            <p>在 WatchBell 服务器上运行 <code>watchbell set-password</code> 重置管理员密码。</p>
          </details>
        </form>
      </section>
    </main>
  );
}

function Shell(props: { authEnabled: boolean; username: string }) {
  const screens = Grid.useBreakpoint();
  const mobile = !screens.lg;
  const [mobileMenuOpen, setMobileMenuOpen] = useState(false);
  const [commandOpen, setCommandOpen] = useState(false);
  const [commandSearch, setCommandSearch] = useState('');
  const [monitorCreateRequest, setMonitorCreateRequest] = useState(0);
  const [route, setRoute] = useState<RouteState>(() => routeFromHash());
  const queryClient = useQueryClient();
  const system = useQuery({ queryKey: ['systemStatus'], queryFn: api.systemStatus, refetchInterval: 30_000, retry: false });
  const settings = useQuery({ queryKey: ['settings'], queryFn: api.settings, refetchInterval: 60_000, retry: false });
  if (settings.data) configureDateTimePreferences(settings.data);
  const logout = useMutation({
    mutationFn: api.logout,
    onSuccess: async () => queryClient.invalidateQueries({ queryKey: ['authMe'] })
  });

  useEffect(() => {
    const onHashChange = () => setRoute(routeFromHash());
    window.addEventListener('hashchange', onHashChange);
    if (!window.location.hash) window.history.replaceState(null, '', '#/dashboard');
    return () => window.removeEventListener('hashchange', onHashChange);
  }, []);

  useEffect(() => {
    const onShortcut = (event: KeyboardEvent) => {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 'k') {
        event.preventDefault();
        setCommandOpen((open) => !open);
      }
    };
    window.addEventListener('keydown', onShortcut);
    return () => window.removeEventListener('keydown', onShortcut);
  }, []);

  useEffect(() => {
    if (!props.authEnabled) return;
    let lastRefreshAt = 0;
    let refreshing = false;
    const refreshForActivity = () => {
      const now = Date.now();
      if (refreshing || now-lastRefreshAt < 60_000) return;
      refreshing = true;
      lastRefreshAt = now;
      void api.touchSession().catch(() => undefined).finally(() => { refreshing = false; });
    };
    const onVisibility = () => { if (document.visibilityState === 'visible') refreshForActivity(); };
    window.addEventListener('pointerdown', refreshForActivity, { passive: true });
    window.addEventListener('click', refreshForActivity);
    window.addEventListener('keydown', refreshForActivity);
    window.addEventListener('scroll', refreshForActivity, { passive: true, capture: true });
    window.addEventListener('focus', refreshForActivity);
    document.addEventListener('visibilitychange', onVisibility);
    return () => {
      window.removeEventListener('pointerdown', refreshForActivity);
      window.removeEventListener('click', refreshForActivity);
      window.removeEventListener('keydown', refreshForActivity);
      window.removeEventListener('scroll', refreshForActivity, true);
      window.removeEventListener('focus', refreshForActivity);
      document.removeEventListener('visibilitychange', onVisibility);
    };
  }, [props.authEnabled]);

  const navigate = (key: string) => {
    window.location.hash = `#/${key}`;
    setMobileMenuOpen(false);
    setCommandOpen(false);
    setCommandSearch('');
  };
  const openNewMonitor = () => {
    setMonitorCreateRequest((request) => request + 1);
    navigate('monitors');
  };
  const activeNavigationKey = route.page === 'monitorDetail' ? 'monitors' : route.page;
  const mobileNavigationKeys: NavigationPageKey[] = ['dashboard', 'monitors', 'rules', 'activity'];
  const normalizedCommandSearch = commandSearch.trim().toLowerCase();
  const commandItems = [
    ...pageItems.map((item) => ({ ...item, command: item.key as string })),
    { command: 'new-monitor', icon: <PlusOutlined />, label: '新建监控', description: '配置一个新的信号源', search: '新建 添加 创建 监控' }
  ].filter((item) => `${item.label} ${item.description} ${item.search}`.toLowerCase().includes(normalizedCommandSearch));
  const navigation = (
    <>
      <div className="sidebar-rule" />
      <div className="nav-label">工作区</div>
      <nav className="sidebar-nav" aria-label="主导航">
        {pageItems.map((item) => (
          <button
            key={item.key}
            type="button"
            className={`nav-button ${activeNavigationKey === item.key ? 'active' : ''}`}
            aria-current={activeNavigationKey === item.key ? 'page' : undefined}
            onClick={() => navigate(item.key)}
          >
            <span className="nav-icon">{item.icon}</span>
            <span>{item.label}</span>
            {activeNavigationKey === item.key && <i aria-hidden="true" />}
          </button>
        ))}
      </nav>
    </>
  );

  return (
    <Layout className="app-shell">
      {!mobile && (
        <Sider className="app-sider" width={248}>
          <Brand />
          {navigation}
          <button className="nav-button sider-command" type="button" onClick={() => setCommandOpen(true)}>
            <DesignIcon name="search" /><span>快速查找</span><span className="shortcut-hint">⌘ K</span>
          </button>
          <div className="system-card">
            <div className="system-card-row"><strong>{system.data?.scheduler.lastTickAt ? '调度器在线' : '正在连接调度器'}</strong><span className={`status-beacon ${system.isError ? 'is-error' : ''}`} /></div>
            <span>{system.data?.scheduler.workerCount ?? '—'} 个工作线程 · SQLite {system.data?.database === 'ok' ? '正常' : '检查中'}</span>
          </div>
        </Sider>
      )}
      <Drawer className="mobile-nav" placement="left" width={284} open={mobileMenuOpen} onClose={() => setMobileMenuOpen(false)} closable={false} styles={{ body: { padding: 0 } }}>
        <Brand />
        {navigation}
      </Drawer>
      <Layout className="workspace-shell">
        <Header className="app-header">
          <div className="header-title">
            {mobile && <Button type="text" icon={<MenuOutlined />} aria-label="打开导航" onClick={() => setMobileMenuOpen(true)} />}
            <div className="breadcrumb"><span>WatchBell</span><b>/</b><strong>{titleForRoute(route)}</strong></div>
          </div>
          <div className="header-actions">
            {!mobile && <Button className="header-command" icon={<SearchOutlined />} onClick={() => setCommandOpen(true)}>快速查找 <span className="header-shortcut">⌘ K</span></Button>}
            <Button className="header-icon-button" icon={<ReloadOutlined />} aria-label="刷新当前数据" title="刷新当前数据" onClick={() => queryClient.invalidateQueries()} />
            {props.authEnabled && (
              <Button className="header-icon-button logout-button" icon={<LogoutOutlined />} aria-label="退出登录" title="退出登录" loading={logout.isPending} onClick={() => logout.mutate()} />
            )}
          </div>
        </Header>
        <Content className="app-content">
          <Suspense fallback={<div className="page-loading"><Spin /></div>}>
            {route.page === 'dashboard' && <DashboardPage onNavigate={navigate} onCreateMonitor={openNewMonitor} />}
            {route.page === 'monitors' && <MonitorsPage onNavigate={navigate} createRequest={monitorCreateRequest} />}
            {route.page === 'monitorDetail' && route.monitorId && <MonitorDetailPage monitorId={route.monitorId} onNavigate={navigate} />}
            {route.page === 'rules' && <RulesPage editRuleId={route.ruleId} />}
            {route.page === 'channels' && <ChannelsPage />}
            {route.page === 'templates' && <TemplatesPage />}
            {route.page === 'activity' && <ActivityPage initialTab={route.activityTab} initialEventId={route.eventId} />}
            {route.page === 'settings' && <SettingsPage authEnabled={props.authEnabled} username={props.username} onNavigate={navigate} />}
            {route.page === 'help' && <HelpPage />}
          </Suspense>
        </Content>
      </Layout>
      {mobile && (
        <nav className="mobile-bottom-nav" aria-label="移动端主导航" data-od-id="mobile-bottom-navigation">
          {pageItems.filter((item) => mobileNavigationKeys.includes(item.key)).map((item) => (
            <button
              key={item.key}
              type="button"
              className={`mobile-nav-button ${activeNavigationKey === item.key ? 'active' : ''}`}
              aria-current={activeNavigationKey === item.key ? 'page' : undefined}
              onClick={() => navigate(item.key)}
            >
              {item.icon}<span>{item.key === 'dashboard' ? '概览' : item.key === 'activity' ? '活动' : item.label}</span>
            </button>
          ))}
          <button
            type="button"
            className={`mobile-nav-button ${!mobileNavigationKeys.includes(activeNavigationKey as NavigationPageKey) ? 'active' : ''}`}
            aria-label="打开更多导航"
            onClick={() => setMobileMenuOpen(true)}
          >
            <MoreOutlined /><span>更多</span>
          </button>
        </nav>
      )}
      <Modal
        className="command-palette"
        open={commandOpen}
        width={560}
        centered
        closable={false}
        footer={null}
        destroyOnHidden
        onCancel={() => { setCommandOpen(false); setCommandSearch(''); }}
      >
        <div className="command-search-shell">
          <SearchOutlined />
          <input
            autoFocus
            type="search"
            value={commandSearch}
            aria-label="快速查找页面或操作"
            placeholder="跳转页面或执行操作…"
            onChange={(event) => setCommandSearch(event.target.value)}
          />
          <kbd>Esc</kbd>
        </div>
        <div className="command-results" role="listbox" aria-label="页面与操作">
          {commandItems.map((item) => (
            <button
              key={item.command}
              type="button"
              className="command-result"
              role="option"
              aria-selected={false}
              onClick={() => item.command === 'new-monitor' ? openNewMonitor() : navigate(item.command)}
            >
              <span className="command-result-icon">{item.icon}</span>
              <span className="command-result-copy"><strong>{item.label}</strong><small>{item.description}</small></span>
              <ArrowRightOutlined className="command-result-arrow" />
            </button>
          ))}
          {commandItems.length === 0 && <div className="command-empty">没有匹配的页面或操作。</div>}
        </div>
        <div className="command-foot"><span>输入关键词筛选</span><span><kbd>Esc</kbd> 关闭</span></div>
      </Modal>
    </Layout>
  );
}

function Brand() {
  return (
    <div className="brand">
      <div className="brand-mark"><DesignIcon name="bell" /></div>
      <div><strong>WatchBell</strong><span>自托管监控中心</span></div>
    </div>
  );
}

function LoadingScreen() {
  return <div className="center-screen"><Spin size="large" /></div>;
}

function ErrorScreen({ error }: { error: Error }) {
  return <div className="center-screen"><Card><Text type="danger">{error.message}</Text></Card></div>;
}

function routeFromHash(): RouteState {
  const raw = window.location.hash.replace(/^#\/?/, '');
  const [value, query = ''] = raw.split('?', 2);
  const params = new URLSearchParams(query);
  const monitorMatch = value.match(/^monitors\/(\d+)$/);
  if (monitorMatch && Number(monitorMatch[1]) > 0) return { page: 'monitorDetail', monitorId: Number(monitorMatch[1]) };
  if (value === 'rules') {
    const ruleId = Number(params.get('ruleId'));
    return { page: 'rules', ...(ruleId > 0 ? { ruleId } : {}) };
  }
  if (value === 'activity') {
    const tab = params.get('tab');
    const eventId = Number(params.get('eventId'));
    const activityTab = tab === 'events' || tab === 'evaluations' || tab === 'attempts' ? tab : undefined;
    return { page: 'activity', ...(activityTab ? { activityTab } : {}), ...(eventId > 0 ? { eventId } : {}) };
  }
  return pageItems.some((item) => item.key === value) ? { page: value as PageKey } : { page: 'dashboard' };
}

function titleForRoute(route: RouteState) {
  return {
    dashboard: '运行概览', monitors: '监控', monitorDetail: `监控详情 #${route.monitorId}`, rules: '规则', channels: '通知渠道', templates: '通知模板', activity: '活动与诊断', settings: '设置', help: '变量与使用帮助'
  }[route.page];
}
