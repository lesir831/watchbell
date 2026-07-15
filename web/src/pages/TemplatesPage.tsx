import { useMemo, useRef, useState } from 'react';
import { App as AntApp, Button, Card, Descriptions, Drawer, Form, Input, Modal, Popconfirm, Select, Space, Table, Tag, Typography } from 'antd';
import { DeleteOutlined, EditOutlined, EyeOutlined, PlusOutlined } from '@ant-design/icons';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from '../api';
import { EmptyState, PageError } from '../components/Common';
import type { NotificationTemplate, NotificationTemplateInput } from '../types';

const { Paragraph, Text } = Typography;

type PreviewRequest = Partial<NotificationTemplateInput> & { eventId?: number; requestId: number };
type PreviewResult = { eventId?: number; subject: string; body: string };

export default function TemplatesPage() {
  const queryClient = useQueryClient();
  const { message } = AntApp.useApp();
  const [editing, setEditing] = useState<NotificationTemplate | null>(null);
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [previewing, setPreviewing] = useState<NotificationTemplate | null>(null);
  const [previewEventId, setPreviewEventId] = useState<number | undefined>();
  const [preview, setPreview] = useState<PreviewResult | null>(null);
  const previewEventIdRef = useRef<number | undefined>();
  const previewRequestRef = useRef(0);
  const templates = useQuery({ queryKey: ['templates'], queryFn: api.listTemplates, refetchInterval: 30_000 });
  const plugins = useQuery({ queryKey: ['plugins'], queryFn: api.listPlugins });
  const events = useQuery({ queryKey: ['events'], queryFn: api.listEvents, refetchInterval: 30_000 });
  const variables = useMemo(() => Array.from(new Set((plugins.data ?? []).flatMap((plugin) => plugin.templateVariables))).sort(), [plugins.data]);
  const refresh = async () => Promise.all([queryClient.invalidateQueries({ queryKey: ['templates'] }), queryClient.invalidateQueries({ queryKey: ['auditLogs'] })]);
  const saveMutation = useMutation({
    mutationFn: (payload: { id?: number; input: NotificationTemplateInput }) => payload.id ? api.updateTemplate(payload.id, payload.input) : api.createTemplate(payload.input),
    onSuccess: async () => { await refresh(); setDrawerOpen(false); setEditing(null); message.success('模板已保存'); }
  });
  const deleteMutation = useMutation({
    mutationFn: api.deleteTemplate,
    onSuccess: async () => { await refresh(); message.success('模板已归档'); },
    onError: (error: Error) => message.error(error.message)
  });
  const previewMutation = useMutation({
    mutationFn: async ({ requestId, eventId, ...template }: PreviewRequest) => ({
      requestId,
      eventId,
      result: await api.previewTemplate({ ...template, eventId })
    }),
    onSuccess: ({ requestId, eventId, result }) => {
      if (requestId === previewRequestRef.current && eventId === previewEventIdRef.current) {
        setPreview({ eventId, ...result });
      }
    },
    onError: (error: Error, variables) => {
      if (variables.requestId === previewRequestRef.current && variables.eventId === previewEventIdRef.current) {
        setPreview(null);
        message.error(error.message);
      }
    }
  });
  const selectPreviewEvent = (eventId?: number) => {
    previewRequestRef.current += 1;
    previewEventIdRef.current = eventId;
    setPreviewEventId(eventId);
    setPreview(null);
    previewMutation.reset();
  };
  const openPreview = (record: NotificationTemplate) => {
    const eventId = events.data?.[0]?.id;
    previewRequestRef.current += 1;
    previewEventIdRef.current = eventId;
    setPreviewing(record);
    setPreviewEventId(eventId);
    setPreview(null);
    previewMutation.reset();
  };
  const closePreview = () => {
    previewRequestRef.current += 1;
    previewEventIdRef.current = undefined;
    setPreviewing(null);
    setPreviewEventId(undefined);
    setPreview(null);
    previewMutation.reset();
  };
  const generatePreview = () => {
    if (!previewing) return;
    const requestId = previewRequestRef.current + 1;
    previewRequestRef.current = requestId;
    setPreview(null);
    previewMutation.mutate({ requestId, subjectTemplate: previewing.subjectTemplate, bodyTemplate: previewing.bodyTemplate, eventId: previewEventId });
  };
  const openNew = () => { setEditing(null); setDrawerOpen(true); };
  return (
    <Space direction="vertical" size={16} className="full-width">
      <PageError error={templates.error as Error | null} onRetry={() => templates.refetch()} />
      <div className="page-toolbar"><Button type="primary" icon={<PlusOutlined />} onClick={openNew}>新建模板</Button></div>
      {!templates.data?.length && !templates.isLoading ? <Card><EmptyState title="还没有通知模板" description="创建模板以控制通知标题和正文。" action={<Button type="primary" onClick={openNew}>创建模板</Button>} /></Card> : (
        <Table<NotificationTemplate> rowKey="id" loading={templates.isLoading} dataSource={templates.data ?? []} scroll={{ x: 760 }} columns={[
          { title: '名称', dataIndex: 'name', width: 180, render: (value, record) => <Space>{value}{record.isDefault && <Tag color="blue">默认</Tag>}</Space> },
          { title: '标题模板', dataIndex: 'subjectTemplate', ellipsis: true },
          { title: '操作', width: 280, render: (_, record) => <Space wrap>
            <Button icon={<EyeOutlined />} onClick={() => openPreview(record)}>预览</Button>
            <Button icon={<EditOutlined />} onClick={() => { setEditing(record); setDrawerOpen(true); }}>编辑</Button>
            <Popconfirm title="归档这个模板？" description="使用它的规则会改用系统默认模板。" disabled={record.isDefault} onConfirm={() => deleteMutation.mutate(record.id)}><Button danger disabled={record.isDefault} icon={<DeleteOutlined />} aria-label={`归档 ${record.name}`} /></Popconfirm>
          </Space> }
        ]} />
      )}
      <TemplateDrawer open={drawerOpen} record={editing} variables={variables} saving={saveMutation.isPending} error={saveMutation.error as Error | null} onClose={() => { setDrawerOpen(false); saveMutation.reset(); }} onSave={(input) => saveMutation.mutate({ id: editing?.id, input })} />
      <Modal open={previewing !== null} title="通知预览" footer={null} onCancel={closePreview}>
        <Space direction="vertical" size={16} className="full-width">
          <div>
            <Text strong>预览数据</Text>
            <Select
              className="preview-event-select"
              allowClear
              value={previewEventId}
              placeholder="不选择时使用内置样例"
              options={(events.data ?? []).map((event) => ({ label: `事件 #${event.id} · ${event.type}`, value: event.id }))}
              onChange={selectPreviewEvent}
            />
          </div>
          <Button type="primary" loading={previewMutation.isPending} onClick={generatePreview}>生成预览</Button>
          {preview && preview.eventId === previewEventId && <Descriptions column={1} bordered size="small"><Descriptions.Item label="标题">{preview.subject}</Descriptions.Item><Descriptions.Item label="正文"><pre className="message-preview">{preview.body}</pre></Descriptions.Item></Descriptions>}
        </Space>
      </Modal>
    </Space>
  );
}

function TemplateDrawer(props: { open: boolean; record: NotificationTemplate | null; variables: string[]; saving: boolean; error: Error | null; onClose: () => void; onSave: (input: NotificationTemplateInput) => void }) {
  const [form] = Form.useForm();
  const [insertTarget, setInsertTarget] = useState<'subjectTemplate' | 'bodyTemplate'>('bodyTemplate');
  const setInitial = () => form.setFieldsValue({
    name: props.record?.name ?? '', subjectTemplate: props.record?.subjectTemplate ?? '${monitor.name}: ${event.type}',
    bodyTemplate: props.record?.bodyTemplate ?? '监控：${monitor.name}\n时间：${event.time}\n\n${rss.title}${testflight.message}${webpage.summary}${github.release.name}\n${rss.link}${testflight.url}${webpage.url}${github.release.url}'
  });
  return (
    <Drawer title={props.record ? '编辑模板' : '新建模板'} open={props.open} onClose={props.onClose} width={720} destroyOnClose afterOpenChange={(open) => { if (open) setInitial(); }} footer={<div className="drawer-footer"><Button onClick={props.onClose}>取消</Button><Button type="primary" loading={props.saving} onClick={() => form.submit()}>保存模板</Button></div>}>
      <PageError error={props.error} />
      <Form form={form} layout="vertical" onFinish={(values) => props.onSave(values)}>
        <Form.Item name="name" label="名称" rules={[{ required: true, whitespace: true }]}><Input /></Form.Item>
        <Form.Item name="subjectTemplate" label="标题" rules={[{ required: true, whitespace: true }]}><Input onFocus={() => setInsertTarget('subjectTemplate')} /></Form.Item>
        <Form.Item name="bodyTemplate" label="正文" rules={[{ required: true, whitespace: true }]}><Input.TextArea rows={12} className="code-input" onFocus={() => setInsertTarget('bodyTemplate')} /></Form.Item>
      </Form>
      <Card size="small" title="可用变量" className="variable-card">
        <Paragraph type="secondary">先点击标题或正文，再点击变量；它会直接插入当前编辑区。</Paragraph>
        <Space wrap>{['monitor.name', 'event.type', 'event.time', ...props.variables].map((item) => <Tag className="copy-tag" key={item} onClick={() => {
          const token = `\${${item}}`;
          form.setFieldValue(insertTarget, `${form.getFieldValue(insertTarget) ?? ''}${token}`);
          form.validateFields([insertTarget]).catch(() => undefined);
        }}>{`\${${item}}`}</Tag>)}</Space>
      </Card>
    </Drawer>
  );
}
