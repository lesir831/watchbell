package templatex

import (
	"fmt"
	"regexp"
	"strings"
)

var variablePattern = regexp.MustCompile(`\$\{([a-zA-Z0-9_.-]+)\}`)

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
