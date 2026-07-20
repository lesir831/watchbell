package rule

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

type ConditionSet struct {
	Match      string      `json:"match"`
	Conditions []Condition `json:"conditions"`
}

type Condition struct {
	Field      string      `json:"field,omitempty"`
	Operator   string      `json:"operator,omitempty"`
	Value      string      `json:"value,omitempty"`
	Match      string      `json:"match,omitempty"`
	Conditions []Condition `json:"conditions,omitempty"`
}

const (
	maxConditionDepth = 16
	maxConditionNodes = 200
)

func Validate(raw json.RawMessage) error {
	if len(raw) == 0 || string(raw) == "{}" {
		return nil
	}
	var set ConditionSet
	if err := json.Unmarshal(raw, &set); err != nil {
		return fmt.Errorf("条件不是有效的 JSON：%w", err)
	}
	mode := strings.ToLower(strings.TrimSpace(set.Match))
	if mode != "" && mode != "all" && mode != "any" {
		return fmt.Errorf("条件关系只能是 all 或 any")
	}
	nodes := 0
	for index, condition := range set.Conditions {
		if err := validateCondition(condition, []int{index + 1}, 1, &nodes); err != nil {
			return err
		}
	}
	return nil
}

func validateCondition(condition Condition, path []int, depth int, nodes *int) error {
	label := conditionLabel(path)
	(*nodes)++
	if *nodes > maxConditionNodes {
		return fmt.Errorf("规则最多包含 %d 个条件和条件组", maxConditionNodes)
	}
	if depth > maxConditionDepth {
		return fmt.Errorf("%s超过最大嵌套深度 %d", label, maxConditionDepth)
	}
	if condition.isGroup() {
		if strings.TrimSpace(condition.Field) != "" || strings.TrimSpace(condition.Operator) != "" || strings.TrimSpace(condition.Value) != "" {
			return fmt.Errorf("%s不能同时包含字段判断和子条件", label)
		}
		mode := strings.ToLower(strings.TrimSpace(condition.Match))
		if mode != "" && mode != "all" && mode != "any" {
			return fmt.Errorf("%s的条件关系只能是 all 或 any", label)
		}
		if len(condition.Conditions) == 0 {
			return fmt.Errorf("%s条件组至少需要一个子条件", label)
		}
		for index, child := range condition.Conditions {
			childPath := append(append([]int(nil), path...), index+1)
			if err := validateCondition(child, childPath, depth+1, nodes); err != nil {
				return err
			}
		}
		return nil
	}

	if strings.TrimSpace(condition.Field) == "" {
		return fmt.Errorf("%s缺少事件字段", label)
	}
	operator := strings.ToLower(strings.TrimSpace(condition.Operator))
	if operator == "" {
		operator = "contains"
	}
	switch operator {
	case "contains", "not_contains", "equals":
		if strings.TrimSpace(condition.Value) == "" {
			return fmt.Errorf("%s缺少判断值", label)
		}
	case "regex":
		if _, err := regexp.Compile(condition.Value); err != nil {
			return fmt.Errorf("%s的正则表达式无效：%w", label, err)
		}
	case "within_last":
		duration, err := time.ParseDuration(strings.TrimSpace(condition.Value))
		if err != nil {
			return fmt.Errorf("%s的时间窗口无效：%q 不是有效时长", label, condition.Value)
		}
		if duration <= 0 {
			return fmt.Errorf("%s的时间窗口必须大于 0", label)
		}
	case "exists":
	default:
		return fmt.Errorf("%s使用了不支持的操作符 %q", label, condition.Operator)
	}
	return nil
}

func conditionLabel(path []int) string {
	parts := make([]string, len(path))
	for index, part := range path {
		parts[index] = fmt.Sprint(part)
	}
	return fmt.Sprintf("第 %s 个条件", strings.Join(parts, "."))
}

func (condition Condition) isGroup() bool {
	return condition.Conditions != nil || strings.TrimSpace(condition.Match) != ""
}

func Match(raw json.RawMessage, payload map[string]any) (bool, []string, error) {
	return MatchAt(raw, payload, time.Now())
}

func MatchAt(raw json.RawMessage, payload map[string]any, now time.Time) (bool, []string, error) {
	details, err := EvaluateAt(raw, payload, now)
	return details.Matched, details.Values, err
}

// MatchDetails contains both the legacy match values used by templates and a
// human-readable explanation suitable for activity diagnostics.
type MatchDetails struct {
	Matched        bool
	Values         []string
	MismatchReason string
}

func Evaluate(raw json.RawMessage, payload map[string]any) (MatchDetails, error) {
	return EvaluateAt(raw, payload, time.Now())
}

func EvaluateAt(raw json.RawMessage, payload map[string]any, now time.Time) (MatchDetails, error) {
	if len(raw) == 0 || string(raw) == "{}" {
		return MatchDetails{Matched: true}, nil
	}
	var set ConditionSet
	if err := json.Unmarshal(raw, &set); err != nil {
		return MatchDetails{}, err
	}
	if len(set.Conditions) == 0 {
		return MatchDetails{Matched: true}, nil
	}
	result, err := evalSetDetailed(set.Match, set.Conditions, payload, now, nil)
	if err != nil {
		return MatchDetails{Values: result.values}, err
	}
	details := MatchDetails{Matched: result.matched, Values: result.values}
	if !result.matched {
		reason := strings.TrimSpace(result.failure)
		if reason == "" {
			reason = "没有条件满足"
		}
		details.MismatchReason = "规则条件未匹配：" + strings.TrimSuffix(reason, "。") + "。"
	}
	return details, nil
}

func evalSet(matchMode string, conditions []Condition, payload map[string]any, now time.Time) (bool, []string, error) {
	result, err := evalSetDetailed(matchMode, conditions, payload, now, nil)
	return result.matched, result.values, err
}

type conditionEvaluation struct {
	matched bool
	values  []string
	failure string
}

func evalSetDetailed(matchMode string, conditions []Condition, payload map[string]any, now time.Time, path []int) (conditionEvaluation, error) {
	matchMode = strings.ToLower(strings.TrimSpace(matchMode))
	if matchMode == "" {
		matchMode = "all"
	}
	if matchMode != "all" && matchMode != "any" {
		return conditionEvaluation{}, fmt.Errorf("条件关系只能是 all 或 any")
	}
	if len(conditions) == 0 {
		return conditionEvaluation{matched: true}, nil
	}

	matched := make([]string, 0)
	failures := make([]string, 0, len(conditions))
	for index, condition := range conditions {
		conditionPath := append(append([]int(nil), path...), index+1)
		result, err := evalDetailed(condition, payload, now, conditionPath)
		if err != nil {
			return conditionEvaluation{values: matched}, err
		}
		matched = append(matched, result.values...)
		if result.matched && matchMode == "any" {
			return conditionEvaluation{matched: true, values: matched}, nil
		}
		if !result.matched {
			failures = append(failures, result.failure)
			if matchMode == "all" {
				return conditionEvaluation{values: matched, failure: groupFailure(path, matchMode, failures)}, nil
			}
		}
	}
	if matchMode == "all" {
		return conditionEvaluation{matched: true, values: matched}, nil
	}
	return conditionEvaluation{values: matched, failure: groupFailure(path, matchMode, failures)}, nil
}

func eval(condition Condition, payload map[string]any, now time.Time) (bool, []string, error) {
	result, err := evalDetailed(condition, payload, now, nil)
	return result.matched, result.values, err
}

func evalDetailed(condition Condition, payload map[string]any, now time.Time, path []int) (conditionEvaluation, error) {
	if condition.isGroup() {
		return evalSetDetailed(condition.Match, condition.Conditions, payload, now, path)
	}

	operator := strings.ToLower(strings.TrimSpace(condition.Operator))
	if operator == "" {
		operator = "contains"
	}
	value, exists := lookup(payload, condition.Field)
	actual := fmt.Sprint(value)
	var matched bool
	switch operator {
	case "exists":
		matched = exists
	case "contains":
		matched = strings.Contains(strings.ToLower(actual), strings.ToLower(condition.Value))
	case "not_contains":
		matched = !strings.Contains(strings.ToLower(actual), strings.ToLower(condition.Value))
	case "equals":
		matched = actual == condition.Value
	case "regex":
		re, err := regexp.Compile(condition.Value)
		if err != nil {
			return conditionEvaluation{}, err
		}
		matched = re.MatchString(actual)
	case "within_last":
		if !exists {
			return conditionEvaluation{failure: conditionFailure(condition, path, value, false)}, nil
		}
		if strings.TrimSpace(actual) == "" {
			return conditionEvaluation{failure: conditionFailure(condition, path, value, true)}, nil
		}
		duration, err := time.ParseDuration(strings.TrimSpace(condition.Value))
		if err != nil {
			return conditionEvaluation{}, fmt.Errorf("时间窗口 %q 无效：%w", condition.Value, err)
		}
		if duration <= 0 {
			return conditionEvaluation{}, fmt.Errorf("时间窗口必须大于 0")
		}
		occurredAt, err := parseRFC3339Time(value)
		if err != nil {
			return conditionEvaluation{}, fmt.Errorf("字段 %q 的值 %q 不是有效的 RFC3339 时间：%w", condition.Field, actual, err)
		}
		age := now.Sub(occurredAt)
		matched = age >= 0 && age <= duration
	default:
		return conditionEvaluation{}, fmt.Errorf("不支持的操作符 %q", condition.Operator)
	}
	if matched {
		return conditionEvaluation{matched: true, values: []string{condition.Value}}, nil
	}
	return conditionEvaluation{failure: conditionFailure(condition, path, value, exists)}, nil
}

func groupFailure(path []int, matchMode string, failures []string) string {
	detail := strings.Join(nonEmptyStrings(failures), "；")
	if len(path) == 0 {
		if matchMode == "all" {
			return detail
		}
		return "顶层条件组（任一）未满足：" + detail
	}
	mode := "全部"
	if matchMode == "any" {
		mode = "任一"
	}
	return conditionGroupLabel(path) + "（" + mode + "）未满足：" + detail
}

func nonEmptyStrings(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			result = append(result, value)
		}
	}
	return result
}

func conditionFailure(condition Condition, path []int, value any, exists bool) string {
	label := "条件"
	if len(path) > 0 {
		label = conditionLabel(path)
	}
	failure := fmt.Sprintf("%s（%s）未满足", label, conditionExpectation(condition))
	if !exists {
		return failure + "：事件中不存在字段 " + fmt.Sprintf("%q", strings.TrimSpace(condition.Field))
	}
	return failure + "：实际值为 " + diagnosticValue(value)
}

func conditionExpectation(condition Condition) string {
	field := fmt.Sprintf("%q", strings.TrimSpace(condition.Field))
	operator := strings.ToLower(strings.TrimSpace(condition.Operator))
	if operator == "" {
		operator = "contains"
	}
	switch operator {
	case "exists":
		return "字段 " + field + " 应存在"
	case "contains":
		return fmt.Sprintf("字段 %s 应包含 %q", field, condition.Value)
	case "not_contains":
		return fmt.Sprintf("字段 %s 不应包含 %q", field, condition.Value)
	case "equals":
		return fmt.Sprintf("字段 %s 应等于 %q", field, condition.Value)
	case "regex":
		return fmt.Sprintf("字段 %s 应匹配正则 %q", field, condition.Value)
	case "within_last":
		return fmt.Sprintf("字段 %s 应为最近 %s 内的时间", field, condition.Value)
	default:
		return fmt.Sprintf("字段 %s 使用操作符 %q 和值 %q", field, condition.Operator, condition.Value)
	}
}

func diagnosticValue(value any) string {
	if text, ok := value.(string); ok {
		return fmt.Sprintf("%q", truncateDiagnosticText(text))
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprintf("%q", truncateDiagnosticText(fmt.Sprint(value)))
	}
	return truncateDiagnosticText(string(encoded))
}

func truncateDiagnosticText(value string) string {
	const maxRunes = 120
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return string(runes[:maxRunes]) + "…"
}

func conditionGroupLabel(path []int) string {
	parts := make([]string, len(path))
	for index, part := range path {
		parts[index] = fmt.Sprint(part)
	}
	return fmt.Sprintf("第 %s 个条件组", strings.Join(parts, "."))
}

func parseRFC3339Time(value any) (time.Time, error) {
	switch typed := value.(type) {
	case time.Time:
		return typed, nil
	case *time.Time:
		if typed == nil {
			return time.Time{}, fmt.Errorf("时间为空")
		}
		return *typed, nil
	case string:
		return time.Parse(time.RFC3339Nano, strings.TrimSpace(typed))
	default:
		return time.Parse(time.RFC3339Nano, strings.TrimSpace(fmt.Sprint(value)))
	}
}

func lookup(payload map[string]any, path string) (any, bool) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, false
	}
	var current any = payload
	for _, part := range strings.Split(path, ".") {
		obj, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = obj[part]
		if !ok {
			return nil, false
		}
	}
	return current, true
}
