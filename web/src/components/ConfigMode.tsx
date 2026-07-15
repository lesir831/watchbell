import { Form, Input, Segmented, Typography, type FormInstance } from 'antd';

const { Text } = Typography;

export function ConfigMode(props: {
  form: FormInstance;
  advanced: boolean;
  onChange: (advanced: boolean) => void;
}) {
  const changeMode = async (value: string | number) => {
    const nextAdvanced = value === 'json';
    if (nextAdvanced) {
      const current = props.form.getFieldValue('config') ?? {};
      props.form.setFieldValue('rawConfig', JSON.stringify(current, null, 2));
      props.onChange(true);
      return;
    }
    try {
      await props.form.validateFields(['rawConfig']);
      props.form.setFieldValue('config', parseConfigJSON(props.form.getFieldValue('rawConfig')));
      props.onChange(false);
    } catch {
      // Keep the JSON editor visible so the validation message remains actionable.
    }
  };

  return (
    <div className="config-mode-row">
      <div><Text strong>配置方式</Text><br /><Text type="secondary">高级模式适合粘贴未在表单中展示的插件参数。</Text></div>
      <Segmented value={props.advanced ? 'json' : 'form'} options={[{ label: '结构化表单', value: 'form' }, { label: '高级 JSON', value: 'json' }]} onChange={changeMode} />
    </div>
  );
}

export function AdvancedConfigField() {
  return (
    <Form.Item
      name="rawConfig"
      label="配置 JSON"
      validateTrigger="onBlur"
      rules={[{
        validator: (_, value) => {
          try {
            parseConfigJSON(value);
            return Promise.resolve();
          } catch (error) {
            return Promise.reject(error);
          }
        }
      }]}
      extra="保存时仍会执行服务端字段校验；敏感字段留空或省略会保留已有值。"
    >
      <Input.TextArea className="code-input" rows={16} spellCheck={false} />
    </Form.Item>
  );
}

export function parseConfigJSON(value: unknown): Record<string, unknown> {
  let parsed: unknown;
  try {
    parsed = JSON.parse(String(value ?? '{}'));
  } catch {
    throw new Error('请输入有效的 JSON。');
  }
  if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
    throw new Error('配置必须是 JSON 对象。');
  }
  return parsed as Record<string, unknown>;
}
