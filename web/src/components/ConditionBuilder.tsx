import { useState } from 'react';
import type { DragEvent as ReactDragEvent, KeyboardEvent as ReactKeyboardEvent } from 'react';
import { Button, Input, Select, Space, Typography } from 'antd';
import { ArrowDownOutlined, ArrowUpOutlined, DeleteOutlined, HolderOutlined, PlusOutlined } from '@ant-design/icons';
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

function normalizeMatchMode(value: unknown): RuleConditionGroup['match'] {
  return typeof value === 'string' && value.trim().toLowerCase() === 'any' ? 'any' : 'all';
}

function normalizeOperator(value: unknown): RuleOperator {
  const normalized = typeof value === 'string' ? value.trim().toLowerCase() : '';
  return operatorOptions.some((item) => item.value === normalized) ? normalized as RuleOperator : 'contains';
}

export function normalizeConditionGroup(value: unknown, fallbackField = ''): RuleConditionGroup {
  if (!value || typeof value !== 'object') return defaultConditionGroup(fallbackField);
  const candidate = value as { match?: unknown; conditions?: unknown };
  if (!Array.isArray(candidate.conditions) || candidate.conditions.length === 0) return defaultConditionGroup(fallbackField);
  const normalizeNode = (node: unknown): RuleConditionNode => {
    if (node && typeof node === 'object' && Array.isArray((node as RuleConditionGroup).conditions)) {
      const group = node as Partial<RuleConditionGroup>;
      const children = (group.conditions ?? []).map(normalizeNode);
      return { match: normalizeMatchMode(group.match), conditions: children.length ? children : [defaultCondition(fallbackField)] };
    }
    const leaf = (node ?? {}) as Partial<RuleConditionLeaf>;
    const operator = normalizeOperator(leaf.operator);
    return { field: typeof leaf.field === 'string' ? leaf.field : fallbackField, operator, ...(operator === 'exists' ? {} : { value: typeof leaf.value === 'string' ? leaf.value : '' }) };
  };
  return {
    match: normalizeMatchMode(candidate.match),
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
  const [draggedIndex, setDraggedIndex] = useState<number | null>(null);
  const [dropIndex, setDropIndex] = useState<number | null>(null);
  const [reorderAnnouncement, setReorderAnnouncement] = useState('');
  const updateNode = (index: number, node: RuleConditionNode) => {
    const conditions = [...props.group.conditions];
    conditions[index] = node;
    props.onChange({ ...props.group, conditions });
  };
  const removeNode = (index: number) => props.onChange({ ...props.group, conditions: props.group.conditions.filter((_, itemIndex) => itemIndex !== index) });
  const addLeaf = () => props.onChange({ ...props.group, conditions: [...props.group.conditions, defaultCondition(fallbackField)] });
  const addGroup = () => props.onChange({ ...props.group, conditions: [...props.group.conditions, defaultConditionGroup(fallbackField)] });
  const describeNode = (index: number) => isConditionGroup(props.group.conditions[index]) ? '条件组' : '条件';
  const reorderNode = (fromIndex: number, toIndex: number) => {
    if (fromIndex === toIndex || fromIndex < 0 || toIndex < 0 || fromIndex >= props.group.conditions.length || toIndex >= props.group.conditions.length) return;
    const conditions = [...props.group.conditions];
    const [node] = conditions.splice(fromIndex, 1);
    conditions.splice(toIndex, 0, node);
    setReorderAnnouncement(`${isConditionGroup(node) ? '条件组' : '条件'}已移动到第 ${toIndex + 1} 项。`);
    props.onChange({ ...props.group, conditions });
  };
  const moveNode = (index: number, direction: -1 | 1) => reorderNode(index, index + direction);
  const beginDrag = (event: ReactDragEvent<HTMLButtonElement>, index: number) => {
    event.stopPropagation();
    event.dataTransfer.effectAllowed = 'move';
    event.dataTransfer.setData('text/plain', `condition-node-${index}`);
    setDraggedIndex(index);
    setDropIndex(index);
  };
  const updateDropTarget = (event: ReactDragEvent<HTMLElement>, insertionIndex: number) => {
    if (draggedIndex === null) return;
    event.preventDefault();
    event.stopPropagation();
    event.dataTransfer.dropEffect = 'move';
    setDropIndex(insertionIndex);
  };
  const dropAt = (event: ReactDragEvent<HTMLElement>, insertionIndex: number) => {
    if (draggedIndex === null) return;
    event.preventDefault();
    event.stopPropagation();
    const destinationIndex = draggedIndex < insertionIndex ? insertionIndex - 1 : insertionIndex;
    reorderNode(draggedIndex, destinationIndex);
    setDraggedIndex(null);
    setDropIndex(null);
  };
  const updateDropTargetFromNode = (event: ReactDragEvent<HTMLDivElement>, index: number) => {
    if (draggedIndex === null) return;
    const bounds = event.currentTarget.getBoundingClientRect();
    updateDropTarget(event, event.clientY < bounds.top + bounds.height / 2 ? index : index + 1);
  };
  const dropOnNode = (event: ReactDragEvent<HTMLDivElement>, index: number) => {
    if (draggedIndex === null) return;
    const bounds = event.currentTarget.getBoundingClientRect();
    dropAt(event, event.clientY < bounds.top + bounds.height / 2 ? index : index + 1);
  };
  const finishDrag = (event: ReactDragEvent<HTMLButtonElement>) => {
    event.stopPropagation();
    setDraggedIndex(null);
    setDropIndex(null);
  };
  const handleReorderKey = (event: ReactKeyboardEvent<HTMLButtonElement>, index: number) => {
    if (!event.altKey || (event.key !== 'ArrowUp' && event.key !== 'ArrowDown')) return;
    event.preventDefault();
    event.stopPropagation();
    moveNode(index, event.key === 'ArrowUp' ? -1 : 1);
  };
  const logicLabel = props.group.match === 'all' ? 'AND' : 'OR';

  return (
    <div className={`condition-group condition-group-depth-${Math.min(props.depth, 4)}`}>
      <div className="condition-group-header">
        <div className="condition-group-summary">
          <span className="condition-group-level">{props.root ? '主条件组' : `第 ${props.depth} 层条件组`}</span>
          <div>
            <Text strong>{props.root ? '规则匹配条件' : '嵌套条件'}</Text>
            <Text type="secondary">{props.group.conditions.length} 项 · 可拖动调整顺序</Text>
          </div>
        </div>
        <div className="condition-group-logic">
          <Text type="secondary">满足</Text>
          <Select
            value={props.group.match}
            aria-label="条件关系"
            onChange={(match) => props.onChange({ ...props.group, match })}
            options={[{ label: '全部条件', value: 'all' }, { label: '任一条件', value: 'any' }]}
            style={{ width: 124 }}
          />
          <span className={`condition-logic-badge condition-logic-badge-${props.group.match}`}>{logicLabel}</span>
        </div>
      </div>
      <div className="condition-group-children">
        {props.group.conditions.map((node, index) => {
          const nodeType = isConditionGroup(node) ? '条件组' : '条件';
          return (
            <div className="condition-list-item" key={index}>
              <div
                className={`condition-drop-zone condition-drop-zone-${index === 0 ? 'edge' : 'between'}${dropIndex === index ? ' condition-drop-zone-active' : ''}`}
                onDragOver={(event) => updateDropTarget(event, index)}
                onDrop={(event) => dropAt(event, index)}
                aria-hidden="true"
              >
                {index > 0 && <span className="condition-connector">{logicLabel}</span>}
              </div>
              <div
                className={`condition-node${isConditionGroup(node) ? ' condition-node-group' : ''}${draggedIndex === index ? ' condition-node-dragging' : ''}`}
                onDragOver={(event) => updateDropTargetFromNode(event, index)}
                onDrop={(event) => dropOnNode(event, index)}
              >
                <div className="condition-node-toolbar">
                  <div className="condition-node-identity">
                    <button
                      type="button"
                      className="condition-drag-handle"
                      draggable
                      onDragStart={(event) => beginDrag(event, index)}
                      onDragEnd={finishDrag}
                      onKeyDown={(event) => handleReorderKey(event, index)}
                      aria-label={`拖动${nodeType} ${index + 1} 排序，或按 Option 加上下方向键移动`}
                      title="拖动排序；也可按 Option + ↑/↓ 移动"
                    >
                      <HolderOutlined />
                    </button>
                    <span className="condition-node-kind">{nodeType}</span>
                    <Text type="secondary">#{index + 1}</Text>
                  </div>
                  <Space size={2} className="condition-node-actions">
                    <Button
                      type="text"
                      size="small"
                      icon={<ArrowUpOutlined />}
                      disabled={index === 0}
                      onClick={() => moveNode(index, -1)}
                      aria-label={`${describeNode(index)} ${index + 1} 上移`}
                      title="上移"
                    />
                    <Button
                      type="text"
                      size="small"
                      icon={<ArrowDownOutlined />}
                      disabled={index === props.group.conditions.length - 1}
                      onClick={() => moveNode(index, 1)}
                      aria-label={`${describeNode(index)} ${index + 1} 下移`}
                      title="下移"
                    />
                    <Button
                      type="text"
                      size="small"
                      danger
                      icon={<DeleteOutlined />}
                      disabled={props.group.conditions.length === 1}
                      onClick={() => removeNode(index)}
                      aria-label={`删除${nodeType} ${index + 1}`}
                      title={props.group.conditions.length === 1 ? '每个条件组至少保留一个条件' : `删除${nodeType}`}
                    />
                  </Space>
                </div>
                <div className={`condition-node-content${isConditionGroup(node) ? ' condition-node-content-group' : ''}`}>
                  {isConditionGroup(node) ? (
                    <ConditionGroupEditor group={node} onChange={(value) => updateNode(index, value)} fields={props.fields} depth={props.depth + 1} />
                  ) : (
                    <ConditionLeafEditor leaf={node} onChange={(value) => updateNode(index, value)} fields={props.fields} />
                  )}
                </div>
              </div>
            </div>
          );
        })}
        <div
          className={`condition-drop-zone condition-drop-zone-edge${dropIndex === props.group.conditions.length ? ' condition-drop-zone-active' : ''}`}
          onDragOver={(event) => updateDropTarget(event, props.group.conditions.length)}
          onDrop={(event) => dropAt(event, props.group.conditions.length)}
          aria-hidden="true"
        />
      </div>
      <Space wrap className="condition-add-actions">
        <Button type="dashed" icon={<PlusOutlined />} onClick={addLeaf}>添加条件</Button>
        <Button
          type="dashed"
          icon={<PlusOutlined />}
          disabled={props.depth >= maxEditorDepth - 1}
          onClick={addGroup}
          title={props.depth >= maxEditorDepth - 1 ? `条件组最多嵌套 ${maxEditorDepth} 层` : '添加嵌套条件组'}
        >
          添加条件组
        </Button>
      </Space>
      <span className="condition-reorder-announcement" aria-live="polite">{reorderAnnouncement}</span>
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
      <div className="condition-leaf-control">
        <Text type="secondary">事件字段</Text>
        <Select
          showSearch
          value={props.leaf.field || undefined}
          placeholder="选择事件字段"
          aria-label="事件字段"
          options={fieldOptions}
          onChange={(field) => props.onChange({
            ...props.leaf,
            field,
            ...(props.leaf.operator === 'within_last' && !isDateTimeField(field) ? { operator: 'contains' as const, value: '' } : {})
          })}
        />
      </div>
      <div className="condition-leaf-control">
        <Text type="secondary">判断方式</Text>
        <Select
          value={props.leaf.operator}
          aria-label="判断方式"
          options={visibleOperatorOptions}
          onChange={(operator: RuleOperator) => props.onChange({ ...props.leaf, operator, ...(operator === 'exists' ? { value: undefined } : { value: props.leaf.value ?? '' }) })}
        />
      </div>
      <div className="condition-leaf-control">
        <Text type="secondary">判断值</Text>
        {needsValue ? (
          <Input
            value={props.leaf.value}
            aria-label="判断值"
            placeholder={props.leaf.operator === 'within_last' ? '例如 2m、30s、1h' : '输入判断值'}
            onChange={(event) => props.onChange({ ...props.leaf, value: event.target.value })}
          />
        ) : <div className="condition-value-placeholder">此判断无需填写值</div>}
      </div>
    </div>
  );
}

function isDateTimeField(field: string) {
  return field === 'publishedAt' || field === 'event.time' || field.endsWith('.publishedAt');
}
