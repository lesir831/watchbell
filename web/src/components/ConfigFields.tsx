import { Form, Input, InputNumber, Select, Switch, Typography } from 'antd';
import { useEffect, useState } from 'react';
import type { PluginConfigField } from '../types';

const { Text } = Typography;

export default function ConfigFields({ fields, configuredSecrets = [] }: { fields: PluginConfigField[]; configuredSecrets?: string[] }) {
  return (
    <>
      {fields.map((field) => {
        const configured = configuredSecrets.includes(field.key);
        const rules = [
          ...(field.required && !configured ? [{ required: true, message: `请填写${field.label}` }] : []),
          ...(field.type === 'json' ? [{ validator: (_: unknown, value: unknown) => (
            (configured && (value == null || value === '')) || (value !== null && typeof value === 'object' && !Array.isArray(value))
          ) ? Promise.resolve() : Promise.reject(new Error(`${field.label}必须是 JSON 对象`)) }] : [])
        ];
        return (
          <Form.Item
            key={field.key}
            name={['config', field.key]}
            label={field.label}
            valuePropName={field.type === 'boolean' ? 'checked' : 'value'}
            rules={rules}
            extra={<>{field.description}{configured && field.secret ? <Text type="success"> 已配置；留空将保持原值。</Text> : null}</>}
          >
            {renderField(field, configured)}
          </Form.Item>
        );
      })}
    </>
  );
}

function renderField(field: PluginConfigField, configured: boolean) {
  if (field.type === 'boolean') return <Switch />;
  if (field.type === 'number') return <InputNumber min={field.key === 'timeoutSeconds' ? 1 : 0} className="full-width" />;
  if (field.type === 'string-list') return <Select mode="tags" tokenSeparators={[',']} placeholder="输入后按回车添加" />;
  if (field.type === 'json') return <JSONObjectInput />;
  if (field.type === 'textarea') return <Input.TextArea className="code-input" rows={8} spellCheck={false} />;
  if (field.secret || field.type === 'secret') return <Input.Password autoComplete="new-password" placeholder={configured ? '已配置，留空保持原值' : '请输入敏感信息'} />;
  if (field.type === 'url') return <Input type="url" placeholder="https://" />;
  return <Input />;
}

function JSONObjectInput({ value, onChange }: { value?: unknown; onChange?: (value: unknown) => void }) {
  const [text, setText] = useState(() => formatJSONValue(value));
  const [invalid, setInvalid] = useState(false);
  useEffect(() => { setText(formatJSONValue(value)); }, [value]);
  return <div><Input.TextArea className="code-input" rows={6} value={text} spellCheck={false} status={invalid ? 'error' : undefined} onChange={(event) => {
    const raw = event.target.value;
    setText(raw);
    try {
      const parsed = JSON.parse(raw || '{}');
      const valid = parsed && typeof parsed === 'object' && !Array.isArray(parsed);
      setInvalid(!valid);
      onChange?.(valid ? parsed : raw);
    } catch {
      setInvalid(true);
      onChange?.(raw);
    }
  }} />{invalid && <Text type="danger">请输入有效的 JSON 对象。</Text>}</div>;
}

function formatJSONValue(value: unknown) {
  if (typeof value === 'string') return value;
  return JSON.stringify(value ?? {}, null, 2);
}
