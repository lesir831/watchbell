import { useEffect, useMemo, useRef, useState } from 'react';
import { Alert, App as AntApp, Button, Card, Collapse, Drawer, Form, Input, Popconfirm, Select, Tag } from 'antd';
import { ArrowRightOutlined, CopyOutlined, DeleteOutlined, EditOutlined, PlusOutlined, SendOutlined } from '@ant-design/icons';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from '../api';
import { EmptyState, eventTitle, formatDate, PageError, PageHeader } from '../components/Common';
import { TemplateVariableEditor, type TemplateVariableEditorHandle } from '../components/TemplateVariableEditor';
import type { NotificationTemplate, NotificationTemplateInput, VariableCatalog, VariableDefinition } from '../types';

type PreviewRequest = Partial<NotificationTemplateInput> & { eventId?: number; requestId: number };
type PreviewResult = { eventId?: number; subject: string; body: string };

export default function TemplatesPage() {
  const queryClient = useQueryClient();
  const { message } = AntApp.useApp();
  const [editing, setEditing] = useState<NotificationTemplate | null>(null);
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [previewing, setPreviewing] = useState<NotificationTemplate | null>(null);
  const [previewEventId, setPreviewEventId] = useState<number | undefined>();
  const [previewChannelId, setPreviewChannelId] = useState<number | undefined>();
  const [preview, setPreview] = useState<PreviewResult | null>(null);
  const previewEventIdRef = useRef<number | undefined>();
  const previewRequestRef = useRef(0);
  const templates = useQuery({ queryKey: ['templates'], queryFn: api.listTemplates, refetchInterval: 30_000 });
  const variableCatalog = useQuery({ queryKey: ['variableCatalog'], queryFn: api.variableCatalog });
  const events = useQuery({ queryKey: ['events'], queryFn: api.listEvents, refetchInterval: 30_000 });
  const monitors = useQuery({ queryKey: ['monitors'], queryFn: api.listMonitors, refetchInterval: 30_000 });
  const channels = useQuery({ queryKey: ['channels'], queryFn: api.listChannels, refetchInterval: 30_000 });
  const variableGroups = useMemo(() => templateVariableGroups(variableCatalog.data), [variableCatalog.data]);
  const monitorByID = useMemo(() => new Map((monitors.data ?? []).map((monitor) => [monitor.id, monitor.name])), [monitors.data]);
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
  const sendPreviewMutation = useMutation({
    mutationFn: api.sendTemplatePreview,
    onSuccess: async (attempt) => {
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['notificationAttempts'] }),
        queryClient.invalidateQueries({ queryKey: ['auditLogs'] })
      ]);
      message.success(`预览已通过 ${attempt.channelName} 发送`);
    },
    onError: (error: Error) => message.error(error.message)
  });
  const selectPreviewEvent = (eventId?: number) => {
    const requestId = previewRequestRef.current + 1;
    previewRequestRef.current = requestId;
    previewEventIdRef.current = eventId;
    setPreviewEventId(eventId);
    setPreview(null);
    previewMutation.reset();
    if (previewing) {
      previewMutation.mutate({ requestId, subjectTemplate: previewing.subjectTemplate, bodyTemplate: previewing.bodyTemplate, eventId });
    }
  };
  const openPreview = (record: NotificationTemplate) => {
    const eventId = events.data?.[0]?.id;
    const requestId = previewRequestRef.current + 1;
    previewRequestRef.current = requestId;
    previewEventIdRef.current = eventId;
    setPreviewing(record);
    setPreviewEventId(eventId);
    setPreview(null);
    previewMutation.reset();
    previewMutation.mutate({ requestId, subjectTemplate: record.subjectTemplate, bodyTemplate: record.bodyTemplate, eventId });
  };
  const openNew = () => { setEditing(null); setDrawerOpen(true); };
  useEffect(() => {
    if (!previewing && templates.data?.length) openPreview(templates.data[0]);
  // Select once when the first real template list arrives.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [templates.data, previewing]);
  useEffect(() => {
    const enabled = (channels.data ?? []).filter((channel) => channel.enabled);
    if (!enabled.some((channel) => channel.id === previewChannelId)) setPreviewChannelId(enabled[0]?.id);
  }, [channels.data, previewChannelId]);
  const selectedEvent = events.data?.find((event) => event.id === previewEventId);
  const selectedMonitor = monitors.data?.find((monitor) => monitor.id === selectedEvent?.monitorId);
  const copyPreview = async () => {
    if (!preview) return;
    await navigator.clipboard.writeText(`${preview.subject}\n\n${preview.body}`);
    message.success('预览内容已复制');
  };
  return (
    <div className="design-page">
      <PageHeader
        eyebrow="内容编排"
        title="通知模板"
        description="在真实事件变量的上下文中预览消息，避免模板保存后才发现字段缺失。"
        actions={<Button className="design-primary" type="primary" icon={<PlusOutlined />} onClick={openNew}>新建模板</Button>}
      />
      <PageError error={(templates.error || variableCatalog.error || events.error || monitors.error || channels.error) as Error | null} onRetry={() => { templates.refetch(); variableCatalog.refetch(); events.refetch(); monitors.refetch(); channels.refetch(); }} />
      {!templates.data?.length && !templates.isLoading ? <div className="empty-panel"><EmptyState title="还没有通知模板" description="创建模板以控制通知标题和正文。" action={<Button type="primary" onClick={openNew}>创建模板</Button>} /></div> : (
        <div className="template-layout">
          <section className="template-list">
            <div className="template-list-head"><h2>模板列表</h2><span>选择后更新右侧预览</span></div>
            {(templates.data ?? []).map((item) => (
              <button key={item.id} type="button" className={`template-option ${previewing?.id === item.id ? 'active' : ''}`} onClick={() => openPreview(item)}>
                <span><strong>{item.name}{item.isDefault && <Tag>默认</Tag>}</strong><small>{item.subjectTemplate}</small></span><ArrowRightOutlined />
              </button>
            ))}
          </section>
          <section className="preview-panel">
            <div className="preview-toolbar">
              <div><h2>消息预览</h2><span>预览数据：{selectedEvent ? eventTitle(selectedEvent.payload, selectedMonitor?.name) : '内置样例'}</span></div>
              <div className="inline-actions">
                <Button className="mini-action" icon={<CopyOutlined />} disabled={!preview} onClick={copyPreview}>复制内容</Button>
                <Button className="mini-action" icon={<SendOutlined />} loading={sendPreviewMutation.isPending} disabled={!previewing || !preview || !previewChannelId} onClick={() => previewing && previewChannelId && sendPreviewMutation.mutate({ templateId: previewing.id, channelId: previewChannelId, eventId: previewEventId })}>发送预览</Button>
                <Button className="mini-action" icon={<EditOutlined />} disabled={!previewing} onClick={() => { if (previewing) { setEditing(previewing); setDrawerOpen(true); } }}>编辑</Button>
                <Popconfirm title="归档这个模板？" description="使用它的规则会改用系统默认模板。" disabled={!previewing || previewing.isDefault} onConfirm={() => previewing && deleteMutation.mutate(previewing.id)}><Button className="mini-action icon-only" danger disabled={!previewing || previewing.isDefault} icon={<DeleteOutlined />} aria-label="归档当前模板" /></Popconfirm>
              </div>
            </div>
            <div className="preview-event-row">
              <label><span>预览数据</span><Select allowClear showSearch optionFilterProp="label" value={previewEventId} placeholder="搜索事件标题，留空使用内置样例" notFoundContent="没有匹配的事件标题" options={(events.data ?? []).map((event) => ({ label: eventTitle(event.payload, monitorByID.get(event.monitorId)), value: event.id }))} onChange={selectPreviewEvent} /></label>
              <label><span>发送渠道</span><Select value={previewChannelId} placeholder="选择已启用渠道" options={(channels.data ?? []).filter((channel) => channel.enabled).map((channel) => ({ label: `${channel.name} · ${channel.type}`, value: channel.id }))} onChange={setPreviewChannelId} /></label>
            </div>
            {previewMutation.isPending && !preview ? <Card loading className="message-preview-card" /> : preview ? (
              <article className="message-preview-card">
                <div className="message-preview-head"><span>WATCHBELL 通知</span><strong>{preview.subject}</strong></div>
                <div className="message-preview-body"><p>{preview.body}</p><dl><dt>监控</dt><dd>{selectedMonitor?.name ?? '示例监控'}</dd><dt>事件时间</dt><dd>{formatDate(selectedEvent?.createdAt)}</dd></dl></div>
              </article>
            ) : <div className="preview-placeholder">选择模板或事件后将自动生成预览。</div>}
            <Alert className="preview-notice" type="info" showIcon message="变量使用 ${path} 语法；已识别变量会高亮，事件中没有取值的变量在预览中显示为空。" />
          </section>
        </div>
      )}
      <TemplateDrawer open={drawerOpen} record={editing} variableGroups={variableGroups} variablesLoading={variableCatalog.isLoading} saving={saveMutation.isPending} error={saveMutation.error as Error | null} onClose={() => { setDrawerOpen(false); saveMutation.reset(); }} onSave={(input) => saveMutation.mutate({ id: editing?.id, input })} />
    </div>
  );
}

interface TemplateVariableGroup {
  id: string;
  name: string;
  variables: VariableDefinition[];
}

function templateVariableGroups(catalog?: VariableCatalog): TemplateVariableGroup[] {
  if (!catalog) return [];
  const inTemplates = (variables: VariableDefinition[]) => variables.filter((item) => item.availableIn.includes('template'));
  return [
    { id: 'globals', name: '跨模块通用', variables: inTemplates(catalog.globals) },
    { id: 'system', name: '系统上下文', variables: inTemplates(catalog.system) },
    ...catalog.modules.map((module) => ({ id: module.id, name: module.name, variables: inTemplates(module.variables) }))
  ].filter((group) => group.variables.length > 0);
}

function TemplateDrawer(props: { open: boolean; record: NotificationTemplate | null; variableGroups: TemplateVariableGroup[]; variablesLoading: boolean; saving: boolean; error: Error | null; onClose: () => void; onSave: (input: NotificationTemplateInput) => void }) {
  const [form] = Form.useForm();
  const [insertTarget, setInsertTarget] = useState<'subjectTemplate' | 'bodyTemplate'>('bodyTemplate');
  const subjectEditor = useRef<TemplateVariableEditorHandle>(null);
  const bodyEditor = useRef<TemplateVariableEditorHandle>(null);
  const variables = useMemo(() => props.variableGroups.flatMap((group) => group.variables), [props.variableGroups]);
  const setInitial = () => form.setFieldsValue({
    name: props.record?.name ?? '', subjectTemplate: props.record?.subjectTemplate ?? '${monitor.name}: ${event.type}',
    bodyTemplate: props.record?.bodyTemplate ?? '监控：${monitor.name}\n时间：${event.time}\n\n${rss.title}${testflight.message}${webpage.summary}${github.release.name}\n${rss.link}${testflight.url}${webpage.url}${github.release.url}'
  });
  const insertVariable = (key: string) => {
    const editor = insertTarget === 'subjectTemplate' ? subjectEditor.current : bodyEditor.current;
    editor?.insertVariable(key);
    form.validateFields([insertTarget]).catch(() => undefined);
  };
  return (
    <Drawer title={props.record ? '编辑模板' : '新建模板'} open={props.open} onClose={props.onClose} width={720} destroyOnClose afterOpenChange={(open) => { if (open) setInitial(); }} footer={<div className="drawer-footer"><Button onClick={props.onClose}>取消</Button><Button type="primary" loading={props.saving} onClick={() => form.submit()}>保存模板</Button></div>}>
      <PageError error={props.error} />
      <Form form={form} layout="vertical" onFinish={(values) => props.onSave(values)}>
        <Form.Item name="name" label="名称" rules={[{ required: true, whitespace: true }]}><Input /></Form.Item>
        <Form.Item name="subjectTemplate" label="标题" rules={[{ required: true, whitespace: true }]}>
          <TemplateVariableEditor ref={subjectEditor} variables={variables} placeholder="输入 $ 插入变量" onFocus={() => setInsertTarget('subjectTemplate')} />
        </Form.Item>
        <Form.Item name="bodyTemplate" label="正文" rules={[{ required: true, whitespace: true }]}>
          <TemplateVariableEditor ref={bodyEditor} variables={variables} multiline rows={12} placeholder="输入正文；输入 $ 可搜索变量" onFocus={() => setInsertTarget('bodyTemplate')} />
        </Form.Item>
      </Form>
      <Card size="small" className="variable-card template-variable-browser">
        <div className="template-variable-browser-head">
          <div><strong>可用变量</strong><span>输入 $ 搜索，或点击下方变量插入光标位置</span></div>
          <div className="template-variable-browser-actions"><Tag color="blue">插入到{insertTarget === 'subjectTemplate' ? '标题' : '正文'}</Tag><a href="#/help">完整变量帮助</a></div>
        </div>
        {props.variablesLoading ? <div className="template-variable-browser-empty">正在载入变量目录…</div> : props.variableGroups.length ? (
          <Collapse
            ghost
            size="small"
            defaultActiveKey={['globals', 'system']}
            items={props.variableGroups.map((group) => ({
              key: group.id,
              label: <span className="template-variable-group-label"><strong>{group.name}</strong><small>{group.variables.length}</small></span>,
              children: <div className="template-variable-grid">{group.variables.map((item) => (
                <button
                  type="button"
                  key={item.key}
                  title={item.description}
                  onMouseDown={(event) => event.preventDefault()}
                  onClick={() => insertVariable(item.key)}
                >
                  <span>{item.label}</span><code>{`\${${item.key}}`}</code>
                </button>
              ))}</div>
            }))}
          />
        ) : <div className="template-variable-browser-empty">变量目录暂不可用，请稍后重试。</div>}
      </Card>
    </Drawer>
  );
}
