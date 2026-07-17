import { Button, Input, Select, Space, Typography } from 'antd';
import { DeleteOutlined, PlusOutlined } from '@ant-design/icons';
import type { RuleConditionGroup, RuleConditionLeaf, RuleConditionNode, RuleOperator } from '../types';

const { Text } = Typography;
const maxEditorDepth = 8;

const operatorOptions: Array<{ label: string; value: RuleOperator }> = [
  { label: '包含', value: 'contains' },
  { label: '不包含', value: 'not_contains' },
  { label: '等于', value: 'equals' },
  { label: '正则表达式', value: 'regex' },
  { label: '字段存在', value: 'exists' },
  { label: '在最近时间内', value: 'within_last' }
];

export function isConditionGroup(node: RuleConditionNode): node is RuleConditionGroup {
  return Array.isArray((node as RuleConditionGroup).conditions);
}

export function defaultCondition(field = ''): RuleConditionLeaf {
  return { field, operator: 'contains', value: '' };
}

export function defaultConditionGroup(field = ''): RuleConditionGroup {
  return { match: 'all', conditions: [defaultCondition(field)] };
}

export function normalizeConditionGroup(value: unknown, fallbackField = ''): RuleConditionGroup {
  if (!value || typeof value !== 'object') return defaultConditionGroup(fallbackField);
  const candidate = value as { match?: unknown; conditions?: unknown };
  if (!Array.isArray(candidate.conditions) || candidate.conditions.length === 0) return defaultConditionGroup(fallbackField);
  const normalizeNode = (node: unknown): RuleConditionNode => {
    if (node && typeof node === 'object' && Array.isArray((node as RuleConditionGroup).conditions)) {
      const group = node as Partial<RuleConditionGroup>;
      const children = (group.conditions ?? []).map(normalizeNode);
      return { match: group.match === 'any' ? 'any' : 'all', conditions: children.length ? children : [defaultCondition(fallbackField)] };
    }
    const leaf = (node ?? {}) as Partial<RuleConditionLeaf>;
    const operator = operatorOptions.some((item) => item.value === leaf.operator) ? leaf.operator as RuleOperator : 'contains';
    return { field: typeof leaf.field === 'string' ? leaf.field : fallbackField, operator, ...(operator === 'exists' ? {} : { value: typeof leaf.value === 'string' ? leaf.value : '' }) };
  };
  return {
    match: candidate.match === 'any' ? 'any' : 'all',
    conditions: candidate.conditions.map(normalizeNode)
  };
}

export function validateConditionGroup(group: RuleConditionGroup): string | null {
  let nodes = 0;
  const visit = (node: RuleConditionNode, depth: number): string | null => {
    nodes += 1;
    if (nodes > 200) return '一条规则最多包含 200 个条件和条件组。';
    if (depth > maxEditorDepth) return `条件组最多嵌套 ${maxEditorDepth} 层。`;
    if (isConditionGroup(node)) {
      if (!node.conditions.length) return '每个条件组至少需要一个条件。';
      for (const child of node.conditions) {
        const error = visit(child, depth + 1);
        if (error) return error;
      }
      return null;
    }
    if (!node.field.trim()) return '请为每个条件选择事件字段。';
    if (node.operator !== 'exists' && !(node.value ?? '').trim()) return '请填写每个条件的判断值。';
    if (node.operator === 'within_last' && !/^\s*(?:\d+(?:\.\d+)?(?:ns|us|µs|ms|s|m|h))+\s*$/.test(node.value ?? '')) {
      return '最近时间请使用 30s、2m、1h 或 24h 这样的时长。';
    }
    return null;
  };
  return visit(group, 0);
}

export default function ConditionBuilder(props: {
  value: RuleConditionGroup;
  onChange: (value: RuleConditionGroup) => void;
  fields: string[];
}) {
  return <ConditionGroupEditor group={props.value} onChange={props.onChange} fields={props.fields} depth={0} root />;
}

function ConditionGroupEditor(props: {
  group: RuleConditionGroup;
  onChange: (value: RuleConditionGroup) => void;
  fields: string[];
  depth: number;
  root?: boolean;
}) {
  const fallbackField = props.fields[0] ?? '';
  const updateNode = (index: number, node: RuleConditionNode) => {
    const conditions = [...props.group.conditions];
    conditions[index] = node;
    props.onChange({ ...props.group, conditions });
  };
  const removeNode = (index: number) => props.onChange({ ...props.group, conditions: props.group.conditions.filter((_, itemIndex) => itemIndex !== index) });
  const addLeaf = () => props.onChange({ ...props.group, conditions: [...props.group.conditions, defaultCondition(fallbackField)] });
  const addGroup = () => props.onChange({ ...props.group, conditions: [...props.group.conditions, defaultConditionGroup(fallbackField)] });

  return (
    <div className={`condition-group condition-group-depth-${Math.min(props.depth, 4)}`}>
      <div className="condition-group-header">
        <Space wrap>
          <Text strong>{props.root ? '以下条件' : '条件组'}满足</Text>
          <Select
            value={props.group.match}
            aria-label="条件关系"
            onChange={(match) => props.onChange({ ...props.group, match })}
            options={[{ label: '全部', value: 'all' }, { label: '任一', value: 'any' }]}
            style={{ width: 104 }}
          />
        </Space>
        <Text type="secondary">{props.group.match === 'all' ? 'AND' : 'OR'}</Text>
      </div>
      <div className="condition-group-children">
        {props.group.conditions.map((node, index) => (
          <div className="condition-node" key={index}>
            {isConditionGroup(node) ? (
              <ConditionGroupEditor group={node} onChange={(value) => updateNode(index, value)} fields={props.fields} depth={props.depth + 1} />
            ) : (
              <ConditionLeafEditor leaf={node} onChange={(value) => updateNode(index, value)} fields={props.fields} />
            )}
            <Button
              className="condition-node-delete"
              type="text"
              danger
              icon={<DeleteOutlined />}
              disabled={props.group.conditions.length === 1}
              onClick={() => removeNode(index)}
              aria-label="删除条件"
            />
          </div>
        ))}
      </div>
      <Space wrap className="condition-add-actions">
        <Button type="dashed" icon={<PlusOutlined />} onClick={addLeaf}>添加条件</Button>
        <Button type="dashed" icon={<PlusOutlined />} disabled={props.depth >= maxEditorDepth - 1} onClick={addGroup}>添加条件组</Button>
      </Space>
    </div>
  );
}

function ConditionLeafEditor(props: {
  leaf: RuleConditionLeaf;
  onChange: (value: RuleConditionLeaf) => void;
  fields: string[];
}) {
  const fieldOptions = props.fields.map((value) => ({ label: value, value }));
  if (props.leaf.field && !props.fields.includes(props.leaf.field)) {
    fieldOptions.unshift({ label: `${props.leaf.field}（当前监控不再提供）`, value: props.leaf.field });
  }
  const needsValue = props.leaf.operator !== 'exists';
  const supportsRelativeTime = isDateTimeField(props.leaf.field);
  const visibleOperatorOptions = operatorOptions.filter((item) => item.value !== 'within_last' || supportsRelativeTime || props.leaf.operator === 'within_last');
  return (
    <div className="condition-leaf">
      <Select
        showSearch
        value={props.leaf.field || undefined}
        placeholder="事件字段"
        options={fieldOptions}
        onChange={(field) => props.onChange({
          ...props.leaf,
          field,
          ...(props.leaf.operator === 'within_last' && !isDateTimeField(field) ? { operator: 'contains' as const, value: '' } : {})
        })}
      />
      <Select
        value={props.leaf.operator}
        options={visibleOperatorOptions}
        onChange={(operator: RuleOperator) => props.onChange({ ...props.leaf, operator, ...(operator === 'exists' ? { value: undefined } : { value: props.leaf.value ?? '' }) })}
      />
      {needsValue ? (
        <Input
          value={props.leaf.value}
          placeholder={props.leaf.operator === 'within_last' ? '例如 2m、30s、1h' : '判断值'}
          onChange={(event) => props.onChange({ ...props.leaf, value: event.target.value })}
        />
      ) : <div />}
    </div>
  );
}

function isDateTimeField(field: string) {
  return field === 'publishedAt' || field === 'event.time' || field.endsWith('.publishedAt');
}
