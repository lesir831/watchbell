package api

import (
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// protectBrowserMutation rejects unsafe browser requests that do not originate
// from this exact origin. Requests without browser Origin/Fetch Metadata remain
// available to non-browser API clients, which still need a valid session.
func (s *Server) protectBrowserMutation(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isUnsafeMethod(r.Method) {
			next.ServeHTTP(w, r)
			return
		}

		fetchSite := strings.ToLower(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site")))
		if fetchSite != "" && fetchSite != "same-origin" {
			s.writeCSRFError(w, r)
			return
		}
		if origin := strings.TrimSpace(r.Header.Get("Origin")); origin != "" {
			if !s.requestHasSameOrigin(r, origin) {
				s.writeCSRFError(w, r)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func isUnsafeMethod(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

func (s *Server) requestHasSameOrigin(r *http.Request, rawOrigin string) bool {
	scheme := "http"
	if s.auth != nil && s.auth.RequestUsesHTTPS(r) {
		scheme = "https"
	}
	expected, err := normalizeOrigin(scheme + "://" + r.Host)
	if err != nil {
		return false
	}
	actual, err := normalizeOrigin(rawOrigin)
	return err == nil && actual == expected
}

func normalizeOrigin(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil ||
		parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("invalid origin")
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", errors.New("invalid origin scheme")
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "" {
		return "", errors.New("invalid origin host")
	}
	port := parsed.Port()
	if (scheme == "http" && port == "80") || (scheme == "https" && port == "443") {
		port = ""
	}
	authority := host
	if strings.Contains(host, ":") {
		authority = "[" + host + "]"
	}
	if port != "" {
		authority = net.JoinHostPort(host, port)
	}
	return scheme + "://" + authority, nil
}

func (s *Server) writeCSRFError(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusForbidden, errorPayload(r,
		errors.New("cross-origin mutation is not allowed"),
		"csrf_rejected", map[string]string{}, nil,
	))
}
