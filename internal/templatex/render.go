package templatex

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

var variablePattern = regexp.MustCompile(`\$\{([a-zA-Z0-9_.-]+)\}`)
var jsonVariablePattern = regexp.MustCompile(`\$\{(?:json:)?[a-zA-Z0-9_.-]+\}`)

func Render(input string, data map[string]any) string {
	return variablePattern.ReplaceAllStringFunc(input, func(token string) string {
		matches := variablePattern.FindStringSubmatch(token)
		if len(matches) != 2 {
			return token
		}
		value, ok := lookup(data, matches[1])
		if !ok || value == nil {
			return ""
		}
		return fmt.Sprint(value)
	})
}

// RenderJSONTemplate renders ordinary ${path} variables as strings and
// ${json:path} variables as complete JSON values. The latter is the safe way
// to insert untrusted strings, arrays, or objects into a JSON webhook body.
func RenderJSONTemplate(input string, data map[string]any) (string, error) {
	var renderErr error
	rendered := jsonVariablePattern.ReplaceAllStringFunc(input, func(token string) string {
		if renderErr != nil {
			return token
		}
		expression := strings.TrimSuffix(strings.TrimPrefix(token, "${"), "}")
		if !strings.HasPrefix(expression, "json:") {
			value, ok := lookup(data, expression)
			if !ok || value == nil {
				return ""
			}
			return fmt.Sprint(value)
		}
		path := strings.TrimPrefix(expression, "json:")
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

// HasPlainVariables reports whether a template contains raw ${path}
// interpolation. Raw interpolation is useful for text, URLs, and headers, but
// must not be accepted in a JSON document because an attacker-controlled value
// can change the document's structure while leaving it syntactically valid.
func HasPlainVariables(input string) bool {
	for _, token := range jsonVariablePattern.FindAllString(input, -1) {
		if !strings.HasPrefix(token, "${json:") {
			return true
		}
	}
	return false
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
