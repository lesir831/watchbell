package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/watchbell/watchbell/internal/checker"
	"github.com/watchbell/watchbell/internal/model"
	"github.com/watchbell/watchbell/internal/notifier"
	"github.com/watchbell/watchbell/internal/scheduler"
	"github.com/watchbell/watchbell/internal/store"
)

type stubCinemaDiscovery struct {
	cities       checker.CinemaCityResult
	venues       checker.CinemaVenueResult
	movies       checker.CinemaMovieResult
	err          error
	cityQuery    string
	searchCityID int64
	searchQuery  string
	searchProxy  *model.ProxyProfile
	cityProxy    *model.ProxyProfile
	calls        int
}

func (s *stubCinemaDiscovery) Cities(_ context.Context, query string, proxy *model.ProxyProfile) (checker.CinemaCityResult, error) {
	s.calls++
	s.cityQuery = query
	s.cityProxy = proxy
	return s.cities, s.err
}

func (s *stubCinemaDiscovery) Cinemas(_ context.Context, cityID int64, query string, proxy *model.ProxyProfile) (checker.CinemaVenueResult, error) {
	s.calls++
	s.searchCityID, s.searchQuery, s.searchProxy = cityID, query, proxy
	return s.venues, s.err
}

func (s *stubCinemaDiscovery) Movies(_ context.Context, cityID int64, query string, proxy *model.ProxyProfile) (checker.CinemaMovieResult, error) {
	s.calls++
	s.searchCityID, s.searchQuery, s.searchProxy = cityID, query, proxy
	return s.movies, s.err
}

func TestCinemaDiscoveryEndpointsNormalizeInputAndResolveProxy(t *testing.T) {
	discovery := &stubCinemaDiscovery{
		cities: checker.CinemaCityResult{Items: []checker.CinemaCity{{ID: 1, Name: "北京", Pinyin: "beijing"}}, Total: 1},
		venues: checker.CinemaVenueResult{Items: []checker.CinemaVenue{{ID: 16655, Name: "万达影城", Address: "丰台区"}}, Total: 1},
		movies: checker.CinemaMovieResult{Items: []checker.CinemaMovie{{ID: 1490607, Name: "蜘蛛侠：崭新之日"}}, Total: 1},
	}
	server, database := newCinemaDiscoveryAPIServer(t, discovery)

	response, err := http.Get(server.URL + "/api/cinema/cities?q=%20BEI%20")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var cities checker.CinemaCityResult
	if response.StatusCode != http.StatusOK || json.NewDecoder(response.Body).Decode(&cities) != nil {
		t.Fatalf("cities status=%d", response.StatusCode)
	}
	if discovery.cityQuery != "BEI" || cities.Total != 1 || len(cities.Items) != 1 {
		t.Fatalf("query=%q response=%#v", discovery.cityQuery, cities)
	}

	response, err = http.Get(server.URL + "/api/cinema/cinemas?cityId=1&q=%20%E4%B8%87%E8%BE%BE%20")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var venues checker.CinemaVenueResult
	if response.StatusCode != http.StatusOK || json.NewDecoder(response.Body).Decode(&venues) != nil {
		t.Fatalf("cinemas status=%d", response.StatusCode)
	}
	if discovery.searchCityID != 1 || discovery.searchQuery != "万达" || discovery.searchProxy != nil || venues.Total != 1 {
		t.Fatalf("unexpected cinema call or response: city=%d query=%q proxy=%#v result=%#v", discovery.searchCityID, discovery.searchQuery, discovery.searchProxy, venues)
	}

	profile, err := database.CreateProxyProfile(context.Background(), model.ProxyProfileInput{
		Name: "影院查询代理", Type: model.ProxyTypeHTTP, Host: "127.0.0.1", Port: 8080,
		Username: "user", Password: "secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	response, err = http.Get(server.URL + "/api/cinema/cities?q=bei&proxyId=" + strconv.FormatInt(profile.ID, 10))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK || discovery.cityProxy == nil || discovery.cityProxy.ID != profile.ID || discovery.cityProxy.Password != "secret" {
		t.Fatalf("city proxy was not resolved from storage: status=%d proxy=%#v", response.StatusCode, discovery.cityProxy)
	}
	response, err = http.Get(server.URL + "/api/cinema/movies?cityId=1&q=%E8%9C%98%E8%9B%9B%E4%BE%A0&proxyId=" + strconv.FormatInt(profile.ID, 10))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var movies checker.CinemaMovieResult
	if response.StatusCode != http.StatusOK || json.NewDecoder(response.Body).Decode(&movies) != nil {
		t.Fatalf("movies status=%d", response.StatusCode)
	}
	if discovery.searchProxy == nil || discovery.searchProxy.ID != profile.ID || discovery.searchProxy.Password != "secret" || movies.Total != 1 {
		t.Fatalf("proxy was not resolved from storage: %#v", discovery.searchProxy)
	}
}

func TestCinemaDiscoveryEndpointsRejectInvalidQueriesBeforeUpstream(t *testing.T) {
	discovery := &stubCinemaDiscovery{}
	server, _ := newCinemaDiscoveryAPIServer(t, discovery)
	tests := []struct {
		path  string
		field string
	}{
		{path: "/api/cinema/cinemas?cityId=0&q=%E4%B8%87%E8%BE%BE", field: "cityId"},
		{path: "/api/cinema/cinemas?cityId=1", field: "q"},
		{path: "/api/cinema/movies?cityId=1&q=%E7%89%87&proxyId=bad", field: "proxyId"},
		{path: "/api/cinema/movies?cityId=1&q=%E7%89%87&proxyId=99999", field: "proxyId"},
		{path: "/api/cinema/cities?q=" + url.QueryEscape(strings.Repeat("城", maxCinemaDiscoveryQueryRunes+1)), field: "q"},
	}
	for _, test := range tests {
		response, err := http.Get(server.URL + test.path)
		if err != nil {
			t.Fatal(err)
		}
		var payload struct {
			Code   string            `json:"code"`
			Fields map[string]string `json:"fields"`
		}
		decodeErr := json.NewDecoder(response.Body).Decode(&payload)
		response.Body.Close()
		if decodeErr != nil {
			t.Fatal(decodeErr)
		}
		if response.StatusCode != http.StatusUnprocessableEntity || payload.Code != "validation_failed" || payload.Fields[test.field] == "" {
			t.Fatalf("path=%s status=%d payload=%#v", test.path, response.StatusCode, payload)
		}
	}
	if discovery.calls != 0 {
		t.Fatalf("invalid requests reached discovery %d time(s)", discovery.calls)
	}
}

func TestCinemaDiscoveryEndpointMapsUpstreamFailure(t *testing.T) {
	discovery := &stubCinemaDiscovery{err: errors.New("upstream returned http 429")}
	server, _ := newCinemaDiscoveryAPIServer(t, discovery)
	response, err := http.Get(server.URL + "/api/cinema/cinemas?cityId=1&q=%E4%B8%87%E8%BE%BE")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var payload struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusBadGateway || payload.Code != "upstream_error" {
		t.Fatalf("status=%d payload=%#v", response.StatusCode, payload)
	}
}

func newCinemaDiscoveryAPIServer(t *testing.T, discovery cinemaDiscovery) (*httptest.Server, *store.Store) {
	t.Helper()
	database, err := store.Open(context.Background(), t.TempDir()+"/watchbell.db")
	if err != nil {
		t.Fatal(err)
	}
	sched := scheduler.New(database, checker.NewRegistry(), notifier.NewRegistry(), scheduler.Options{})
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := httptest.NewServer(NewServer(database, sched, "", logger, nil, WithCinemaDiscovery(discovery)).Routes())
	t.Cleanup(func() {
		server.Close()
		database.Close()
	})
	return server, database
}
