import { Form, Input, InputNumber, Select, Switch, Typography } from 'antd';
import type { PluginConfigField } from '../types';

const { Text } = Typography;

export default function ConfigFields({ fields, configuredSecrets = [] }: { fields: PluginConfigField[]; configuredSecrets?: string[] }) {
  return (
    <>
      {fields.map((field) => {
        const configured = configuredSecrets.includes(field.key);
        const rules = field.required && !configured ? [{ required: true, message: `请填写${field.label}` }] : [];
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
  if (field.secret || field.type === 'secret') return <Input.Password autoComplete="new-password" placeholder={configured ? '已配置，留空保持原值' : '请输入敏感信息'} />;
  if (field.type === 'url') return <Input type="url" placeholder="https://" />;
  return <Input />;
}
