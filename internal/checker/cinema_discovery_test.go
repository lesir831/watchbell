package checker

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/watchbell/watchbell/internal/model"
)

func TestMaoyanCinemaDiscoveryNormalizesProviderResponses(t *testing.T) {
	var searchRequests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		switch r.URL.Path {
		case "/cities":
			_, _ = io.WriteString(w, `{"cts":[
				{"id":1,"nm":" 北京 ","py":"BeiJing"},
				{"id":10,"nm":"上海","py":"shanghai"},
				{"id":1,"nm":"重复北京","py":"beijing"},
				{"id":0,"nm":"无效","py":"invalid"}
			]}`)
		case "/search":
			searchRequests.Add(1)
			if r.URL.Query().Get("cityId") != "1" || r.URL.Query().Get("kw") == "" {
				t.Errorf("unexpected search query: %s", r.URL.RawQuery)
			}
			if r.Header.Get("Accept") != "application/json" || !strings.Contains(r.Header.Get("User-Agent"), "WatchBell") {
				t.Errorf("unexpected request headers: %#v", r.Header)
			}
			switch r.URL.Query().Get("stype") {
			case "2":
				_, _ = io.WriteString(w, `{"cinemas":{"total":3,"list":[
					{"id":16655,"nm":" 万达影城 ","addr":" 丰台区 路 1 号 ","distance":"1.8km","hallType":["IMAX厅","IMAX厅"," "]},
					{"id":16655,"nm":"重复影院"},{"id":0,"nm":"无效影院"}
				]}}`)
			case "0":
				_, _ = io.WriteString(w, `{"movies":{"total":4,"list":[
					{"id":1490607,"nm":" 蜘蛛侠：崭新之日 ","enm":" Spider-Man: Brand New Day ","movieAlias":"蜘蛛侠：重生日","movieType":0,"rt":"2026-07-29","pubDesc":"2026-07-29中国大陆上映","cat":"动作, 冒险，科幻","ver":"IMAX 2D"},
					{"id":370420,"nm":"神探阿蒙 第一季","movieType":1},
					{"id":1490607,"nm":"重复影片","movieType":0},{"id":0,"nm":"无效影片","movieType":0}
				]}}`)
			default:
				http.Error(w, "unexpected stype", http.StatusBadRequest)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	discovery := &maoyanCinemaDiscovery{
		client: upstream.Client(), citiesURL: upstream.URL + "/cities", searchURL: upstream.URL + "/search",
	}
	cities, err := discovery.Cities(context.Background(), "BEI", nil)
	if err != nil {
		t.Fatal(err)
	}
	if cities.Total != 1 || len(cities.Items) != 1 || cities.Items[0] != (CinemaCity{ID: 1, Name: "北京", Pinyin: "beijing"}) {
		t.Fatalf("unexpected cities: %#v", cities)
	}

	venues, err := discovery.Cinemas(context.Background(), 1, " 万达 ", nil)
	if err != nil {
		t.Fatal(err)
	}
	if venues.Total != 1 || len(venues.Items) != 1 {
		t.Fatalf("unexpected cinemas: %#v", venues)
	}
	venue := venues.Items[0]
	if venue.ID != 16655 || venue.Name != "万达影城" || venue.Address != "丰台区 路 1 号" || len(venue.HallTypes) != 1 || venue.HallTypes[0] != "IMAX厅" {
		t.Fatalf("unexpected normalized cinema: %#v", venue)
	}

	movies, err := discovery.Movies(context.Background(), 1, "蜘蛛侠", nil)
	if err != nil {
		t.Fatal(err)
	}
	if movies.Total != 1 || len(movies.Items) != 1 {
		t.Fatalf("unexpected movies: %#v", movies)
	}
	movie := movies.Items[0]
	if movie.ID != 1490607 || movie.Name != "蜘蛛侠：崭新之日" || movie.EnglishName != "Spider-Man: Brand New Day" ||
		movie.Alias != "蜘蛛侠：重生日" || len(movie.Categories) != 3 || movie.Categories[2] != "科幻" || movie.Version != "IMAX 2D" {
		t.Fatalf("unexpected normalized movie: %#v", movie)
	}
	if searchRequests.Load() != 2 {
		t.Fatalf("search requests = %d, want 2", searchRequests.Load())
	}
}

func TestMaoyanCinemaDiscoveryRejectsInvalidUpstreamResponses(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		contentType string
		body        string
		want        string
	}{
		{name: "status", status: http.StatusTooManyRequests, contentType: "application/json", body: `{}`, want: "http 429"},
		{name: "content type", status: http.StatusOK, contentType: "text/html", body: `<html>blocked</html>`, want: "unexpected content type"},
		{name: "malformed", status: http.StatusOK, contentType: "application/json", body: `{`, want: "decode response"},
		{name: "oversize", status: http.StatusOK, contentType: "application/json", body: strings.Repeat(" ", maxCinemaDiscoveryBytes+1), want: "exceeds"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", test.contentType)
				w.WriteHeader(test.status)
				_, _ = io.WriteString(w, test.body)
			}))
			defer upstream.Close()
			discovery := &maoyanCinemaDiscovery{client: upstream.Client(), citiesURL: upstream.URL, searchURL: upstream.URL}
			_, err := discovery.Cities(context.Background(), "", nil)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestMaoyanCinemaDiscoveryUsesAssignedProxy(t *testing.T) {
	var targetRequests atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetRequests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"cinemas":{"list":[{"id":1,"nm":"影院"}]}}`)
	}))
	defer target.Close()

	var proxyRequests atomic.Int32
	forwardTransport := &http.Transport{Proxy: nil}
	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyRequests.Add(1)
		outbound := r.Clone(r.Context())
		outbound.RequestURI = ""
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
	discovery := &maoyanCinemaDiscovery{
		client: &http.Client{Transport: &http.Transport{Proxy: nil}}, citiesURL: target.URL, searchURL: target.URL,
	}
	result, err := discovery.Cinemas(context.Background(), 1, "影院", &model.ProxyProfile{
		Name: "discovery proxy", Type: model.ProxyTypeHTTP, Host: parsedProxy.Hostname(), Port: port,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 1 || proxyRequests.Load() != 1 || targetRequests.Load() != 1 {
		t.Fatalf("result=%#v proxy requests=%d target requests=%d", result, proxyRequests.Load(), targetRequests.Load())
	}
}

func TestCinemaDiscoveryInputValidationAndFixedDefaults(t *testing.T) {
	if _, err := normalizeCinemaDiscoverySearch(0, "影院"); err == nil {
		t.Fatal("zero city id was accepted")
	}
	if _, err := normalizeCinemaDiscoverySearch(1, " "); err == nil {
		t.Fatal("empty query was accepted")
	}
	if _, err := normalizeCinemaDiscoveryQuery(strings.Repeat("搜", maxCinemaDiscoveryQueryRunes+1), false); err == nil {
		t.Fatal("oversized query was accepted")
	}
	discovery, ok := NewCinemaDiscovery().(*maoyanCinemaDiscovery)
	if !ok || discovery.citiesURL != maoyanCitiesURL || discovery.searchURL != maoyanSearchURL {
		t.Fatalf("production discovery endpoints are not fixed: %#v", discovery)
	}
}
