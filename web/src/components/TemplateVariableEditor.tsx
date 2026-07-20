import {
  forwardRef,
  useEffect,
  useImperativeHandle,
  useMemo,
  useRef,
  useState,
  type ChangeEvent,
  type FocusEvent,
  type KeyboardEvent,
  type ReactNode,
  type UIEvent
} from 'react';
import type { VariableDefinition } from '../types';

export interface TemplateVariableEditorHandle {
  focus: () => void;
  insertVariable: (key: string) => void;
  insertTransform: (key: TemplateTextTransform['key']) => void;
}

export interface TemplateTextTransform {
  key: 'text' | 'markdown';
  label: string;
  description: string;
}

export const templateTextTransforms: TemplateTextTransform[] = [
  { key: 'text', label: '转为纯文本', description: '转为纯文本并去除 HTML、Markdown 标记。' },
  { key: 'markdown', label: '转为 Markdown', description: '将 HTML 富文本转为 Markdown，已有 Markdown 会尽量保留，适合 Markdown 或卡片渠道。' }
];

interface TemplateVariableEditorProps {
  id?: string;
  value?: string;
  variables: VariableDefinition[];
  multiline?: boolean;
  rows?: number;
  placeholder?: string;
  disabled?: boolean;
  onChange?: (value: string) => void;
  onFocus?: (event: FocusEvent<HTMLTextAreaElement>) => void;
  onBlur?: (event: FocusEvent<HTMLTextAreaElement>) => void;
}

interface CompletionContext {
  start: number;
  end: number;
  query: string;
}

type CompletionSuggestion =
  | { kind: 'variable'; variable: VariableDefinition; transform?: TemplateTextTransform }
  | { kind: 'transform'; transform: TemplateTextTransform };

function completionContext(value: string, caret: number): CompletionContext | null {
  const opener = value.lastIndexOf('${', Math.max(0, caret - 1));
  if (opener < 0 || opener + 2 > caret) return null;
  const query = value.slice(opener + 2, caret);
  if (/[{}\n\r]/.test(query)) return null;
  return { start: opener + 2, end: caret, query };
}

function isKnownToken(key: string, knownVariables: Set<string>) {
  if (knownVariables.has(key)) return true;
  const separator = key.indexOf(':');
  if (separator < 1) return false;
  const transform = key.slice(0, separator);
  const path = key.slice(separator + 1);
  return templateTextTransforms.some((item) => item.key === transform) && knownVariables.has(path);
}

function highlightedTemplate(value: string, knownVariables: Set<string>) {
  const output: ReactNode[] = [];
  const pattern = /\$\{[^}\n\r]*(?:\}|$)/g;
  let cursor = 0;
  let match: RegExpExecArray | null;
  while ((match = pattern.exec(value)) !== null) {
    if (match.index > cursor) output.push(value.slice(cursor, match.index));
    const token = match[0];
    const closed = token.endsWith('}');
    const key = token.slice(2, closed ? -1 : undefined);
    output.push(
      <mark
        className={`template-token${closed && isKnownToken(key, knownVariables) ? ' known' : ''}${closed ? '' : ' editing'}`}
        key={`${match.index}-${token}`}
      >
        {token}
      </mark>
    );
    cursor = match.index + token.length;
    if (!token.length) pattern.lastIndex += 1;
  }
  if (cursor < value.length) output.push(value.slice(cursor));
  // A final newline has no visual height in a <pre>; a zero-width suffix keeps
  // the highlight layer aligned with the textarea's last empty line.
  if (value.endsWith('\n')) output.push('\u200b');
  return output.length ? output : '\u200b';
}

export const TemplateVariableEditor = forwardRef<TemplateVariableEditorHandle, TemplateVariableEditorProps>(function TemplateVariableEditor({
  id,
  value = '',
  variables,
  multiline = false,
  rows = multiline ? 10 : 1,
  placeholder,
  disabled,
  onChange,
  onFocus,
  onBlur
}, forwardedRef) {
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  const highlightRef = useRef<HTMLPreElement>(null);
  const selectionRef = useRef({ start: value.length, end: value.length });
  const currentValueRef = useRef(value);
  const previousPropValueRef = useRef(value);
  const [focused, setFocused] = useState(false);
  const [completion, setCompletion] = useState<CompletionContext | null>(null);
  const [activeSuggestion, setActiveSuggestion] = useState(0);
  currentValueRef.current = value;

  useEffect(() => {
    if (previousPropValueRef.current !== value && document.activeElement !== textareaRef.current) {
      selectionRef.current = { start: value.length, end: value.length };
    }
    previousPropValueRef.current = value;
  }, [value]);

  const uniqueVariables = useMemo(() => {
    const byKey = new Map<string, VariableDefinition>();
    variables.forEach((item) => byKey.set(item.key, item));
    return Array.from(byKey.values());
  }, [variables]);
  const knownVariables = useMemo(() => new Set(uniqueVariables.map((item) => item.key)), [uniqueVariables]);
  const suggestions = useMemo(() => {
    if (!completion) return [];
    const query = completion.query.trim().toLocaleLowerCase();
    const separator = query.indexOf(':');
    const selectedTransform = separator >= 0
      ? templateTextTransforms.find((item) => item.key === query.slice(0, separator))
      : undefined;
    const pathQuery = selectedTransform ? query.slice(separator + 1) : query;
    const variableSuggestions: CompletionSuggestion[] = uniqueVariables
      .filter((item) => !pathQuery || item.key.toLocaleLowerCase().includes(pathQuery) || item.label.toLocaleLowerCase().includes(pathQuery))
      .sort((left, right) => {
        const leftStarts = left.key.toLocaleLowerCase().startsWith(pathQuery) ? 0 : 1;
        const rightStarts = right.key.toLocaleLowerCase().startsWith(pathQuery) ? 0 : 1;
        return leftStarts - rightStarts || left.key.localeCompare(right.key);
      })
      .map((variable) => ({ kind: 'variable' as const, variable, transform: selectedTransform }));
    if (selectedTransform) return variableSuggestions.slice(0, 8);
    const transformSuggestions: CompletionSuggestion[] = templateTextTransforms
      .filter((item) => !query || item.key.includes(query) || item.label.toLocaleLowerCase().includes(query))
      .map((transform) => ({ kind: 'transform' as const, transform }));
    return [...transformSuggestions, ...variableSuggestions].slice(0, 8);
  }, [completion, uniqueVariables]);

  const updateCompletion = (nextValue: string, caret: number) => {
    setCompletion(completionContext(nextValue, caret));
    setActiveSuggestion(0);
  };

  const restoreSelection = (start: number, end = start, focus = true) => {
    selectionRef.current = { start, end };
    requestAnimationFrame(() => {
      const textarea = textareaRef.current;
      if (!textarea) return;
      if (focus) textarea.focus();
      textarea.setSelectionRange(start, end);
    });
  };

  const replaceSelection = (text: string, caretOffset = text.length, focus = true) => {
    const textarea = textareaRef.current;
    const current = textarea && document.activeElement === textarea
      ? { start: textarea.selectionStart, end: textarea.selectionEnd }
      : selectionRef.current;
    const currentValue = currentValueRef.current;
    const nextValue = `${currentValue.slice(0, current.start)}${text}${currentValue.slice(current.end)}`;
    const caret = current.start + caretOffset;
    currentValueRef.current = nextValue;
    onChange?.(nextValue);
    restoreSelection(caret, caret, focus);
    updateCompletion(nextValue, caret);
  };

  const chooseSuggestion = (item: CompletionSuggestion) => {
    if (!completion) return;
    const currentValue = currentValueRef.current;
    const hasClosingBrace = currentValue[completion.end] === '}';
    const suffix = hasClosingBrace ? '' : '}';
    const replacement = item.kind === 'transform'
      ? `${item.transform.key}:`
      : `${item.transform ? `${item.transform.key}:` : ''}${item.variable.key}`;
    const nextValue = `${currentValue.slice(0, completion.start)}${replacement}${suffix}${currentValue.slice(completion.end)}`;
    const caret = completion.start + replacement.length + (item.kind === 'transform' ? 0 : 1);
    currentValueRef.current = nextValue;
    onChange?.(nextValue);
    if (item.kind === 'transform') updateCompletion(nextValue, caret);
    else setCompletion(null);
    restoreSelection(caret);
  };

  useImperativeHandle(forwardedRef, () => ({
    focus: () => textareaRef.current?.focus(),
    insertVariable: (key: string) => replaceSelection(`\${${key}}`),
    insertTransform: (key: TemplateTextTransform['key']) => {
      const token = `\${${key}:}`;
      replaceSelection(token, token.length - 1);
    }
  }));

  const handleChange = (event: ChangeEvent<HTMLTextAreaElement>) => {
    const nextValue = event.target.value;
    const caret = event.target.selectionStart;
    selectionRef.current = { start: caret, end: event.target.selectionEnd };
    currentValueRef.current = nextValue;
    onChange?.(nextValue);
    updateCompletion(nextValue, caret);
  };

  const rememberSelection = () => {
    const textarea = textareaRef.current;
    if (!textarea) return;
    selectionRef.current = { start: textarea.selectionStart, end: textarea.selectionEnd };
    updateCompletion(currentValueRef.current, textarea.selectionStart);
  };

  const handleKeyDown = (event: KeyboardEvent<HTMLTextAreaElement>) => {
    if (focused && completion && suggestions.length) {
      if (event.key === 'ArrowDown') {
        event.preventDefault();
        setActiveSuggestion((current) => (current + 1) % suggestions.length);
        return;
      }
      if (event.key === 'ArrowUp') {
        event.preventDefault();
        setActiveSuggestion((current) => (current - 1 + suggestions.length) % suggestions.length);
        return;
      }
      if (event.key === 'Enter' || event.key === 'Tab') {
        event.preventDefault();
        chooseSuggestion(suggestions[activeSuggestion]);
        return;
      }
    }
    if (event.key === 'Escape' && completion) {
      event.preventDefault();
      setCompletion(null);
      return;
    }
    if (event.key === '$' && !event.metaKey && !event.ctrlKey && !event.altKey) {
      event.preventDefault();
      replaceSelection('${}', 2);
      return;
    }
    if (!multiline && event.key === 'Enter') event.preventDefault();
  };

  const syncScroll = (event: UIEvent<HTMLTextAreaElement>) => {
    if (!highlightRef.current) return;
    highlightRef.current.scrollTop = event.currentTarget.scrollTop;
    highlightRef.current.scrollLeft = event.currentTarget.scrollLeft;
  };

  return (
    <div className={`template-variable-editor${multiline ? ' multiline' : ' single-line'}${disabled ? ' disabled' : ''}`}>
      <pre ref={highlightRef} className="template-variable-highlight" aria-hidden="true">{highlightedTemplate(value, knownVariables)}</pre>
      <textarea
        ref={textareaRef}
        id={id}
        value={value}
        rows={rows}
        disabled={disabled}
        placeholder={placeholder}
        spellCheck={false}
        autoComplete="off"
        aria-autocomplete="list"
        aria-expanded={focused && Boolean(completion) && suggestions.length > 0}
        aria-controls={focused && completion ? `${id ?? 'template-editor'}-suggestions` : undefined}
        aria-activedescendant={focused && completion && suggestions.length ? `${id ?? 'template-editor'}-suggestion-${activeSuggestion}` : undefined}
        onChange={handleChange}
        onKeyDown={handleKeyDown}
        onKeyUp={rememberSelection}
        onClick={rememberSelection}
        onSelect={rememberSelection}
        onScroll={syncScroll}
        onFocus={(event) => {
          setFocused(true);
          rememberSelection();
          onFocus?.(event);
        }}
        onBlur={(event) => {
          setFocused(false);
          onBlur?.(event);
        }}
      />
      {focused && completion && suggestions.length > 0 && (
        <div className="template-variable-suggestions" id={`${id ?? 'template-editor'}-suggestions`} role="listbox">
          {suggestions.map((item, index) => (
            <button
              type="button"
              role="option"
              aria-selected={index === activeSuggestion}
              id={`${id ?? 'template-editor'}-suggestion-${index}`}
              className={index === activeSuggestion ? 'active' : ''}
              key={item.kind === 'transform' ? `transform-${item.transform.key}` : `${item.transform?.key ?? 'variable'}-${item.variable.key}`}
              onMouseDown={(event) => event.preventDefault()}
              onMouseEnter={() => setActiveSuggestion(index)}
              onClick={() => chooseSuggestion(item)}
            >
              <span>
                <strong>{item.kind === 'transform' ? item.transform.label : item.variable.label}</strong>
                <small>{item.kind === 'transform' ? item.transform.description : item.variable.description}</small>
              </span>
              <code>{item.kind === 'transform'
                ? `\${${item.transform.key}:path}`
                : `\${${item.transform ? `${item.transform.key}:` : ''}${item.variable.key}}`}</code>
            </button>
          ))}
          <div className="template-variable-suggestion-hint">↑↓ 选择 · Enter 插入 · Esc 关闭</div>
        </div>
      )}
    </div>
  );
});
