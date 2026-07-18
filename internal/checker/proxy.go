package checker

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/watchbell/watchbell/internal/model"
	xproxy "golang.org/x/net/proxy"
)

// clientForMonitor returns the checker's normal client unless the scheduler
// attached a proxy profile to this monitor. Proxy transports are cloned per
// check so credentials and routes never leak between concurrently running
// monitors.
func clientForMonitor(base *http.Client, monitor model.Monitor) (*http.Client, error) {
	if base == nil {
		base = http.DefaultClient
	}
	if monitor.Proxy == nil {
		return base, nil
	}
	profile := monitor.Proxy
	if profile.Port < 1 || profile.Port > 65535 || strings.TrimSpace(profile.Host) == "" {
		return nil, fmt.Errorf("proxy %q has an invalid host or port", profile.Name)
	}
	baseTransport := base.Transport
	if baseTransport == nil {
		baseTransport = http.DefaultTransport
	}
	transport, ok := baseTransport.(*http.Transport)
	if !ok {
		return nil, fmt.Errorf("proxy %q cannot be combined with the checker's custom HTTP transport", profile.Name)
	}
	clonedTransport := transport.Clone()
	endpoint := net.JoinHostPort(strings.TrimSpace(profile.Host), fmt.Sprint(profile.Port))

	switch profile.Type {
	case model.ProxyTypeHTTP, model.ProxyTypeHTTPS:
		proxyURL := &url.URL{Scheme: profile.Type, Host: endpoint}
		if profile.Username != "" {
			proxyURL.User = url.UserPassword(profile.Username, profile.Password)
		}
		clonedTransport.Proxy = http.ProxyURL(proxyURL)
	case model.ProxyTypeSOCKS5:
		var proxyAuth *xproxy.Auth
		if profile.Username != "" {
			proxyAuth = &xproxy.Auth{User: profile.Username, Password: profile.Password}
		}
		dialer, err := xproxy.SOCKS5("tcp", endpoint, proxyAuth, xproxy.Direct)
		if err != nil {
			return nil, fmt.Errorf("configure SOCKS5 proxy %q: %w", profile.Name, err)
		}
		clonedTransport.Proxy = nil
		clonedTransport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
			if contextDialer, ok := dialer.(xproxy.ContextDialer); ok {
				return contextDialer.DialContext(ctx, network, address)
			}
			return dialer.Dial(network, address)
		}
	default:
		return nil, fmt.Errorf("proxy %q has unsupported type %q", profile.Name, profile.Type)
	}

	client := *base
	client.Transport = clonedTransport
	return &client, nil
}
