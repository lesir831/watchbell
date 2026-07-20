package notifier

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/watchbell/watchbell/internal/model"
	"github.com/watchbell/watchbell/internal/templatex"
)

const (
	maxDingTalkSecretBytes  = 8 * 1024
	maxDingTalkRecipients   = 1000
	maxDingTalkPayloadBytes = 1024 * 1024
)

var dingTalkMessageTypes = map[string]struct{}{
	"text":       {},
	"markdown":   {},
	"link":       {},
	"actionCard": {},
	"feedCard":   {},
}

var dingTalkTemplateVariablePattern = regexp.MustCompile(`\$\{(?:(?:text|markdown):)?[a-zA-Z0-9_.-]+\}`)

// DingTalkConfig describes a DingTalk custom-robot webhook. ExtraParams is
// recursively merged into the provider-native request body, so new or less
// common DingTalk fields do not require a WatchBell release. String values in
// ExtraParams support the same ${path}, ${text:path}, and ${markdown:path}
// expressions as notification templates.
type DingTalkConfig struct {
	WebhookURL   string         `json:"webhookUrl"`
	Secret       string         `json:"secret"`
	MessageType  string         `json:"messageType"`
	Title        string         `json:"title"`
	Text         string         `json:"text"`
	AtMobiles    []string       `json:"atMobiles"`
	AtUserIDs    []string       `json:"atUserIds"`
	IsAtAll      bool           `json:"isAtAll"`
	ExtraParams  map[string]any `json:"extraParams"`
	AllowPrivate bool           `json:"allowPrivate"`
}

type DingTalkNotifier struct {
	client       *http.Client
	lookupIPAddr func(context.Context, string) ([]net.IPAddr, error)
	now          func() time.Time
}

func NewDingTalkNotifier() *DingTalkNotifier {
	return &DingTalkNotifier{
		client:       &http.Client{Timeout: 15 * time.Second},
		lookupIPAddr: net.DefaultResolver.LookupIPAddr,
		now:          time.Now,
	}
}

func (n *DingTalkNotifier) Type() string {
	return model.ChannelTypeDingTalk
}

// ValidateDingTalkConfig applies the same URL, type, size, and payload-shape
// checks used by Send. It intentionally does not make a network request.
func ValidateDingTalkConfig(raw json.RawMessage) error {
	cfg, err := decodeDingTalkConfig(raw)
	if err != nil {
		return err
	}
	// Save-time validation must accept URL/button fields that will only become
	// concrete when an event is sent. Replace template expressions with a safe,
	// non-empty sentinel for structural validation; Send validates the fully
	// rendered payload again before making a request.
	validationConfig, err := dingTalkValidationConfig(cfg)
	if err != nil {
		return err
	}
	payload, err := buildDingTalkPayload(validationConfig, Message{Subject: "WatchBell", Body: "WatchBell notification"})
	if err != nil {
		return err
	}
	return validateDingTalkPayload(validationConfig.MessageType, payload)
}

func (n *DingTalkNotifier) Send(ctx context.Context, channel model.NotifyChannel, message Message) error {
	cfg, err := decodeDingTalkConfig(channel.Config)
	if err != nil {
		return err
	}
	payload, err := buildDingTalkPayload(cfg, message)
	if err != nil {
		return err
	}
	if err := validateDingTalkPayload(cfg.MessageType, payload); err != nil {
		return err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode dingtalk payload: %w", err)
	}
	if len(body) > maxDingTalkPayloadBytes {
		return fmt.Errorf("dingtalk payload exceeds %d bytes", maxDingTalkPayloadBytes)
	}

	targetURL, err := signedDingTalkURL(cfg.WebhookURL, cfg.Secret, n.currentTime())
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create dingtalk request: webhook URL is invalid")
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")

	client := n.client
	if client == nil {
		client = &http.Client{}
	}
	requestClient := *client
	if requestClient.Timeout <= 0 {
		requestClient.Timeout = 15 * time.Second
	}
	// The webhook URL contains an access token. Never forward it, or the signed
	// request body, to a redirect target.
	requestClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	lookupIPAddr := n.lookupIPAddr
	if lookupIPAddr == nil {
		lookupIPAddr = net.DefaultResolver.LookupIPAddr
	}
	transport, err := webhookTransport(requestClient.Transport, cfg.AllowPrivate, lookupIPAddr)
	if err != nil {
		return err
	}
	requestClient.Transport = transport
	defer transport.CloseIdleConnections()

	resp, err := requestClient.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("send dingtalk webhook: request timed out")
		}
		return fmt.Errorf("send dingtalk webhook: request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("dingtalk http %d: %s", resp.StatusCode, webhookResponseDetail(resp))
	}
	return decodeDingTalkResponse(resp.Body)
}

func (n *DingTalkNotifier) currentTime() time.Time {
	if n.now != nil {
		return n.now()
	}
	return time.Now()
}

func decodeDingTalkConfig(raw json.RawMessage) (DingTalkConfig, error) {
	var cfg DingTalkConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return cfg, fmt.Errorf("decode dingtalk config: %w", err)
	}
	cfg.WebhookURL = strings.TrimSpace(cfg.WebhookURL)
	if err := validateDingTalkWebhookURL(cfg.WebhookURL, cfg.AllowPrivate); err != nil {
		return cfg, fmt.Errorf("dingtalk webhook url: %w", err)
	}
	if len(cfg.Secret) > maxDingTalkSecretBytes {
		return cfg, fmt.Errorf("dingtalk secret exceeds %d bytes", maxDingTalkSecretBytes)
	}
	cfg.MessageType = strings.TrimSpace(cfg.MessageType)
	if cfg.MessageType == "" {
		cfg.MessageType = "text"
	}
	if _, ok := dingTalkMessageTypes[cfg.MessageType]; !ok {
		return cfg, fmt.Errorf("dingtalk messageType %q is not supported", cfg.MessageType)
	}
	if len(cfg.Title) > maxWebhookTemplateBytes {
		return cfg, fmt.Errorf("dingtalk title exceeds %d bytes", maxWebhookTemplateBytes)
	}
	if len(cfg.Text) > maxWebhookTemplateBytes {
		return cfg, fmt.Errorf("dingtalk text exceeds %d bytes", maxWebhookTemplateBytes)
	}
	if len(cfg.AtMobiles) > maxDingTalkRecipients || len(cfg.AtUserIDs) > maxDingTalkRecipients {
		return cfg, fmt.Errorf("dingtalk at recipients exceed %d entries", maxDingTalkRecipients)
	}
	for _, value := range append(append([]string{}, cfg.AtMobiles...), cfg.AtUserIDs...) {
		if strings.ContainsAny(value, "\r\n") {
			return cfg, fmt.Errorf("dingtalk at recipient contains an invalid newline")
		}
	}
	if cfg.ExtraParams != nil {
		encoded, err := json.Marshal(cfg.ExtraParams)
		if err != nil {
			return cfg, fmt.Errorf("encode dingtalk extraParams: %w", err)
		}
		if len(encoded) > maxDingTalkPayloadBytes {
			return cfg, fmt.Errorf("dingtalk extraParams exceed %d bytes", maxDingTalkPayloadBytes)
		}
	}
	return cfg, nil
}

func validateDingTalkWebhookURL(raw string, allowPrivate bool) error {
	if strings.Contains(raw, "${") {
		return fmt.Errorf("must not contain template variables")
	}
	if err := validateWebhookURL(raw, allowPrivate); err != nil {
		return err
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("is invalid")
	}
	if !allowPrivate && !strings.EqualFold(parsed.Scheme, "https") {
		return fmt.Errorf("must use https")
	}
	if strings.EqualFold(parsed.Hostname(), "oapi.dingtalk.com") && strings.TrimSpace(parsed.Query().Get("access_token")) == "" {
		return fmt.Errorf("must include access_token")
	}
	return nil
}

func signedDingTalkURL(raw, secret string, now time.Time) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("create dingtalk signature: webhook URL is invalid")
	}
	if secret == "" {
		return parsed.String(), nil
	}
	timestamp := strconv.FormatInt(now.UnixMilli(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(timestamp + "\n" + secret))
	signature := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	query := parsed.Query()
	query.Set("timestamp", timestamp)
	query.Set("sign", signature)
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func buildDingTalkPayload(cfg DingTalkConfig, message Message) (map[string]any, error) {
	data := templateData(message)
	title := message.Subject
	if cfg.Title != "" {
		title = templatex.Render(cfg.Title, data)
	}
	text := message.Body
	if cfg.Text != "" {
		text = templatex.Render(cfg.Text, data)
	}
	if strings.TrimSpace(title) == "" {
		title = "WatchBell"
	}
	if strings.TrimSpace(text) == "" {
		text = title
	}

	payload := map[string]any{"msgtype": cfg.MessageType}
	switch cfg.MessageType {
	case "text":
		payload["text"] = map[string]any{"content": text}
	case "markdown":
		payload["markdown"] = map[string]any{"title": title, "text": text}
	case "link":
		payload["link"] = map[string]any{"title": title, "text": text}
	case "actionCard":
		payload["actionCard"] = map[string]any{"title": title, "text": text}
	case "feedCard":
		payload["feedCard"] = map[string]any{}
	}
	if len(cfg.AtMobiles) > 0 || len(cfg.AtUserIDs) > 0 || cfg.IsAtAll {
		atMobiles := renderDingTalkStrings(cfg.AtMobiles, data)
		atUserIDs := renderDingTalkStrings(cfg.AtUserIDs, data)
		payload["at"] = map[string]any{
			"atMobiles": atMobiles,
			"atUserIds": atUserIDs,
			"isAtAll":   cfg.IsAtAll,
		}
	}
	if cfg.ExtraParams != nil {
		rendered, err := renderDingTalkValue(cfg.ExtraParams, data)
		if err != nil {
			return nil, err
		}
		extra, ok := rendered.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("dingtalk extraParams must be an object")
		}
		mergeDingTalkObject(payload, extra)
	}
	// messageType remains the source of truth even if extraParams contains a
	// stale or conflicting msgtype field.
	payload["msgtype"] = cfg.MessageType
	normalizeDingTalkMarkdownPayload(cfg.MessageType, payload)
	return payload, nil
}

// normalizeDingTalkMarkdownPayload turns ordinary editor newlines into
// explicit Markdown hard breaks for the two DingTalk formats whose text field
// is rendered as Markdown. Provider-native text/link/feedCard values are left
// byte-for-byte unchanged.
func normalizeDingTalkMarkdownPayload(messageType string, payload map[string]any) {
	if messageType != "markdown" && messageType != "actionCard" {
		return
	}
	message, ok := payload[messageType].(map[string]any)
	if !ok {
		return
	}
	text, ok := message["text"].(string)
	if !ok {
		return
	}
	message["text"] = normalizeDingTalkMarkdownBreaks(text)
}

func normalizeDingTalkMarkdownBreaks(input string) string {
	if !strings.ContainsAny(input, "\r\n") {
		return input
	}
	input = strings.ReplaceAll(input, "\r\n", "\n")
	input = strings.ReplaceAll(input, "\r", "\n")
	lines := strings.Split(input, "\n")
	protected := dingTalkMarkdownProtectedLines(lines)

	var result strings.Builder
	result.Grow(len(input) + len(lines)*2)
	for index, line := range lines {
		if index == len(lines)-1 {
			result.WriteString(line)
			break
		}
		next := lines[index+1]
		if strings.TrimSpace(line) == "" || strings.TrimSpace(next) == "" {
			result.WriteString(line)
			result.WriteByte('\n')
			continue
		}
		if protected[index] || protected[index+1] {
			result.WriteString(line)
			if protected[index] != protected[index+1] {
				// Establish a paragraph boundary before or after a list/code block.
				result.WriteString("\n\n")
			} else {
				result.WriteByte('\n')
			}
			continue
		}
		if dingTalkMarkdownHardBreak(line) {
			result.WriteString(line)
			result.WriteByte('\n')
			continue
		}
		// Two trailing spaces are the portable Markdown hard-break syntax. Trim
		// a lone editor-added trailing space so the output stays deterministic.
		result.WriteString(strings.TrimRight(line, " \t"))
		result.WriteString("  \n")
	}
	return result.String()
}

func dingTalkMarkdownProtectedLines(lines []string) []bool {
	protected := make([]bool, len(lines))
	markDingTalkFencedCode(lines, protected)
	for index := 1; index < len(lines); index++ {
		if !protected[index-1] && !protected[index] && strings.TrimSpace(lines[index-1]) != "" && dingTalkMarkdownSetextUnderline(lines[index]) {
			protected[index-1] = true
			protected[index] = true
		}
	}

	// A list item can contain nested markers, lazy continuation lines, and loose
	// paragraphs separated by a blank line. Keep those lines intact until an
	// unindented paragraph after a blank line definitively ends the list.
	for index := 0; index < len(lines); index++ {
		if protected[index] || !dingTalkMarkdownListMarker(lines[index]) {
			continue
		}
		baseIndent := dingTalkMarkdownIndent(lines[index])
		afterBlank := false
		for cursor := index; cursor < len(lines); cursor++ {
			if strings.TrimSpace(lines[cursor]) == "" {
				afterBlank = true
				continue
			}
			marker := dingTalkMarkdownListMarker(lines[cursor])
			if afterBlank && !marker && dingTalkMarkdownIndent(lines[cursor]) <= baseIndent {
				break
			}
			protected[cursor] = true
			afterBlank = false
		}
	}
	for index, line := range lines {
		if dingTalkMarkdownIndentedCode(line) {
			protected[index] = true
		}
	}
	return protected
}

func dingTalkMarkdownIndent(line string) int {
	indent := 0
	for index := 0; index < len(line); index++ {
		switch line[index] {
		case ' ':
			indent++
		case '\t':
			indent += 4
		default:
			return indent
		}
	}
	return indent
}

func dingTalkMarkdownSetextUnderline(line string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || (trimmed[0] != '=' && trimmed[0] != '-') {
		return false
	}
	marker := trimmed[0]
	for index := 1; index < len(trimmed); index++ {
		if trimmed[index] != marker {
			return false
		}
	}
	return true
}

func markDingTalkFencedCode(lines []string, protected []bool) {
	inside := false
	var fence byte
	fenceLength := 0
	for index, line := range lines {
		marker, length, ok := dingTalkMarkdownFence(line)
		if inside {
			protected[index] = true
			if ok && marker == fence && length >= fenceLength && dingTalkMarkdownClosingFence(line, marker, length) {
				inside = false
			}
			continue
		}
		if ok {
			protected[index] = true
			inside = true
			fence = marker
			fenceLength = length
		}
	}
}

func dingTalkMarkdownClosingFence(line string, marker byte, length int) bool {
	trimmed := strings.TrimLeft(line, " ")
	if len(trimmed) < length {
		return false
	}
	for index := 0; index < length; index++ {
		if trimmed[index] != marker {
			return false
		}
	}
	return strings.TrimSpace(trimmed[length:]) == ""
}

func dingTalkMarkdownFence(line string) (byte, int, bool) {
	indent := len(line) - len(strings.TrimLeft(line, " "))
	if indent > 3 {
		return 0, 0, false
	}
	trimmed := strings.TrimLeft(line, " ")
	if len(trimmed) < 3 || (trimmed[0] != '`' && trimmed[0] != '~') {
		return 0, 0, false
	}
	marker := trimmed[0]
	length := 0
	for length < len(trimmed) && trimmed[length] == marker {
		length++
	}
	return marker, length, length >= 3
}

func dingTalkMarkdownListMarker(line string) bool {
	trimmed := strings.TrimLeft(line, " \t")
	if len(trimmed) < 2 {
		return false
	}
	if (trimmed[0] == '-' || trimmed[0] == '+' || trimmed[0] == '*') && (trimmed[1] == ' ' || trimmed[1] == '\t') {
		return true
	}
	index := 0
	for index < len(trimmed) && trimmed[index] >= '0' && trimmed[index] <= '9' {
		index++
	}
	return index > 0 && index+1 < len(trimmed) && (trimmed[index] == '.' || trimmed[index] == ')') && (trimmed[index+1] == ' ' || trimmed[index+1] == '\t')
}

func dingTalkMarkdownIndentedCode(line string) bool {
	return strings.HasPrefix(line, "\t") || strings.HasPrefix(line, "    ")
}

func dingTalkMarkdownHardBreak(line string) bool {
	if strings.HasSuffix(line, "  ") {
		return true
	}
	trimmed := strings.TrimRight(line, " \t")
	backslashes := 0
	for index := len(trimmed) - 1; index >= 0 && trimmed[index] == '\\'; index-- {
		backslashes++
	}
	return backslashes%2 == 1
}

func dingTalkValidationConfig(cfg DingTalkConfig) (DingTalkConfig, error) {
	replace := func(value string) string {
		return dingTalkTemplateVariablePattern.ReplaceAllString(value, "watchbell-template-value")
	}
	cfg.Title = replace(cfg.Title)
	cfg.Text = replace(cfg.Text)
	cfg.AtMobiles = append([]string(nil), cfg.AtMobiles...)
	for index := range cfg.AtMobiles {
		cfg.AtMobiles[index] = replace(cfg.AtMobiles[index])
	}
	cfg.AtUserIDs = append([]string(nil), cfg.AtUserIDs...)
	for index := range cfg.AtUserIDs {
		cfg.AtUserIDs[index] = replace(cfg.AtUserIDs[index])
	}
	if cfg.ExtraParams != nil {
		replaced, err := replaceDingTalkTemplateVariables(cfg.ExtraParams, replace)
		if err != nil {
			return cfg, err
		}
		cfg.ExtraParams = replaced.(map[string]any)
	}
	return cfg, nil
}

func replaceDingTalkTemplateVariables(value any, replace func(string) string) (any, error) {
	switch typed := value.(type) {
	case string:
		return replace(typed), nil
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			replaced, err := replaceDingTalkTemplateVariables(item, replace)
			if err != nil {
				return nil, err
			}
			result[key] = replaced
		}
		return result, nil
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			replaced, err := replaceDingTalkTemplateVariables(item, replace)
			if err != nil {
				return nil, err
			}
			result[index] = replaced
		}
		return result, nil
	case nil, bool, float64:
		return typed, nil
	default:
		return nil, fmt.Errorf("dingtalk extraParams contains unsupported value %T", value)
	}
}

func renderDingTalkStrings(values []string, data map[string]any) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = templatex.Render(value, data)
	}
	return result
}

func renderDingTalkValue(value any, data map[string]any) (any, error) {
	switch typed := value.(type) {
	case string:
		return templatex.Render(typed, data), nil
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			rendered, err := renderDingTalkValue(item, data)
			if err != nil {
				return nil, err
			}
			result[key] = rendered
		}
		return result, nil
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			rendered, err := renderDingTalkValue(item, data)
			if err != nil {
				return nil, err
			}
			result[index] = rendered
		}
		return result, nil
	case nil, bool, float64:
		return typed, nil
	default:
		return nil, fmt.Errorf("dingtalk extraParams contains unsupported value %T", value)
	}
}

func mergeDingTalkObject(target, source map[string]any) {
	for key, value := range source {
		sourceObject, sourceIsObject := value.(map[string]any)
		targetObject, targetIsObject := target[key].(map[string]any)
		if sourceIsObject && targetIsObject {
			mergeDingTalkObject(targetObject, sourceObject)
			continue
		}
		target[key] = value
	}
}

func validateDingTalkPayload(messageType string, payload map[string]any) error {
	message, ok := payload[messageType].(map[string]any)
	if !ok {
		return fmt.Errorf("dingtalk %s payload must be an object", messageType)
	}
	switch messageType {
	case "text":
		return requireDingTalkString(message, "content", "text.content")
	case "markdown":
		if err := requireDingTalkString(message, "title", "markdown.title"); err != nil {
			return err
		}
		return requireDingTalkString(message, "text", "markdown.text")
	case "link":
		for _, field := range []string{"title", "text", "messageUrl"} {
			if err := requireDingTalkString(message, field, "link."+field); err != nil {
				return err
			}
		}
	case "actionCard":
		for _, field := range []string{"title", "text"} {
			if err := requireDingTalkString(message, field, "actionCard."+field); err != nil {
				return err
			}
		}
		hasSingle := nonemptyDingTalkString(message["singleTitle"]) && nonemptyDingTalkString(message["singleURL"])
		hasButtons := validateDingTalkButtons(message["btns"])
		if !hasSingle && !hasButtons {
			return fmt.Errorf("dingtalk actionCard requires singleTitle/singleURL or valid btns")
		}
	case "feedCard":
		links, ok := message["links"].([]any)
		if !ok || len(links) == 0 {
			return fmt.Errorf("dingtalk feedCard.links must contain at least one item")
		}
		for index, raw := range links {
			link, ok := raw.(map[string]any)
			if !ok {
				return fmt.Errorf("dingtalk feedCard.links.%d must be an object", index)
			}
			for _, field := range []string{"title", "messageURL", "picURL"} {
				if err := requireDingTalkString(link, field, fmt.Sprintf("feedCard.links.%d.%s", index, field)); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func requireDingTalkString(object map[string]any, key, path string) error {
	if !nonemptyDingTalkString(object[key]) {
		return fmt.Errorf("dingtalk %s is required", path)
	}
	return nil
}

func nonemptyDingTalkString(value any) bool {
	text, ok := value.(string)
	return ok && strings.TrimSpace(text) != ""
}

func validateDingTalkButtons(value any) bool {
	buttons, ok := value.([]any)
	if !ok || len(buttons) == 0 {
		return false
	}
	for _, raw := range buttons {
		button, ok := raw.(map[string]any)
		if !ok || !nonemptyDingTalkString(button["title"]) || !nonemptyDingTalkString(button["actionURL"]) {
			return false
		}
	}
	return true
}

func decodeDingTalkResponse(body io.Reader) error {
	limited := io.LimitReader(body, 64*1024)
	var response struct {
		ErrCode json.RawMessage `json:"errcode"`
		ErrMsg  string          `json:"errmsg"`
	}
	decoder := json.NewDecoder(limited)
	if err := decoder.Decode(&response); err != nil {
		return fmt.Errorf("dingtalk returned an invalid response")
	}
	if len(response.ErrCode) == 0 {
		return fmt.Errorf("dingtalk response is missing errcode")
	}
	var codeNumber json.Number
	if err := json.Unmarshal(response.ErrCode, &codeNumber); err != nil {
		var codeString string
		if stringErr := json.Unmarshal(response.ErrCode, &codeString); stringErr != nil {
			return fmt.Errorf("dingtalk response has an invalid errcode")
		}
		codeNumber = json.Number(codeString)
	}
	code, err := strconv.ParseInt(codeNumber.String(), 10, 64)
	if err != nil {
		return fmt.Errorf("dingtalk response has an invalid errcode")
	}
	if code == 0 {
		return nil
	}
	detail := strings.TrimSpace(response.ErrMsg)
	if detail == "" {
		detail = "unknown provider error"
	}
	if len(detail) > 512 {
		detail = detail[:512] + "…"
	}
	return fmt.Errorf("dingtalk errcode %d: %s", code, detail)
}
