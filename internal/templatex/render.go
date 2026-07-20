package templatex

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

var renderVariablePattern = regexp.MustCompile(`\$\{(?:(text|markdown):)?([a-zA-Z0-9_.-]+)\}`)
var templateVariablePattern = regexp.MustCompile(`\$\{(?:(?:json|text|markdown):)?[a-zA-Z0-9_.-]+\}`)

// Render interpolates ordinary values and the text/markdown presentation
// processors used by notification templates. JSON-safe interpolation remains
// exclusive to RenderJSONTemplate.
func Render(input string, data map[string]any) string {
	return renderVariablePattern.ReplaceAllStringFunc(input, func(token string) string {
		matches := renderVariablePattern.FindStringSubmatch(token)
		if len(matches) != 3 {
			return token
		}
		value, ok := lookup(data, matches[2])
		if !ok || value == nil {
			return ""
		}
		return renderValue(matches[1], value)
	})
}

// RenderJSONTemplate renders ordinary and presentation-processed variables as
// strings and ${json:path} variables as complete JSON values. The latter is
// the safe way to insert untrusted strings, arrays, or objects into a JSON
// webhook body.
func RenderJSONTemplate(input string, data map[string]any) (string, error) {
	var renderErr error
	rendered := templateVariablePattern.ReplaceAllStringFunc(input, func(token string) string {
		if renderErr != nil {
			return token
		}
		expression := strings.TrimSuffix(strings.TrimPrefix(token, "${"), "}")
		processor, path := splitProcessor(expression)
		if processor != "json" {
			value, ok := lookup(data, path)
			if !ok || value == nil {
				return ""
			}
			return renderValue(processor, value)
		}
		value, ok := lookup(data, path)
		if !ok {
			value = nil
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			renderErr = fmt.Errorf("encode JSON template variable %q: %w", path, err)
			return token
		}
		return string(encoded)
	})
	if renderErr != nil {
		return "", renderErr
	}
	return rendered, nil
}

// HasPlainVariables reports whether a template contains raw or presentation-
// processed interpolation. Such interpolation is useful for text, URLs, and
// headers, but must not be accepted in a JSON document because an attacker-
// controlled value can change the document's structure while leaving it
// syntactically valid.
func HasPlainVariables(input string) bool {
	for _, token := range templateVariablePattern.FindAllString(input, -1) {
		if !strings.HasPrefix(token, "${json:") {
			return true
		}
	}
	return false
}

func splitProcessor(expression string) (string, string) {
	if index := strings.IndexByte(expression, ':'); index >= 0 {
		return expression[:index], expression[index+1:]
	}
	return "", expression
}

func renderValue(processor string, value any) string {
	text := fmt.Sprint(value)
	switch processor {
	case "text":
		return richTextToPlainText(text)
	case "markdown":
		return richTextToMarkdown(text)
	default:
		return text
	}
}

func lookup(data map[string]any, path string) (any, bool) {
	var current any = data
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
