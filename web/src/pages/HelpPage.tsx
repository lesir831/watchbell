import { useEffect, useMemo, useState } from 'react';
import {
  Alert,
  Button,
  Card,
  Descriptions,
  Empty,
  Select,
  Skeleton,
  Space,
  Table,
  Tabs,
  Tag,
  Typography
} from 'antd';
import { ApiOutlined, ReloadOutlined } from '@ant-design/icons';
import { useQuery } from '@tanstack/react-query';
import { api } from '../api';
import { formatDate, PageError } from '../components/Common';
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
    <Space direction="vertical" size={18} className="full-width help-page">
      <PageError error={error} onRetry={() => { catalog.refetch(); monitors.refetch(); }} />
      <Alert
        type="info"
        showIcon
        message="变量语法"
        description={<span>通知模板和渠道配置使用 <Text code>{'${path}'}</Text>；Webhook JSON 正文插入完整值时使用 <Text code>{'${json:path}'}</Text>。规则编辑器中直接选择同名字段。</span>}
      />

      <Card title="嵌套规则示例">
        <Paragraph>下面的规则表示：标题或正文包含“送码/兑换码”，并且源内容发布时间在最近 2 分钟内。网页规则编辑器可以直接创建同样的嵌套结构。</Paragraph>
        <pre className="detail-json help-rule-example">{`{
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
}`}</pre>
      </Card>

      <Card title="实时变量检查" extra={<Button icon={<ReloadOutlined />} disabled={!monitorId} loading={snapshot.isFetching} onClick={() => snapshot.refetch()}>重新检查</Button>}>
        {monitors.isLoading ? <Skeleton active paragraph={{ rows: 2 }} /> : monitors.data?.length ? (
          <Space direction="vertical" size={16} className="full-width">
            <Alert
              type="info"
              showIcon
              message="选择监控后会立即访问源站"
              description="检查会使用监控的当前配置及其指定代理。它只读取变量，不会创建事件或检查记录，不会修改监控状态或去重状态，不会执行规则，也不会发送通知。"
            />
            <Select
              showSearch
              value={monitorId}
              className="help-monitor-select"
              placeholder="选择监控"
              optionFilterProp="label"
              onChange={setMonitorId}
              options={monitors.data.map((monitor) => ({ label: `${monitor.name} · ${monitor.type}`, value: monitor.id }))}
            />
            <PageError error={snapshot.error as Error | null} onRetry={() => snapshot.refetch()} />
            {snapshot.isFetching && <Skeleton active paragraph={{ rows: 3 }} />}
            {liveSnapshot && (
              <Descriptions bordered size="small" column={{ xs: 1, md: 3 }}>
                <Descriptions.Item label="监控">{liveSnapshot.monitorName}</Descriptions.Item>
                <Descriptions.Item label="观测类型">{liveSnapshot.observationType || '—'}</Descriptions.Item>
                <Descriptions.Item label="本次抓取时间">{formatDate(fetchedAt)}</Descriptions.Item>
                <Descriptions.Item label="样本状态">{liveSnapshot.sampleAvailable ? <Tag color="green">已获取</Tag> : <Tag>无可预览内容</Tag>}</Descriptions.Item>
                <Descriptions.Item label="检查说明" span={2}>{liveSnapshot.message || '检查完成'}</Descriptions.Item>
                <Descriptions.Item label="完整 JSON 实时链接" span={3}>
                  <Space wrap>
                    <Text code copyable={{ text: snapshotURL }}>{snapshotURL}</Text>
                    <Button size="small" icon={<ApiOutlined />} href={snapshotURL} target="_blank">打开</Button>
                  </Space>
                </Descriptions.Item>
              </Descriptions>
            )}
            {liveSnapshot && !liveSnapshot.sampleAvailable && <Alert type="warning" showIcon message="源站没有可预览的内容" description={liveSnapshot.message || '本次检查成功，但源站没有返回可用于渲染变量的条目。'} />}
          </Space>
        ) : <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="创建监控后，可以在这里即时访问源站并检查变量取值。" />}
      </Card>

      <Card className="variable-reference-card">
        <Title level={4}>变量目录</Title>
        <Paragraph type="secondary">选择监控后会立即检查一次；之后仅在重新选择、点击“重新检查”或打开实时链接等主动操作时访问源站，不会在后台轮询。每个可用值都提供实时 JSON 链接，链接取值可能与当前表格不同。</Paragraph>
        <Tabs items={tabs} />
      </Card>
    </Space>
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
