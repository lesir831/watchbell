package notifier

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/http/httpguts"

	"github.com/watchbell/watchbell/internal/model"
	"github.com/watchbell/watchbell/internal/templatex"
)

const (
	maxWebhookURLBytes      = 8 * 1024
	maxWebhookHeaders       = 64
	maxWebhookHeaderBytes   = 64 * 1024
	maxWebhookTemplateBytes = 256 * 1024
	maxWebhookBodyBytes     = 1024 * 1024
)

var webhookMethods = map[string]struct{}{
	http.MethodGet:    {},
	http.MethodPost:   {},
	http.MethodPut:    {},
	http.MethodPatch:  {},
	http.MethodDelete: {},
}

var forbiddenWebhookHeaders = map[string]struct{}{
	"connection":          {},
	"content-length":      {},
	"host":                {},
	"keep-alive":          {},
	"proxy-authorization": {},
	"proxy-connection":    {},
	"te":                  {},
	"trailer":             {},
	"transfer-encoding":   {},
	"upgrade":             {},
}

type WebhookNotifier struct {
	client *http.Client
}

type WebhookConfig struct {
	URL          string            `json:"url"`
	Method       string            `json:"method"`
	Headers      map[string]string `json:"headers"`
	BodyTemplate string            `json:"bodyTemplate"`
	AllowPrivate bool              `json:"allowPrivate"`
}

// ValidateWebhookConfig applies the same safety and size limits used by Send.
// API callers can use it to reject invalid channel configuration at save time.
func ValidateWebhookConfig(raw json.RawMessage) error {
	_, err := decodeWebhookConfig(raw)
	return err
}

func NewWebhookNotifier() *WebhookNotifier {
	return &WebhookNotifier{client: &http.Client{Timeout: 15 * time.Second}}
}

func (n *WebhookNotifier) Type() string {
	return model.ChannelTypeWebhook
}

func (n *WebhookNotifier) Send(ctx context.Context, channel model.NotifyChannel, message Message) error {
	cfg, err := decodeWebhookConfig(channel.Config)
	if err != nil {
		return err
	}

	data := templateData(message)
	targetURL := strings.TrimSpace(templatex.Render(cfg.URL, data))
	if err := validateWebhookURL(targetURL, cfg.AllowPrivate); err != nil {
		return fmt.Errorf("webhook url: %w", err)
	}
	body, err := renderWebhookBody(cfg.BodyTemplate, message, data)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, cfg.Method, targetURL, strings.NewReader(body))
	if err != nil {
		return fmt.Errorf("create webhook request: rendered URL or method is invalid")
	}
	if len(cfg.Headers) == 0 {
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
	}
	renderedHeaderBytes := 0
	for key, valueTemplate := range cfg.Headers {
		value := templatex.Render(valueTemplate, data)
		if err := validateWebhookHeader(key, value); err != nil {
			return err
		}
		renderedHeaderBytes += len(key) + len(value)
		req.Header.Set(key, value)
	}
	if renderedHeaderBytes > maxWebhookHeaderBytes {
		return fmt.Errorf("rendered webhook headers exceed %d bytes", maxWebhookHeaderBytes)
	}
	if req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
	}

	client := n.client
	if client == nil {
		client = &http.Client{}
	}
	requestClient := *client
	if requestClient.Timeout <= 0 {
		requestClient.Timeout = 15 * time.Second
	}
	// Redirects can leak configured authorization headers to an unexpected
	// destination. Webhook endpoints should therefore be configured with their
	// final URL and redirects are returned as ordinary non-2xx responses.
	requestClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	transport, err := webhookTransport(requestClient.Transport, cfg.AllowPrivate)
	if err != nil {
		return err
	}
	requestClient.Transport = transport

	resp, err := requestClient.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("send webhook: request timed out")
		}
		return fmt.Errorf("send webhook: request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		detail := webhookResponseDetail(resp)
		return fmt.Errorf("webhook http %d: %s", resp.StatusCode, detail)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024))
	return nil
}

func decodeWebhookConfig(raw json.RawMessage) (WebhookConfig, error) {
	var cfg WebhookConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return cfg, fmt.Errorf("decode webhook config: %w", err)
	}
	cfg.URL = strings.TrimSpace(cfg.URL)
	if err := validateWebhookURL(cfg.URL, cfg.AllowPrivate); err != nil {
		return cfg, fmt.Errorf("webhook url: %w", err)
	}
	cfg.Method = strings.ToUpper(strings.TrimSpace(cfg.Method))
	if cfg.Method == "" {
		cfg.Method = http.MethodPost
	}
	if _, ok := webhookMethods[cfg.Method]; !ok {
		return cfg, fmt.Errorf("webhook method %q is not allowed", cfg.Method)
	}
	if len(cfg.Headers) > maxWebhookHeaders {
		return cfg, fmt.Errorf("webhook headers exceed %d entries", maxWebhookHeaders)
	}
	headerBytes := 0
	seenHeaders := make(map[string]struct{}, len(cfg.Headers))
	for key, value := range cfg.Headers {
		if err := validateWebhookHeader(key, value); err != nil {
			return cfg, err
		}
		canonicalKey := strings.ToLower(key)
		if _, duplicate := seenHeaders[canonicalKey]; duplicate {
			return cfg, fmt.Errorf("webhook header %q is configured more than once", key)
		}
		seenHeaders[canonicalKey] = struct{}{}
		headerBytes += len(key) + len(value)
	}
	if headerBytes > maxWebhookHeaderBytes {
		return cfg, fmt.Errorf("webhook headers exceed %d bytes", maxWebhookHeaderBytes)
	}
	if len(cfg.BodyTemplate) > maxWebhookTemplateBytes {
		return cfg, fmt.Errorf("webhook bodyTemplate exceeds %d bytes", maxWebhookTemplateBytes)
	}
	return cfg, nil
}

func renderWebhookBody(bodyTemplate string, message Message, data map[string]any) (string, error) {
	if bodyTemplate == "" {
		body, err := json.Marshal(map[string]any{
			"subject": message.Subject,
			"body":    message.Body,
			"data":    message.Data,
		})
		if err != nil {
			return "", fmt.Errorf("encode webhook body: %w", err)
		}
		if len(body) > maxWebhookBodyBytes {
			return "", fmt.Errorf("webhook body exceeds %d bytes", maxWebhookBodyBytes)
		}
		return string(body), nil
	}
	body := templatex.Render(bodyTemplate, data)
	if len(body) > maxWebhookBodyBytes {
		return "", fmt.Errorf("webhook body exceeds %d bytes", maxWebhookBodyBytes)
	}
	return body, nil
}

func validateWebhookURL(raw string, allowPrivate bool) error {
	if raw == "" {
		return fmt.Errorf("is required")
	}
	if len(raw) > maxWebhookURLBytes {
		return fmt.Errorf("exceeds %d bytes", maxWebhookURLBytes)
	}
	if schemeEnd := strings.Index(raw, "://"); schemeEnd >= 0 {
		authority := raw[schemeEnd+3:]
		if end := strings.IndexAny(authority, "/?#"); end >= 0 {
			authority = authority[:end]
		}
		if strings.Contains(authority, "${") {
			return fmt.Errorf("host must not contain template variables")
		}
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("is invalid")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("must use http or https")
	}
	if parsed.Host == "" || parsed.Hostname() == "" {
		return fmt.Errorf("must include a host")
	}
	if parsed.User != nil {
		return fmt.Errorf("must not include credentials")
	}
	if parsed.Fragment != "" {
		return fmt.Errorf("must not include a fragment")
	}
	if ip := net.ParseIP(parsed.Hostname()); ip != nil && !allowPrivate && blockedWebhookIP(ip) {
		return fmt.Errorf("resolves to a private or special-purpose address; enable allowPrivate only for a trusted internal endpoint")
	}
	return nil
}

func webhookTransport(base http.RoundTripper, allowPrivate bool) (*http.Transport, error) {
	var transport *http.Transport
	if base == nil {
		transport = http.DefaultTransport.(*http.Transport).Clone()
	} else {
		configured, ok := base.(*http.Transport)
		if !ok {
			return nil, fmt.Errorf("webhook transport must be an *http.Transport")
		}
		transport = configured.Clone()
	}
	// A proxy can resolve the target outside this process and bypass address
	// validation. Webhook delivery therefore connects to the endpoint directly.
	transport.Proxy = nil
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, fmt.Errorf("parse webhook address: %w", err)
		}
		addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, fmt.Errorf("resolve webhook host: lookup failed")
		}
		if len(addresses) == 0 {
			return nil, fmt.Errorf("resolve webhook host: no addresses")
		}
		if !allowPrivate {
			for _, address := range addresses {
				if blockedWebhookIP(address.IP) {
					return nil, fmt.Errorf("webhook host resolves to a private or special-purpose address")
				}
			}
		}
		for _, resolved := range addresses {
			connection, err := dialer.DialContext(ctx, network, net.JoinHostPort(resolved.IP.String(), port))
			if err == nil {
				return connection, nil
			}
		}
		return nil, fmt.Errorf("connect webhook host: connection failed")
	}
	return transport, nil
}

func blockedWebhookIP(ip net.IP) bool {
	if ip == nil || ip.IsUnspecified() || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || !ip.IsGlobalUnicast() {
		return true
	}
	// RFC 6598 carrier-grade NAT space is not classified as private by net.IP.
	cgnat := &net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}
	return cgnat.Contains(ip)
}

func validateWebhookHeader(key, value string) error {
	if !httpguts.ValidHeaderFieldName(key) {
		return fmt.Errorf("webhook header name %q is invalid", key)
	}
	if _, forbidden := forbiddenWebhookHeaders[strings.ToLower(key)]; forbidden {
		return fmt.Errorf("webhook header %q is not allowed", key)
	}
	if !httpguts.ValidHeaderFieldValue(value) {
		return fmt.Errorf("webhook header %q has an invalid value", key)
	}
	return nil
}

func webhookResponseDetail(resp *http.Response) string {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	detail := strings.TrimSpace(string(body))
	if detail == "" || strings.Contains(strings.ToLower(detail), "<html") {
		detail = http.StatusText(resp.StatusCode)
	}
	if len(detail) > 512 {
		detail = detail[:512] + "…"
	}
	return detail
}
