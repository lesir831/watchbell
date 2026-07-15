package templatex

import "testing"

func TestRenderJSONTemplateEscapesCompleteJSONValues(t *testing.T) {
	data := map[string]any{
		"message": map[string]any{"body": "line one\n\"quoted\" \\ path"},
		"event":   map[string]any{"count": 3},
	}
	rendered, err := RenderJSONTemplate(
		`{"body":${json:message.body},"count":${json:event.count},"plain":"${event.count}"}`,
		data,
	)
	if err != nil {
		t.Fatal(err)
	}
	want := "{\"body\":\"line one\\n\\\"quoted\\\" \\\\ path\",\"count\":3,\"plain\":\"3\"}"
	if rendered != want {
		t.Fatalf("rendered = %q, want %q", rendered, want)
	}
}

func TestRenderJSONTemplateUsesNullForMissingJSONValue(t *testing.T) {
	rendered, err := RenderJSONTemplate(`{"value":${json:event.missing}}`, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if rendered != `{"value":null}` {
		t.Fatalf("rendered = %q", rendered)
	}
}

func TestHasPlainVariables(t *testing.T) {
	if !HasPlainVariables(`{"unsafe":"${message.body}","safe":${json:event.id}}`) {
		t.Fatal("plain variable was not detected")
	}
	if HasPlainVariables(`{"safe":${json:message.body}}`) {
		t.Fatal("JSON-encoded variable was reported as plain")
	}
}
