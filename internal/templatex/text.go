package templatex

import (
	"bytes"
	"regexp"
	"strconv"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/renderer/html"
	nethtml "golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

var (
	markdownRenderer = goldmark.New(
		goldmark.WithExtensions(extension.GFM),
		goldmark.WithRendererOptions(html.WithUnsafe()),
	)
	htmlWhitespacePattern   = regexp.MustCompile(`[\t\n\f\r ]+`)
	excessBlankLinesPattern = regexp.MustCompile(`\n[\t ]*\n(?:[\t ]*\n)+`)
)

var richHTMLTags = map[string]struct{}{
	"a": {}, "abbr": {}, "article": {}, "aside": {}, "b": {}, "blockquote": {}, "body": {}, "br": {},
	"caption": {}, "code": {}, "dd": {}, "del": {}, "details": {}, "div": {}, "dl": {}, "dt": {},
	"em": {}, "figcaption": {}, "figure": {}, "font": {}, "footer": {}, "h1": {}, "h2": {}, "h3": {},
	"h4": {}, "h5": {}, "h6": {}, "head": {}, "header": {}, "hr": {}, "html": {}, "i": {}, "img": {},
	"ins": {}, "kbd": {}, "li": {}, "main": {}, "mark": {}, "nav": {}, "ol": {}, "p": {}, "pre": {},
	"q": {}, "s": {}, "script": {}, "section": {}, "small": {}, "span": {}, "strike": {}, "strong": {},
	"style": {}, "sub": {}, "summary": {}, "sup": {}, "table": {}, "tbody": {}, "td": {}, "tfoot": {},
	"th": {}, "thead": {}, "tr": {}, "u": {}, "ul": {},
}

func richTextToPlainText(input string) string {
	if strings.TrimSpace(input) == "" {
		return ""
	}
	var rendered bytes.Buffer
	if err := markdownRenderer.Convert([]byte(normalizeLineEndings(input)), &rendered); err != nil {
		return strings.TrimSpace(input)
	}
	nodes, err := parseHTMLFragment(rendered.String())
	if err != nil {
		return strings.TrimSpace(input)
	}
	var output strings.Builder
	for _, node := range nodes {
		output.WriteString(renderPlainNode(node, 0))
	}
	return normalizeTextOutput(output.String())
}

func richTextToMarkdown(input string) string {
	if strings.TrimSpace(input) == "" {
		return ""
	}
	input = normalizeLineEndings(input)
	if !containsRichHTML(input) {
		// Returning Markdown unchanged makes repeated rendering idempotent and
		// avoids reformatting user-authored documents merely to normalize them.
		return input
	}
	nodes, err := parseHTMLFragment(input)
	if err != nil {
		return input
	}
	var output strings.Builder
	for _, node := range nodes {
		output.WriteString(renderMarkdownNode(node, 0, true))
	}
	return normalizeMarkdownOutput(output.String())
}

func containsRichHTML(input string) bool {
	tokenizer := nethtml.NewTokenizer(strings.NewReader(input))
	for {
		tokenType := tokenizer.Next()
		switch tokenType {
		case nethtml.ErrorToken:
			return false
		case nethtml.StartTagToken, nethtml.EndTagToken, nethtml.SelfClosingTagToken:
			name, _ := tokenizer.TagName()
			if _, known := richHTMLTags[strings.ToLower(string(name))]; known {
				return true
			}
		}
	}
}

func parseHTMLFragment(input string) ([]*nethtml.Node, error) {
	contextNode := &nethtml.Node{Type: nethtml.ElementNode, Data: "div", DataAtom: atom.Div}
	return nethtml.ParseFragment(strings.NewReader(input), contextNode)
}

func renderPlainNode(node *nethtml.Node, listDepth int) string {
	switch node.Type {
	case nethtml.TextNode:
		return collapseHTMLWhitespace(node.Data)
	case nethtml.CommentNode, nethtml.DoctypeNode:
		return ""
	case nethtml.ElementNode:
		tag := strings.ToLower(node.Data)
		if isDiscardedHTMLTag(tag) {
			return ""
		}
		switch tag {
		case "br":
			return "\n"
		case "hr":
			return "\n\n"
		case "img":
			alt := strings.TrimSpace(attribute(node, "alt"))
			if alt != "" {
				return alt
			}
			return strings.TrimSpace(attribute(node, "src"))
		case "a":
			label := strings.TrimSpace(renderPlainChildren(node, listDepth))
			href := strings.TrimSpace(attribute(node, "href"))
			if label == "" {
				return href
			}
			if href != "" && href != label {
				return label + " (" + href + ")"
			}
			return label
		case "ul":
			return renderPlainList(node, false, listDepth)
		case "ol":
			return renderPlainList(node, true, listDepth)
		case "table":
			return "\n\n" + strings.TrimSpace(renderPlainChildren(node, listDepth)) + "\n\n"
		case "tr":
			cells := childElements(node, "th", "td")
			parts := make([]string, 0, len(cells))
			for _, cell := range cells {
				parts = append(parts, strings.TrimSpace(renderPlainChildren(cell, listDepth)))
			}
			return strings.Join(parts, "\t") + "\n"
		case "th", "td":
			return renderPlainChildren(node, listDepth)
		case "pre":
			return "\n\n" + strings.Trim(rawNodeText(node), "\n") + "\n\n"
		}
		content := renderPlainChildren(node, listDepth)
		if isBlockHTMLTag(tag) {
			return "\n\n" + strings.TrimSpace(content) + "\n\n"
		}
		return content
	default:
		return ""
	}
}

func renderPlainChildren(node *nethtml.Node, listDepth int) string {
	var output strings.Builder
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		output.WriteString(renderPlainNode(child, listDepth))
	}
	return output.String()
}

func renderPlainList(node *nethtml.Node, ordered bool, depth int) string {
	var output strings.Builder
	index := orderedListStart(node)
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if child.Type != nethtml.ElementNode || strings.ToLower(child.Data) != "li" {
			continue
		}
		var body, nested strings.Builder
		for item := child.FirstChild; item != nil; item = item.NextSibling {
			if item.Type == nethtml.ElementNode && (strings.EqualFold(item.Data, "ul") || strings.EqualFold(item.Data, "ol")) {
				nested.WriteString(renderPlainNode(item, depth+1))
				continue
			}
			body.WriteString(renderPlainNode(item, depth))
		}
		prefix := "• "
		if ordered {
			prefix = strconv.Itoa(index) + ". "
			index++
		}
		indent := strings.Repeat("  ", depth)
		content := strings.TrimSpace(body.String())
		output.WriteString("\n" + indent + prefix + indentFollowingLines(content, indent+"  "))
		output.WriteString(nested.String())
	}
	return "\n" + strings.Trim(output.String(), "\n") + "\n"
}

func renderMarkdownNode(node *nethtml.Node, listDepth int, topLevel bool) string {
	switch node.Type {
	case nethtml.TextNode:
		if topLevel {
			return normalizeLineEndings(node.Data)
		}
		return collapseHTMLWhitespace(node.Data)
	case nethtml.CommentNode, nethtml.DoctypeNode:
		return ""
	case nethtml.ElementNode:
		tag := strings.ToLower(node.Data)
		if isDiscardedHTMLTag(tag) {
			return ""
		}
		children := func() string { return renderMarkdownChildren(node, listDepth) }
		switch tag {
		case "h1", "h2", "h3", "h4", "h5", "h6":
			level, _ := strconv.Atoi(tag[1:])
			return "\n\n" + strings.Repeat("#", level) + " " + strings.TrimSpace(children()) + "\n\n"
		case "p", "div", "article", "aside", "header", "footer", "main", "nav", "section", "figure", "figcaption", "details", "summary", "dl", "dd":
			return "\n\n" + strings.TrimSpace(children()) + "\n\n"
		case "dt":
			return "\n\n**" + strings.TrimSpace(children()) + "**\n"
		case "br":
			return "\\\n"
		case "hr":
			return "\n\n---\n\n"
		case "strong", "b":
			return wrapMarkdownInline("**", children())
		case "em", "i":
			return wrapMarkdownInline("*", children())
		case "del", "s", "strike":
			return wrapMarkdownInline("~~", children())
		case "u", "ins", "mark", "span", "font", "small", "abbr":
			return children()
		case "sub":
			return "~" + strings.TrimSpace(children()) + "~"
		case "sup":
			return "^" + strings.TrimSpace(children()) + "^"
		case "q":
			return "\"" + strings.TrimSpace(children()) + "\""
		case "code":
			if node.Parent != nil && strings.EqualFold(node.Parent.Data, "pre") {
				return rawNodeText(node)
			}
			return markdownInlineCode(rawNodeText(node))
		case "pre":
			language := codeLanguage(node)
			content := strings.Trim(rawNodeText(node), "\n")
			fence := strings.Repeat("`", maxInt(3, longestRun(content, '`')+1))
			return "\n\n" + fence + language + "\n" + content + "\n" + fence + "\n\n"
		case "a":
			label := strings.TrimSpace(children())
			href := strings.TrimSpace(attribute(node, "href"))
			if href == "" {
				return label
			}
			if label == "" {
				label = href
			}
			return "[" + label + "](" + escapeMarkdownDestination(href) + optionalMarkdownTitle(attribute(node, "title")) + ")"
		case "img":
			src := strings.TrimSpace(attribute(node, "src"))
			if src == "" {
				return strings.TrimSpace(attribute(node, "alt"))
			}
			return "![" + escapeMarkdownLabel(attribute(node, "alt")) + "](" + escapeMarkdownDestination(src) + optionalMarkdownTitle(attribute(node, "title")) + ")"
		case "blockquote":
			content := strings.TrimSpace(children())
			return "\n\n" + prefixLines(content, "> ") + "\n\n"
		case "ul":
			return "\n\n" + renderMarkdownList(node, false, listDepth) + "\n\n"
		case "ol":
			return "\n\n" + renderMarkdownList(node, true, listDepth) + "\n\n"
		case "table":
			return renderMarkdownTable(node, listDepth)
		case "html", "body", "head":
			return children()
		default:
			return children()
		}
	default:
		return ""
	}
}

func renderMarkdownChildren(node *nethtml.Node, listDepth int) string {
	var output strings.Builder
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		appendMarkdownFragment(&output, renderMarkdownNode(child, listDepth, false))
	}
	return output.String()
}

func appendMarkdownFragment(output *strings.Builder, fragment string) {
	if fragment == "" {
		return
	}
	current := output.String()
	if strings.HasSuffix(current, " ") && strings.HasPrefix(fragment, " ") {
		fragment = strings.TrimPrefix(fragment, " ")
	}
	output.WriteString(fragment)
}

func renderMarkdownList(node *nethtml.Node, ordered bool, depth int) string {
	lines := make([]string, 0)
	index := orderedListStart(node)
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if child.Type != nethtml.ElementNode || !strings.EqualFold(child.Data, "li") {
			continue
		}
		var body strings.Builder
		nested := make([]string, 0)
		for item := child.FirstChild; item != nil; item = item.NextSibling {
			if item.Type == nethtml.ElementNode && (strings.EqualFold(item.Data, "ul") || strings.EqualFold(item.Data, "ol")) {
				nested = append(nested, renderMarkdownList(item, strings.EqualFold(item.Data, "ol"), depth+1))
				continue
			}
			body.WriteString(renderMarkdownNode(item, depth, false))
		}
		prefix := "- "
		if ordered {
			prefix = strconv.Itoa(index) + ". "
			index++
		}
		indent := strings.Repeat("  ", depth)
		content := strings.TrimSpace(body.String())
		lines = append(lines, indent+prefix+indentFollowingLines(content, indent+"  "))
		lines = append(lines, nested...)
	}
	return strings.Join(lines, "\n")
}

func renderMarkdownTable(node *nethtml.Node, listDepth int) string {
	rows := descendantElements(node, "tr")
	if len(rows) == 0 {
		return ""
	}
	table := make([][]string, 0, len(rows))
	maxColumns := 0
	for _, row := range rows {
		cells := childElements(row, "th", "td")
		if len(cells) == 0 {
			continue
		}
		values := make([]string, 0, len(cells))
		for _, cell := range cells {
			value := strings.TrimSpace(renderMarkdownChildren(cell, listDepth))
			value = strings.ReplaceAll(value, "|", "\\|")
			value = strings.ReplaceAll(value, "\n", " ")
			values = append(values, value)
		}
		if len(values) > maxColumns {
			maxColumns = len(values)
		}
		table = append(table, values)
	}
	if len(table) == 0 || maxColumns == 0 {
		return ""
	}
	for index := range table {
		for len(table[index]) < maxColumns {
			table[index] = append(table[index], "")
		}
	}
	var output strings.Builder
	output.WriteString("\n\n| " + strings.Join(table[0], " | ") + " |\n")
	output.WriteString("| " + strings.TrimSuffix(strings.Repeat("--- | ", maxColumns), " ") + "\n")
	for _, row := range table[1:] {
		output.WriteString("| " + strings.Join(row, " | ") + " |\n")
	}
	output.WriteString("\n")
	return output.String()
}

func normalizeTextOutput(input string) string {
	input = strings.ReplaceAll(normalizeLineEndings(input), "\u00a0", " ")
	lines := strings.Split(input, "\n")
	for index := range lines {
		lines[index] = strings.TrimRight(lines[index], " \t")
	}
	input = strings.Join(lines, "\n")
	input = excessBlankLinesPattern.ReplaceAllString(input, "\n\n")
	return strings.TrimSpace(input)
}

func normalizeMarkdownOutput(input string) string {
	input = strings.ReplaceAll(normalizeLineEndings(input), "\u00a0", " ")
	lines := strings.Split(input, "\n")
	for index := range lines {
		lines[index] = strings.TrimRight(lines[index], " \t")
	}
	input = strings.Join(lines, "\n")
	input = excessBlankLinesPattern.ReplaceAllString(input, "\n\n")
	return strings.TrimSpace(input)
}

func normalizeLineEndings(input string) string {
	return strings.ReplaceAll(strings.ReplaceAll(input, "\r\n", "\n"), "\r", "\n")
}

func collapseHTMLWhitespace(input string) string {
	return htmlWhitespacePattern.ReplaceAllString(strings.ReplaceAll(input, "\u00a0", " "), " ")
}

func isDiscardedHTMLTag(tag string) bool {
	switch tag {
	case "script", "style", "noscript", "template", "svg", "canvas":
		return true
	default:
		return false
	}
}

func isBlockHTMLTag(tag string) bool {
	switch tag {
	case "address", "article", "aside", "blockquote", "body", "caption", "dd", "details", "div", "dl", "dt", "fieldset", "figcaption", "figure", "footer", "form", "h1", "h2", "h3", "h4", "h5", "h6", "header", "html", "main", "nav", "p", "section", "summary":
		return true
	default:
		return false
	}
}

func rawNodeText(node *nethtml.Node) string {
	var output strings.Builder
	var walk func(*nethtml.Node)
	walk = func(current *nethtml.Node) {
		if current.Type == nethtml.TextNode {
			output.WriteString(current.Data)
		}
		for child := current.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(node)
	return normalizeLineEndings(output.String())
}

func attribute(node *nethtml.Node, key string) string {
	for _, attribute := range node.Attr {
		if strings.EqualFold(attribute.Key, key) {
			return attribute.Val
		}
	}
	return ""
}

func childElements(node *nethtml.Node, tags ...string) []*nethtml.Node {
	wanted := make(map[string]struct{}, len(tags))
	for _, tag := range tags {
		wanted[tag] = struct{}{}
	}
	result := make([]*nethtml.Node, 0)
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if child.Type == nethtml.ElementNode {
			if _, ok := wanted[strings.ToLower(child.Data)]; ok {
				result = append(result, child)
			}
		}
	}
	return result
}

func descendantElements(node *nethtml.Node, tag string) []*nethtml.Node {
	result := make([]*nethtml.Node, 0)
	var walk func(*nethtml.Node)
	walk = func(current *nethtml.Node) {
		for child := current.FirstChild; child != nil; child = child.NextSibling {
			if child.Type == nethtml.ElementNode && strings.EqualFold(child.Data, tag) {
				result = append(result, child)
			}
			walk(child)
		}
	}
	walk(node)
	return result
}

func orderedListStart(node *nethtml.Node) int {
	start, err := strconv.Atoi(strings.TrimSpace(attribute(node, "start")))
	if err != nil || start < 1 {
		return 1
	}
	return start
}

func wrapMarkdownInline(delimiter, content string) string {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return ""
	}
	leading, trailing := "", ""
	if len(content) > 0 && isHTMLWhitespace(content[0]) {
		leading = " "
	}
	if len(content) > 0 && isHTMLWhitespace(content[len(content)-1]) {
		trailing = " "
	}
	return leading + delimiter + trimmed + delimiter + trailing
}

func isHTMLWhitespace(value byte) bool {
	switch value {
	case ' ', '\t', '\n', '\r', '\f':
		return true
	default:
		return false
	}
}

func markdownInlineCode(content string) string {
	fence := strings.Repeat("`", maxInt(1, longestRun(content, '`')+1))
	if strings.HasPrefix(content, "`") || strings.HasSuffix(content, "`") || strings.HasPrefix(content, " ") || strings.HasSuffix(content, " ") {
		content = " " + content + " "
	}
	return fence + content + fence
}

func longestRun(input string, target rune) int {
	longest, current := 0, 0
	for _, value := range input {
		if value == target {
			current++
			if current > longest {
				longest = current
			}
		} else {
			current = 0
		}
	}
	return longest
}

func codeLanguage(node *nethtml.Node) string {
	code := node.FirstChild
	if code == nil || code.Type != nethtml.ElementNode || !strings.EqualFold(code.Data, "code") {
		return ""
	}
	for _, class := range strings.Fields(attribute(code, "class")) {
		if strings.HasPrefix(class, "language-") {
			return strings.TrimPrefix(class, "language-")
		}
	}
	return ""
}

func escapeMarkdownLabel(input string) string {
	input = strings.ReplaceAll(input, "\\", "\\\\")
	input = strings.ReplaceAll(input, "[", "\\[")
	return strings.ReplaceAll(input, "]", "\\]")
}

func escapeMarkdownDestination(input string) string {
	input = strings.ReplaceAll(input, "\\", "\\\\")
	input = strings.ReplaceAll(input, "(", "\\(")
	input = strings.ReplaceAll(input, ")", "\\)")
	return strings.ReplaceAll(input, " ", "%20")
}

func optionalMarkdownTitle(title string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		return ""
	}
	return ` "` + strings.ReplaceAll(title, `"`, `\"`) + `"`
}

func indentFollowingLines(input, indent string) string {
	return strings.ReplaceAll(input, "\n", "\n"+indent)
}

func prefixLines(input, prefix string) string {
	lines := strings.Split(input, "\n")
	for index := range lines {
		lines[index] = prefix + lines[index]
	}
	return strings.Join(lines, "\n")
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}
