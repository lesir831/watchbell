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
	if !HasPlainVariables(`{"unsafe":"${text:message.body}"}`) || !HasPlainVariables(`{"unsafe":"${markdown:message.body}"}`) {
		t.Fatal("text processors must remain plain JSON interpolations")
	}
}

func TestRenderTextAndMarkdownProcessors(t *testing.T) {
	data := map[string]any{
		"item": map[string]any{
			"content": `<h2>Release notes</h2><p>Hello <strong>world</strong> &amp; <a href="https://example.com/release">details</a>.</p><ul><li>First</li><li>Second</li></ul>`,
		},
	}
	plain := Render(`${text:item.content}`, data)
	wantPlain := "Release notes\n\nHello world & details (https://example.com/release).\n\n• First\n• Second"
	if plain != wantPlain {
		t.Fatalf("plain = %q, want %q", plain, wantPlain)
	}
	markdown := Render(`${markdown:item.content}`, data)
	wantMarkdown := "## Release notes\n\nHello **world** & [details](https://example.com/release).\n\n- First\n- Second"
	if markdown != wantMarkdown {
		t.Fatalf("markdown = %q, want %q", markdown, wantMarkdown)
	}
}

func TestTextProcessorRemovesMarkdownAndKeepsReadableStructure(t *testing.T) {
	input := "# Heading\n\nA **bold** statement with [details](https://example.com).\n\n> quoted line\n\n1. First\n2. Second\n\n`inline code`"
	got := Render(`${text:body}`, map[string]any{"body": input})
	want := "Heading\n\nA bold statement with details (https://example.com).\n\nquoted line\n\n1. First\n2. Second\n\ninline code"
	if got != want {
		t.Fatalf("text markdown conversion = %q, want %q", got, want)
	}
}

func TestMarkdownProcessorIsIdempotentForExistingMarkdown(t *testing.T) {
	input := "## Existing Markdown\n\n- **one**\n- [two](https://example.com/two)\n"
	first := Render(`${markdown:body}`, map[string]any{"body": input})
	second := Render(`${markdown:body}`, map[string]any{"body": first})
	if first != input || second != input {
		t.Fatalf("markdown changed: first=%q second=%q", first, second)
	}
}

func TestMarkdownProcessorConvertsMixedHTMLWithoutDestroyingMarkdown(t *testing.T) {
	input := "**Already Markdown** and <em>HTML emphasis</em>.\n\n<p>Next <code>value</code>.</p>"
	got := Render(`${markdown:body}`, map[string]any{"body": input})
	want := "**Already Markdown** and *HTML emphasis*.\n\nNext `value`."
	if got != want {
		t.Fatalf("mixed markdown = %q, want %q", got, want)
	}
}

func TestMarkdownProcessorKeepsWhitespaceAroundInlineFormatting(t *testing.T) {
	input := `<p><strong>Hello </strong>world and <em> spaced </em>text.</p>`
	if got, want := Render(`${markdown:body}`, map[string]any{"body": input}), "**Hello** world and *spaced* text."; got != want {
		t.Fatalf("inline whitespace = %q, want %q", got, want)
	}
}

func TestProcessorsDropUnsafeHTMLContentAndHandleMissingValues(t *testing.T) {
	input := `<p>Visible<br>line</p><script>alert("secret")</script><style>.hidden{}</style>`
	data := map[string]any{"body": input}
	if got := Render(`${text:body}`, data); got != "Visible\nline" {
		t.Fatalf("plain unsafe HTML = %q", got)
	}
	if got := Render(`${markdown:body}`, data); got != "Visible\\\nline" {
		t.Fatalf("markdown unsafe HTML = %q", got)
	}
	if got := Render(`before${text:missing}after`, data); got != "beforeafter" {
		t.Fatalf("missing processor value = %q", got)
	}
}

func TestRenderJSONTemplateRecognizesTextProcessors(t *testing.T) {
	data := map[string]any{"body": "<strong>Hello</strong> **world**"}
	got, err := RenderJSONTemplate(`plain=${text:body}; markdown=${markdown:body}`, data)
	if err != nil {
		t.Fatal(err)
	}
	if want := "plain=Hello world; markdown=**Hello** **world**"; got != want {
		t.Fatalf("rendered = %q, want %q", got, want)
	}
}
