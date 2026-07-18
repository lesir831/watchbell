import { useEffect, useMemo, useState } from 'react';
import {
  Alert,
  Button,
  Empty,
  Select,
  Skeleton,
  Space,
  Table,
  Tabs,
  Tag,
  Typography
} from 'antd';
import { ApiOutlined, ArrowRightOutlined, CodeOutlined, CopyOutlined, ReloadOutlined, SearchOutlined } from '@ant-design/icons';
import { useQuery } from '@tanstack/react-query';
import { api } from '../api';
import { formatDate, PageError, PageHeader } from '../components/Common';
import type { VariableDefinition, VariableSnapshot, VariableUsage } from '../types';

const { Paragraph, Text, Title } = Typography;

const usageLabels: Record<VariableUsage, string> = {
  rule: '规则条件',
  template: '通知模板',
  channel: '渠道配置'
};

export default function HelpPage() {
  const [monitorId, setMonitorId] = useState<number>();
  const catalog = useQuery({ queryKey: ['variableCatalog'], queryFn: api.variableCatalog });
  const monitors = useQuery({ queryKey: ['monitors'], queryFn: api.listMonitors });
  const snapshot = useQuery({
    queryKey: ['monitorVariables', monitorId],
    queryFn: ({ signal }) => api.monitorVariables(monitorId as number, signal),
    enabled: Boolean(monitorId),
    retry: false,
    refetchOnWindowFocus: false,
    refetchOnReconnect: false
  });

  useEffect(() => {
    if (!monitorId && monitors.data?.length) setMonitorId(monitors.data[0].id);
  }, [monitorId, monitors.data]);

  const selectedMonitor = monitors.data?.find((item) => item.id === monitorId);
  // A failed or in-progress inspection must not present an older cached result
  // as if it came from the current source request.
  const liveSnapshot = snapshot.error || snapshot.isFetching ? undefined : snapshot.data;
  const tabs = useMemo(() => {
    if (!catalog.data) return [];
    return [
      {
        key: 'globals',
        label: '跨模块快捷变量',
        children: <VariableSection title="跨模块快捷变量" description="四类监控均可使用相同变量名；没有对应源字段时返回空字符串。" definitions={catalog.data.globals} snapshot={liveSnapshot} loading={snapshot.isFetching} />
      },
      {
        key: 'system',
        label: '系统上下文',
        children: <VariableSection title="系统上下文" description="实时检查可展示 monitor.*；由于不会创建事件，event.*、rule.* 与 message.* 可能不提供。" definitions={catalog.data.system} snapshot={liveSnapshot} loading={snapshot.isFetching} />
      },
      ...catalog.data.modules.map((module) => ({
        key: module.id,
        label: module.name,
        children: <VariableSection
          title={`${module.name} 变量`}
          description={selectedMonitor?.type === module.id ? `当前取值来自监控“${selectedMonitor.name}”的本次实时源站检查。` : '选择这个模块的监控后，可以在表格中查看本次源站取值。'}
          definitions={module.variables}
          snapshot={selectedMonitor?.type === module.id ? liveSnapshot : undefined}
          loading={selectedMonitor?.type === module.id && snapshot.isFetching}
        />
      }))
    ];
  }, [catalog.data, liveSnapshot, selectedMonitor, snapshot.isFetching]);

  const error = (catalog.error || monitors.error) as Error | null;
  const snapshotURL = monitorId ? `/api/monitors/${monitorId}/variables` : '';
  const fetchedAt = liveSnapshot?.generatedAt;

  return (
    <div className="design-page help-page">
      <PageHeader
        eyebrow="变量与规则工作台"
        title="帮助"
        description="直接检查监控源的实时变量，核对规则字段，并确认模板与渠道中的正确写法。"
        actions={<Button icon={<SearchOutlined />} onClick={() => document.getElementById('helpInspect')?.scrollIntoView({ behavior: 'smooth' })}>检查实时变量</Button>}
      />
      <PageError error={error} onRetry={() => { catalog.refetch(); monitors.refetch(); }} />
      <div className="help-layout">
        <nav className="help-index" aria-label="帮助目录">
          {[['helpInspect', '实时变量检查'], ['helpCatalog', '变量目录'], ['helpRules', '嵌套规则示例'], ['helpBoundaries', '使用边界']].map(([id, label]) => <button key={id} type="button" onClick={() => document.getElementById(id)?.scrollIntoView({ behavior: 'smooth' })}>{label}<ArrowRightOutlined /></button>)}
        </nav>
        <div className="help-sections">
          <div className="help-syntax-banner"><span className="type-mark"><CodeOutlined /></span><div><strong>变量语法</strong><p>通知模板和渠道配置使用 <code>{'${path}'}</code>；Webhook JSON 需要保留原始值类型时使用 <code>{'${json:path}'}</code>。规则编辑器无需手写模板语法，直接选择同名字段。</p></div></div>

          <section className="help-section" id="helpInspect">
            <header className="help-section-head"><div><h2>实时变量检查</h2><p>选择一个现有监控，读取当前源数据，并查看本次可用变量。</p></div><Button icon={<ReloadOutlined />} disabled={!monitorId} loading={snapshot.isFetching} onClick={() => snapshot.refetch()}>重新检查</Button></header>
            <div className="help-section-body">
              {monitors.isLoading ? <Skeleton active paragraph={{ rows: 2 }} /> : monitors.data?.length ? (
                <>
                  <div className="help-inspect-note"><ApiOutlined /><span>这是一项只读检查：会使用该监控当前配置与指定代理访问数据源，但不会创建事件或检查记录，不会执行规则、发送通知，也不会修改去重状态。</span></div>
                  <div className="help-monitor-picker"><label>检查目标<Select showSearch value={monitorId} className="help-monitor-select" placeholder="选择监控" optionFilterProp="label" onChange={setMonitorId} options={monitors.data.map((monitor) => ({ label: `${monitor.name} · ${monitor.type}`, value: monitor.id }))} /></label><Button icon={<CopyOutlined />} disabled={!snapshotURL} onClick={() => navigator.clipboard.writeText(snapshotURL)}>复制接口路径</Button></div>
                  <PageError error={snapshot.error as Error | null} onRetry={() => snapshot.refetch()} />
                  {snapshot.isFetching && <Skeleton active paragraph={{ rows: 3 }} />}
                  {liveSnapshot && <>
                    <dl className="help-snapshot"><div><dt>监控</dt><dd>{liveSnapshot.monitorName}</dd></div><div><dt>观测类型</dt><dd>{liveSnapshot.observationType || '—'}</dd></div><div><dt>获取时间</dt><dd>{formatDate(fetchedAt)}</dd></div><div><dt>样本状态</dt><dd>{liveSnapshot.sampleAvailable ? <Tag color="green">已读取</Tag> : <Tag>无内容</Tag>}</dd></div></dl>
                    <div className="help-snapshot-foot"><div><span>{liveSnapshot.message || '检查完成'}</span><code>{snapshotURL}</code></div><Button size="small" icon={<ApiOutlined />} href={snapshotURL} target="_blank">打开实时 JSON</Button></div>
                  </>}
                  {liveSnapshot && !liveSnapshot.sampleAvailable && <Alert type="warning" showIcon message="源站没有可预览的内容" description={liveSnapshot.message || '本次检查成功，但源站没有返回可用于渲染变量的条目。'} />}
                </>
              ) : <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="创建监控后，可以在这里即时访问源站并检查变量取值。" />}
            </div>
          </section>

          <section className="help-section" id="helpCatalog">
            <header><h2>变量目录</h2><p>选择监控后会主动检查一次；表格中的“本次取值”来自上方只读快照。</p></header>
            <Tabs className="help-catalog-tabs" items={tabs} />
          </section>

          <section className="help-section" id="helpRules">
            <header><h2>嵌套规则示例</h2><p>要求最近两分钟发布，并且标题或正文包含目标关键词。</p></header>
            <div className="help-code-layout"><div className="help-rule-code"><pre>{`{
  "match": "all",
  "conditions": [
    {
      "match": "any",
      "conditions": [
        { "field": "rss.title", "operator": "regex", "value": "送码|兑换码" },
        { "field": "rss.content", "operator": "regex", "value": "送码|兑换码" }
      ]
    },
    { "field": "rss.publishedAt", "operator": "within_last", "value": "2m" }
  ]
}`}</pre></div><div className="help-rule-notes"><div><strong>外层：全部满足</strong><span>关键词条件与发布时间条件必须同时成立。</span></div><div><strong>内层：任一满足</strong><span>标题或正文只需有一处匹配正则表达式。</span></div><div><strong>时间字段有约束</strong><span>只有可解析时间才能用于“最近时间内”判断。</span></div></div></div>
          </section>

          <section className="help-section" id="helpBoundaries">
            <header><h2>使用边界</h2><p>理解实时快照与持久化事件之间的区别，避免误判。</p></header>
            <div className="help-boundary-grid"><article><strong>没有后台轮询</strong><p>只有选择监控、点击重新检查或打开实时链接时才会请求源站。</p></article><article><strong>空值是有效结果</strong><p>变量可能缺失、为空或为 null，表示当前阶段没有该字段。</p></article><article><strong>事件字段可能不可用</strong><p>实时检查不会创建事件，因此 event.id、event.time 等字段可能为空。</p></article></div>
          </section>
        </div>
      </div>
    </div>
  );
}

function VariableSection(props: { title: string; description: string; definitions: VariableDefinition[]; snapshot?: VariableSnapshot; loading?: boolean }) {
  return (
    <div className="variable-section">
      <Title level={5}>{props.title}</Title>
      <Paragraph type="secondary">{props.description}</Paragraph>
      <Table<VariableDefinition>
        rowKey="key"
        pagination={false}
        dataSource={props.definitions}
        loading={props.loading}
        scroll={{ x: 900 }}
        columns={[
          {
            title: '变量', dataIndex: 'key', width: 210,
            render: (key: string) => <Text code copyable={{ text: `\${${key}}` }}>{`\${${key}}`}</Text>
          },
          {
            title: '含义', width: 310,
            render: (_, item) => <div><Text strong>{item.label}</Text><div><Text type="secondary">{item.description}</Text></div><Tag className="variable-type-tag">{item.valueType}</Tag></div>
          },
          {
            title: '可用位置', dataIndex: 'availableIn', width: 220,
            render: (values: VariableUsage[]) => <Space wrap>{values.map((value) => <Tag key={value}>{usageLabels[value]}</Tag>)}</Space>
          },
          {
            title: '本次取值', width: 260,
            render: (_, item) => {
              const hasSnapshot = Boolean(props.snapshot);
              const hasValue = Boolean(props.snapshot && Object.prototype.hasOwnProperty.call(props.snapshot.values, item.key));
              return <VariableValue inspected={hasSnapshot} present={hasValue} value={props.snapshot?.values[item.key]} />;
            }
          },
          {
            title: '实时链接', width: 110, fixed: 'right',
            render: (_, item) => {
              const hasValue = props.snapshot && Object.prototype.hasOwnProperty.call(props.snapshot.values, item.key);
              const link = hasValue ? props.snapshot?.valueLinks[item.key] : undefined;
              return link ? <Button size="small" icon={<ApiOutlined />} href={link} target="_blank">打开</Button> : <Text type="secondary">—</Text>;
            }
          }
        ]}
      />
    </div>
  );
}

function VariableValue({ inspected, present, value }: { inspected: boolean; present: boolean; value: unknown }) {
  if (!inspected) return <Text type="secondary">尚未检查</Text>;
  if (!present || value === undefined) return <Tag>未提供</Tag>;
  if (value === null) return <Text code copyable={{ text: 'null' }}>null</Text>;
  if (value === '') return <Text code copyable={{ text: '' }}>{'""（空字符串）'}</Text>;
  const text = typeof value === 'string' ? value : (JSON.stringify(value) ?? String(value));
  const isURL = typeof value === 'string' && /^https?:\/\//i.test(value);
  return (
    <Text ellipsis={{ tooltip: text }} copyable={{ text }} className="variable-live-value">
      {isURL ? <a href={value as string} target="_blank" rel="noreferrer">{text}</a> : text}
    </Text>
  );
}
