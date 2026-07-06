package rule

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

type ConditionSet struct {
	Match      string      `json:"match"`
	Conditions []Condition `json:"conditions"`
}

type Condition struct {
	Field    string `json:"field"`
	Operator string `json:"operator"`
	Value    string `json:"value"`
}

func Match(raw json.RawMessage, payload map[string]any) (bool, []string, error) {
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
	matchMode := strings.ToLower(strings.TrimSpace(set.Match))
	if matchMode == "" {
		matchMode = "all"
	}

	matched := make([]string, 0)
	for _, condition := range set.Conditions {
		ok, err := eval(condition, payload)
		if err != nil {
			return false, nil, err
		}
		if ok {
			matched = append(matched, condition.Value)
			if matchMode == "any" {
				return true, matched, nil
			}
		} else if matchMode == "all" {
			return false, matched, nil
		}
	}
	return matchMode == "all" && len(matched) == len(set.Conditions), matched, nil
}

func eval(condition Condition, payload map[string]any) (bool, error) {
	operator := strings.ToLower(strings.TrimSpace(condition.Operator))
	if operator == "" {
		operator = "contains"
	}
	value, exists := lookup(payload, condition.Field)
	actual := fmt.Sprint(value)
	switch operator {
	case "exists":
		return exists, nil
	case "contains":
		return strings.Contains(strings.ToLower(actual), strings.ToLower(condition.Value)), nil
	case "not_contains":
		return !strings.Contains(strings.ToLower(actual), strings.ToLower(condition.Value)), nil
	case "equals":
		return actual == condition.Value, nil
	case "regex":
		re, err := regexp.Compile(condition.Value)
		if err != nil {
			return false, err
		}
		return re.MatchString(actual), nil
	default:
		return false, fmt.Errorf("unsupported operator %q", condition.Operator)
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
