package checker

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/watchbell/watchbell/internal/model"
)

func TestRSSCheckerUsesAssignedHTTPProxy(t *testing.T) {
	feed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = io.WriteString(w, `<?xml version="1.0"?><rss version="2.0"><channel><title>Feed</title><link>https://example.com</link><item><guid>1</guid><title>Item</title></item></channel></rss>`)
	}))
	defer feed.Close()

	var proxyRequests atomic.Int32
	forwardTransport := &http.Transport{Proxy: nil}
	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyRequests.Add(1)
		outbound := r.Clone(r.Context())
		outbound.RequestURI = ""
		outbound.Header.Del("Proxy-Connection")
		response, err := forwardTransport.RoundTrip(outbound)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer response.Body.Close()
		for key, values := range response.Header {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(response.StatusCode)
		_, _ = io.Copy(w, response.Body)
	}))
	defer proxyServer.Close()

	parsedProxy, err := url.Parse(proxyServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(parsedProxy.Port())
	monitor := model.Monitor{
		Type:   model.MonitorTypeRSS,
		Config: []byte(`{"url":` + strconv.Quote(feed.URL) + `,"notifyExisting":true}`),
		Proxy:  &model.ProxyProfile{Name: "HTTP proxy", Type: model.ProxyTypeHTTP, Host: parsedProxy.Hostname(), Port: port},
	}
	result, err := NewRSSChecker().Check(context.Background(), monitor)
	if err != nil {
		t.Fatal(err)
	}
	if proxyRequests.Load() != 1 || len(result.Events) != 1 {
		t.Fatalf("proxy requests=%d events=%d", proxyRequests.Load(), len(result.Events))
	}
}

func TestClientForMonitorConfiguresHTTPSAndSOCKS5(t *testing.T) {
	httpsMonitor := model.Monitor{Proxy: &model.ProxyProfile{
		Name: "Secure proxy", Type: model.ProxyTypeHTTPS, Host: "proxy.example.com", Port: 8443,
		Username: "user", Password: "secret",
	}}
	client, err := clientForMonitor(&http.Client{}, httpsMonitor)
	if err != nil {
		t.Fatal(err)
	}
	transport := client.Transport.(*http.Transport)
	proxyURL, err := transport.Proxy(&http.Request{URL: &url.URL{Scheme: "https", Host: "example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	password, _ := proxyURL.User.Password()
	if proxyURL.Scheme != "https" || proxyURL.Host != "proxy.example.com:8443" || proxyURL.User.Username() != "user" || password != "secret" {
		t.Fatalf("HTTPS proxy URL was configured incorrectly")
	}

	socksMonitor := model.Monitor{Proxy: &model.ProxyProfile{Name: "SOCKS", Type: model.ProxyTypeSOCKS5, Host: "127.0.0.1", Port: 1080}}
	socksClient, err := clientForMonitor(&http.Client{}, socksMonitor)
	if err != nil {
		t.Fatal(err)
	}
	socksTransport := socksClient.Transport.(*http.Transport)
	if socksTransport.Proxy != nil || socksTransport.DialContext == nil {
		t.Fatal("SOCKS5 transport is missing its direct proxy dialer")
	}
}

func TestClientForMonitorRejectsInvalidProxy(t *testing.T) {
	_, err := clientForMonitor(&http.Client{}, model.Monitor{Proxy: &model.ProxyProfile{Name: "Broken", Type: "ftp", Host: "127.0.0.1", Port: 21}})
	if err == nil {
		t.Fatal("unsupported proxy type was accepted")
	}
}

func TestAssignedUnavailableProxyNeverFallsBackToTarget(t *testing.T) {
	var targetRequests atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetRequests.Add(1)
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = io.WriteString(w, `<?xml version="1.0"?><rss version="2.0"><channel><title>Feed</title></channel></rss>`)
	}))
	defer target.Close()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	monitor := model.Monitor{
		Type:   model.MonitorTypeRSS,
		Config: []byte(`{"url":` + strconv.Quote(target.URL) + `}`),
		Proxy:  &model.ProxyProfile{Name: "Unavailable", Type: model.ProxyTypeHTTP, Host: "127.0.0.1", Port: port},
	}
	if _, err := NewRSSChecker().Check(context.Background(), monitor); err == nil {
		t.Fatal("check unexpectedly succeeded through an unavailable proxy")
	}
	if targetRequests.Load() != 0 {
		t.Fatalf("target received %d request(s) after proxy failure", targetRequests.Load())
	}
}
