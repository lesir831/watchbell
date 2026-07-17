package rule

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestValidateRejectsInvalidRuleDefinitions(t *testing.T) {
	tests := []string{
		`{"match":"sometimes","conditions":[]}`,
		`{"match":"all","conditions":[{"field":"","operator":"contains","value":"x"}]}`,
		`{"match":"all","conditions":[{"field":"rss.title","operator":"unknown","value":"x"}]}`,
		`{"match":"all","conditions":[{"field":"rss.title","operator":"regex","value":"["}]}`,
	}
	for _, input := range tests {
		if err := Validate(json.RawMessage(input)); err == nil {
			t.Fatalf("expected validation error for %s", input)
		}
	}
}

func TestValidateAcceptsVisualBuilderShape(t *testing.T) {
	input := json.RawMessage(`{"match":"any","conditions":[{"field":"rss.title","operator":"contains","value":"release"},{"field":"rss.link","operator":"exists","value":""}]}`)
	if err := Validate(input); err != nil {
		t.Fatal(err)
	}
}

func TestValidateAcceptsNestedConditionGroupsAndWithinLast(t *testing.T) {
	input := json.RawMessage(`{
		"match":"all",
		"conditions":[
			{"match":"any","conditions":[
				{"field":"rss.title","operator":"contains","value":"送码"},
				{"field":"rss.content","operator":"contains","value":"兑换码"}
			]},
			{"field":"rss.publishedAt","operator":"within_last","value":"2m"}
		]
	}`)
	if err := Validate(input); err != nil {
		t.Fatal(err)
	}
}

func TestValidateRejectsInvalidNestedGroupsAndTimeWindows(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantError string
	}{
		{
			name:      "nested match mode",
			input:     `{"match":"all","conditions":[{"match":"sometimes","conditions":[{"field":"rss.title","operator":"contains","value":"x"}]}]}`,
			wantError: "第 1 个条件的条件关系",
		},
		{
			name:      "empty nested group",
			input:     `{"match":"all","conditions":[{"match":"all","conditions":[]}]}`,
			wantError: "至少需要一个子条件",
		},
		{
			name:      "mixed group and leaf",
			input:     `{"match":"all","conditions":[{"match":"all","field":"rss.title","conditions":[{"field":"rss.title","operator":"contains","value":"x"}]}]}`,
			wantError: "不能同时包含字段判断和子条件",
		},
		{
			name:      "bad duration",
			input:     `{"match":"all","conditions":[{"field":"rss.publishedAt","operator":"within_last","value":"two minutes"}]}`,
			wantError: "时间窗口无效",
		},
		{
			name:      "zero duration",
			input:     `{"match":"all","conditions":[{"field":"rss.publishedAt","operator":"within_last","value":"0s"}]}`,
			wantError: "时间窗口必须大于 0",
		},
		{
			name:      "deep invalid leaf",
			input:     `{"match":"all","conditions":[{"match":"all","conditions":[{"match":"any","conditions":[{"field":"","operator":"exists"}]}]}]}`,
			wantError: "第 1.1.1 个条件缺少事件字段",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := Validate(json.RawMessage(test.input))
			if err == nil {
				t.Fatal("expected validation error")
			}
			if !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("error %q does not contain %q", err, test.wantError)
			}
		})
	}
}

func TestMatchNestedGroupsWithRecentPublishedTime(t *testing.T) {
	raw := json.RawMessage(`{
		"match":"all",
		"conditions":[
			{"match":"any","conditions":[
				{"field":"rss.title","operator":"contains","value":"送码"},
				{"field":"rss.content","operator":"contains","value":"兑换码"}
			]},
			{"field":"rss.publishedAt","operator":"within_last","value":"2m"}
		]
	}`)
	payload := map[string]any{
		"rss": map[string]any{
			"title":       "活动送码",
			"content":     "其他内容",
			"publishedAt": time.Now().Add(-time.Minute).Format(time.RFC3339Nano),
		},
	}

	ok, matched, err := Match(raw, payload)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected rule to match")
	}
	if want := []string{"送码", "2m"}; !reflect.DeepEqual(matched, want) {
		t.Fatalf("matched = %#v, want %#v", matched, want)
	}

	payload["rss"].(map[string]any)["publishedAt"] = time.Now().Add(-3 * time.Minute).Format(time.RFC3339Nano)
	ok, matched, err = Match(raw, payload)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("expected old item not to match")
	}
	if want := []string{"送码"}; !reflect.DeepEqual(matched, want) {
		t.Fatalf("matched = %#v, want the successfully matched leaves %#v", matched, want)
	}
}

func TestMatchSupportsArbitrarilyNestedGroups(t *testing.T) {
	raw := json.RawMessage(`{
		"match":"all",
		"conditions":[{"match":"all","conditions":[
			{"match":"any","conditions":[
				{"field":"rss.title","operator":"equals","value":"no"},
				{"match":"all","conditions":[
					{"field":"rss.title","operator":"contains","value":"release"},
					{"field":"rss.link","operator":"exists"}
				]}
			]}
		]}]
	}`)

	ok, matched, err := Match(raw, map[string]any{"rss": map[string]any{"title": "new release", "link": "https://example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected deeply nested rule to match")
	}
	if want := []string{"release", ""}; !reflect.DeepEqual(matched, want) {
		t.Fatalf("matched = %#v, want %#v", matched, want)
	}
}

func TestWithinLastEvaluation(t *testing.T) {
	now := time.Date(2026, time.July, 17, 12, 0, 0, 0, time.UTC)
	condition := Condition{Field: "rss.publishedAt", Operator: "within_last", Value: "2m"}
	tests := []struct {
		name    string
		payload map[string]any
		want    bool
		wantErr string
	}{
		{name: "inside window", payload: map[string]any{"rss": map[string]any{"publishedAt": now.Add(-119 * time.Second).Format(time.RFC3339Nano)}}, want: true},
		{name: "boundary is included", payload: map[string]any{"rss": map[string]any{"publishedAt": now.Add(-2 * time.Minute)}}, want: true},
		{name: "too old", payload: map[string]any{"rss": map[string]any{"publishedAt": now.Add(-121 * time.Second).Format(time.RFC3339Nano)}}},
		{name: "future is excluded", payload: map[string]any{"rss": map[string]any{"publishedAt": now.Add(time.Second).Format(time.RFC3339Nano)}}},
		{name: "missing field", payload: map[string]any{"rss": map[string]any{}}},
		{name: "empty field", payload: map[string]any{"rss": map[string]any{"publishedAt": ""}}},
		{name: "invalid timestamp", payload: map[string]any{"rss": map[string]any{"publishedAt": "yesterday"}}, wantErr: "rss.publishedAt"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, matched, err := eval(condition, test.payload, now)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("error = %v, want error containing %q", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("match = %v, want %v", got, test.want)
			}
			if got && !reflect.DeepEqual(matched, []string{"2m"}) {
				t.Fatalf("matched = %#v, want [2m]", matched)
			}
			if !got && len(matched) != 0 {
				t.Fatalf("matched = %#v, want empty", matched)
			}
		})
	}
}

func TestMatchKeepsLegacyFlatConditionBehavior(t *testing.T) {
	raw := json.RawMessage(`{"match":"any","conditions":[{"field":"rss.title","operator":"contains","value":"release"},{"field":"rss.link","operator":"exists","value":""}]}`)
	ok, matched, err := Match(raw, map[string]any{"rss": map[string]any{"title": "Release notes", "link": "https://example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected legacy flat condition set to match")
	}
	if want := []string{"release"}; !reflect.DeepEqual(matched, want) {
		t.Fatalf("matched = %#v, want %#v", matched, want)
	}
}
