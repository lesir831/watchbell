import { useEffect, useMemo, useState } from 'react';
import {
  Alert,
  Button,
  Card,
  Descriptions,
  Empty,
  Select,
  Space,
  Table,
  Tabs,
  Tag,
  Typography
} from 'antd';
import { ApiOutlined, ReloadOutlined } from '@ant-design/icons';
import { useQuery } from '@tanstack/react-query';
import { api } from '../api';
import { PageError, relativeDate } from '../components/Common';
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
  const monitors = useQuery({ queryKey: ['monitors'], queryFn: api.listMonitors, refetchInterval: 30_000 });
  const snapshot = useQuery({
    queryKey: ['monitorVariables', monitorId],
    queryFn: () => api.monitorVariables(monitorId as number),
    enabled: Boolean(monitorId),
    refetchInterval: 30_000
  });

  useEffect(() => {
    if (!monitorId && monitors.data?.length) setMonitorId(monitors.data[0].id);
  }, [monitorId, monitors.data]);

  const selectedMonitor = monitors.data?.find((item) => item.id === monitorId);
  const tabs = useMemo(() => {
    if (!catalog.data) return [];
    return [
      {
        key: 'globals',
        label: '跨模块快捷变量',
        children: <VariableSection title="跨模块快捷变量" description="四类监控均可使用相同变量名；没有对应源字段时返回空字符串。" definitions={catalog.data.globals} snapshot={snapshot.data} />
      },
      {
        key: 'system',
        label: '系统上下文',
        children: <VariableSection title="系统上下文" description="由 WatchBell 在处理事件和发送通知时补充；rule.* 与 message.* 不属于原始事件快照。" definitions={catalog.data.system} snapshot={snapshot.data} />
      },
      ...catalog.data.modules.map((module) => ({
        key: module.id,
        label: module.name,
        children: <VariableSection
          title={`${module.name} 变量`}
          description={selectedMonitor?.type === module.id ? `当前取值来自监控“${selectedMonitor.name}”的最近事件。` : '选择这个模块的监控后，可以在表格中查看最近事件取值。'}
          definitions={module.variables}
          snapshot={selectedMonitor?.type === module.id ? snapshot.data : undefined}
        />
      }))
    ];
  }, [catalog.data, selectedMonitor, snapshot.data]);

  const error = (catalog.error || monitors.error || snapshot.error) as Error | null;
  const snapshotURL = monitorId ? `/api/monitors/${monitorId}/variables` : '';

  return (
    <Space direction="vertical" size={18} className="full-width help-page">
      <PageError error={error} onRetry={() => { catalog.refetch(); monitors.refetch(); if (monitorId) snapshot.refetch(); }} />
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

      <Card title="最近事件实时取值" extra={<Button icon={<ReloadOutlined />} disabled={!monitorId} loading={snapshot.isFetching} onClick={() => snapshot.refetch()}>刷新取值</Button>}>
        {monitors.data?.length ? (
          <Space direction="vertical" size={16} className="full-width">
            <Select
              showSearch
              value={monitorId}
              className="help-monitor-select"
              placeholder="选择监控"
              optionFilterProp="label"
              onChange={setMonitorId}
              options={monitors.data.map((monitor) => ({ label: `${monitor.name} · ${monitor.type}`, value: monitor.id }))}
            />
            {snapshot.data && (
              <Descriptions bordered size="small" column={{ xs: 1, md: 3 }}>
                <Descriptions.Item label="监控">{snapshot.data.monitorName}</Descriptions.Item>
                <Descriptions.Item label="最近事件">{snapshot.data.eventId ? `#${snapshot.data.eventId} · ${snapshot.data.eventType}` : '暂无事件'}</Descriptions.Item>
                <Descriptions.Item label="事件时间">{snapshot.data.eventCreatedAt ? relativeDate(snapshot.data.eventCreatedAt) : '—'}</Descriptions.Item>
                <Descriptions.Item label="完整 JSON 链接" span={3}>
                  <Space wrap>
                    <Text code copyable={{ text: snapshotURL }}>{snapshotURL}</Text>
                    <Button size="small" icon={<ApiOutlined />} href={snapshotURL} target="_blank">打开</Button>
                  </Space>
                </Descriptions.Item>
              </Descriptions>
            )}
            {snapshot.data && !snapshot.data.eventId && <Alert type="warning" showIcon message="这个监控还没有事件" description="运行监控并产生事件后，此处会展示真实变量值；打开帮助页本身不会抓取源站或发送通知。" />}
          </Space>
        ) : <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="创建监控并产生事件后，可以在这里查看变量实时取值。" />}
      </Card>

      <Card className="variable-reference-card">
        <Title level={4}>变量目录</Title>
        <Paragraph type="secondary">“实时取值”来自所选监控最近一次已持久化事件，并每 30 秒自动刷新。每个可用值都提供独立 JSON 链接。</Paragraph>
        <Tabs items={tabs} />
      </Card>
    </Space>
  );
}

function VariableSection(props: { title: string; description: string; definitions: VariableDefinition[]; snapshot?: VariableSnapshot }) {
  return (
    <div className="variable-section">
      <Title level={5}>{props.title}</Title>
      <Paragraph type="secondary">{props.description}</Paragraph>
      <Table<VariableDefinition>
        rowKey="key"
        pagination={false}
        dataSource={props.definitions}
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
            title: '最近取值', width: 260,
            render: (_, item) => <VariableValue value={props.snapshot?.values[item.key]} />
          },
          {
            title: '取值链接', width: 110, fixed: 'right',
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

function VariableValue({ value }: { value: unknown }) {
  if (value === undefined || value === null || value === '') return <Text type="secondary">—</Text>;
  const text = typeof value === 'string' ? value : JSON.stringify(value);
  const isURL = typeof value === 'string' && /^https?:\/\//i.test(value);
  return (
    <Text ellipsis={{ tooltip: text }} copyable={{ text }} className="variable-live-value">
      {isURL ? <a href={value as string} target="_blank" rel="noreferrer">{text}</a> : text}
    </Text>
  );
}
