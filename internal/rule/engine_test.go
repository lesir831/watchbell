package rule

import (
	"encoding/json"
	"testing"
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
