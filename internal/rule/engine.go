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
	if len(raw) == 0 || string(raw) == "{}" {
		return true, nil, nil
	}
	var set ConditionSet
	if err := json.Unmarshal(raw, &set); err != nil {
		return false, nil, err
	}
	if len(set.Conditions) == 0 {
		return true, nil, nil
	}
	return evalSet(set.Match, set.Conditions, payload, now)
}

func evalSet(matchMode string, conditions []Condition, payload map[string]any, now time.Time) (bool, []string, error) {
	matchMode = strings.ToLower(strings.TrimSpace(matchMode))
	if matchMode == "" {
		matchMode = "all"
	}
	if matchMode != "all" && matchMode != "any" {
		return false, nil, fmt.Errorf("条件关系只能是 all 或 any")
	}
	if len(conditions) == 0 {
		return true, nil, nil
	}

	matched := make([]string, 0)
	for _, condition := range conditions {
		ok, conditionMatched, err := eval(condition, payload, now)
		if err != nil {
			return false, matched, err
		}
		matched = append(matched, conditionMatched...)
		if ok && matchMode == "any" {
			return true, matched, nil
		}
		if !ok && matchMode == "all" {
			return false, matched, nil
		}
	}
	return matchMode == "all", matched, nil
}

func eval(condition Condition, payload map[string]any, now time.Time) (bool, []string, error) {
	if condition.isGroup() {
		return evalSet(condition.Match, condition.Conditions, payload, now)
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
			return false, nil, err
		}
		matched = re.MatchString(actual)
	case "within_last":
		if !exists {
			return false, nil, nil
		}
		if strings.TrimSpace(actual) == "" {
			return false, nil, nil
		}
		duration, err := time.ParseDuration(strings.TrimSpace(condition.Value))
		if err != nil {
			return false, nil, fmt.Errorf("时间窗口 %q 无效：%w", condition.Value, err)
		}
		if duration <= 0 {
			return false, nil, fmt.Errorf("时间窗口必须大于 0")
		}
		occurredAt, err := parseRFC3339Time(value)
		if err != nil {
			return false, nil, fmt.Errorf("字段 %q 的值 %q 不是有效的 RFC3339 时间：%w", condition.Field, actual, err)
		}
		age := now.Sub(occurredAt)
		matched = age >= 0 && age <= duration
	default:
		return false, nil, fmt.Errorf("不支持的操作符 %q", condition.Operator)
	}
	if matched {
		return true, []string{condition.Value}, nil
	}
	return false, nil, nil
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
