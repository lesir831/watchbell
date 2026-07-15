import { useMemo, useState } from 'react';
import { App as AntApp, Button, Card, Descriptions, Drawer, Form, Input, Modal, Popconfirm, Space, Table, Tag, Typography } from 'antd';
import { DeleteOutlined, EditOutlined, EyeOutlined, PlusOutlined } from '@ant-design/icons';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from '../api';
import { EmptyState, PageError } from '../components/Common';
import type { NotificationTemplate, NotificationTemplateInput } from '../types';

const { Paragraph, Text } = Typography;

export default function TemplatesPage() {
  const queryClient = useQueryClient();
  const { message } = AntApp.useApp();
  const [editing, setEditing] = useState<NotificationTemplate | null>(null);
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [preview, setPreview] = useState<{ subject: string; body: string } | null>(null);
  const templates = useQuery({ queryKey: ['templates'], queryFn: api.listTemplates });
  const plugins = useQuery({ queryKey: ['plugins'], queryFn: api.listPlugins });
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
  const previewMutation = useMutation({ mutationFn: api.previewTemplate, onSuccess: setPreview, onError: (error: Error) => message.error(error.message) });
  const openNew = () => { setEditing(null); setDrawerOpen(true); };
  return (
    <Space direction="vertical" size={16} className="full-width">
      <PageError error={templates.error as Error | null} onRetry={() => templates.refetch()} />
      <div className="page-toolbar"><Button type="primary" icon={<PlusOutlined />} onClick={openNew}>新建模板</Button></div>
      {!templates.data?.length && !templates.isLoading ? <Card><EmptyState title="还没有通知模板" description="创建模板以控制通知标题和正文。" action={<Button type="primary" onClick={openNew}>创建模板</Button>} /></Card> : (
        <Table<NotificationTemplate> rowKey="id" loading={templates.isLoading} dataSource={templates.data ?? []} scroll={{ x: 760 }} columns={[
          { title: '名称', dataIndex: 'name', width: 180, render: (value, record) => <Space>{value}{record.id === 1 && <Tag color="blue">默认</Tag>}</Space> },
          { title: '标题模板', dataIndex: 'subjectTemplate', ellipsis: true },
          { title: '操作', width: 280, render: (_, record) => <Space wrap>
            <Button icon={<EyeOutlined />} loading={previewMutation.isPending} onClick={() => previewMutation.mutate(record)}>预览</Button>
            <Button icon={<EditOutlined />} onClick={() => { setEditing(record); setDrawerOpen(true); }}>编辑</Button>
            <Popconfirm title="归档这个模板？" disabled={record.id === 1} onConfirm={() => deleteMutation.mutate(record.id)}><Button danger disabled={record.id === 1} icon={<DeleteOutlined />} aria-label={`归档 ${record.name}`} /></Popconfirm>
          </Space> }
        ]} />
      )}
      <TemplateDrawer open={drawerOpen} record={editing} variables={variables} saving={saveMutation.isPending} error={saveMutation.error as Error | null} onClose={() => { setDrawerOpen(false); saveMutation.reset(); }} onSave={(input) => saveMutation.mutate({ id: editing?.id, input })} />
      <Modal open={preview !== null} title="通知预览" footer={null} onCancel={() => setPreview(null)}>
        <Descriptions column={1} bordered size="small"><Descriptions.Item label="标题">{preview?.subject}</Descriptions.Item><Descriptions.Item label="正文"><pre className="message-preview">{preview?.body}</pre></Descriptions.Item></Descriptions>
      </Modal>
    </Space>
  );
}

function TemplateDrawer(props: { open: boolean; record: NotificationTemplate | null; variables: string[]; saving: boolean; error: Error | null; onClose: () => void; onSave: (input: NotificationTemplateInput) => void }) {
  const [form] = Form.useForm();
  const setInitial = () => form.setFieldsValue({
    name: props.record?.name ?? '', subjectTemplate: props.record?.subjectTemplate ?? '${monitor.name}: ${event.type}',
    bodyTemplate: props.record?.bodyTemplate ?? '监控：${monitor.name}\n时间：${event.time}\n\n${rss.title}${testflight.message}${webpage.summary}${github.release.name}\n${rss.link}${testflight.url}${webpage.url}${github.release.url}'
  });
  return (
    <Drawer title={props.record ? '编辑模板' : '新建模板'} open={props.open} onClose={props.onClose} width={720} destroyOnClose afterOpenChange={(open) => { if (open) setInitial(); }} footer={<div className="drawer-footer"><Button onClick={props.onClose}>取消</Button><Button type="primary" loading={props.saving} onClick={() => form.submit()}>保存模板</Button></div>}>
      <PageError error={props.error} />
      <Form form={form} layout="vertical" onFinish={(values) => props.onSave(values)}>
        <Form.Item name="name" label="名称" rules={[{ required: true, whitespace: true }]}><Input /></Form.Item>
        <Form.Item name="subjectTemplate" label="标题" rules={[{ required: true, whitespace: true }]}><Input /></Form.Item>
        <Form.Item name="bodyTemplate" label="正文" rules={[{ required: true, whitespace: true }]}><Input.TextArea rows={12} className="code-input" /></Form.Item>
      </Form>
      <Card size="small" title="可用变量" className="variable-card">
        <Paragraph type="secondary">点击变量即可复制，然后粘贴到标题或正文中。</Paragraph>
        <Space wrap>{['monitor.name', 'event.type', 'event.time', ...props.variables].map((item) => <Tag className="copy-tag" key={item} onClick={() => navigator.clipboard.writeText(`\${${item}}`)}>{`\${${item}}`}</Tag>)}</Space>
      </Card>
    </Drawer>
  );
}
