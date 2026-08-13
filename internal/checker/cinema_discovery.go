package checker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/watchbell/watchbell/internal/model"
)

const (
	maoyanCitiesURL              = "https://m.maoyan.com/dianying/cities.json"
	maoyanSearchURL              = "https://m.maoyan.com/ajax/search"
	maxCinemaDiscoveryBytes      = 2 * 1024 * 1024
	maxCinemaDiscoveryQueryRunes = 100
	cinemaDiscoveryTimeout       = 10 * time.Second
)

// CinemaCity is the stable subset of a Maoyan city used by WatchBell's
// configuration UI. Provider-specific field names never leave the backend.
type CinemaCity struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Pinyin string `json:"pinyin"`
}

type CinemaVenue struct {
	ID        int64    `json:"id"`
	Name      string   `json:"name"`
	Address   string   `json:"address"`
	Distance  string   `json:"distance,omitempty"`
	HallTypes []string `json:"hallTypes"`
}

type CinemaMovie struct {
	ID                 int64    `json:"id"`
	Name               string   `json:"name"`
	EnglishName        string   `json:"englishName,omitempty"`
	Alias              string   `json:"alias,omitempty"`
	ReleaseDate        string   `json:"releaseDate,omitempty"`
	ReleaseDescription string   `json:"releaseDescription,omitempty"`
	Categories         []string `json:"categories"`
	Version            string   `json:"version,omitempty"`
}

type CinemaCityResult struct {
	Items []CinemaCity `json:"items"`
	Total int          `json:"total"`
}

type CinemaVenueResult struct {
	Items []CinemaVenue `json:"items"`
	Total int           `json:"total"`
}

type CinemaMovieResult struct {
	Items []CinemaMovie `json:"items"`
	Total int           `json:"total"`
}

// CinemaDiscovery is kept behind an interface so API tests can verify request
// validation and proxy resolution without reaching Maoyan.
type CinemaDiscovery interface {
	Cities(context.Context, string, *model.ProxyProfile) (CinemaCityResult, error)
	Cinemas(context.Context, int64, string, *model.ProxyProfile) (CinemaVenueResult, error)
	Movies(context.Context, int64, string, *model.ProxyProfile) (CinemaMovieResult, error)
}

type maoyanCinemaDiscovery struct {
	client    *http.Client
	citiesURL string
	searchURL string
}

func NewCinemaDiscovery() CinemaDiscovery {
	return &maoyanCinemaDiscovery{
		client:    &http.Client{},
		citiesURL: maoyanCitiesURL,
		searchURL: maoyanSearchURL,
	}
}

func (d *maoyanCinemaDiscovery) Cities(ctx context.Context, query string, proxyProfile *model.ProxyProfile) (CinemaCityResult, error) {
	query, err := normalizeCinemaDiscoveryQuery(query, false)
	if err != nil {
		return CinemaCityResult{}, err
	}
	var response struct {
		Cities []struct {
			ID     int64  `json:"id"`
			Name   string `json:"nm"`
			Pinyin string `json:"py"`
		} `json:"cts"`
	}
	client, err := clientForMonitor(d.client, model.Monitor{Proxy: proxyProfile})
	if err != nil {
		return CinemaCityResult{}, err
	}
	if err := d.fetchJSON(ctx, client, d.citiesURL, &response); err != nil {
		return CinemaCityResult{}, fmt.Errorf("fetch maoyan cities: %w", err)
	}

	needle := strings.ToLower(query)
	seen := make(map[int64]struct{}, len(response.Cities))
	items := make([]CinemaCity, 0, len(response.Cities))
	for _, item := range response.Cities {
		name := cleanCinemaText(item.Name)
		pinyin := strings.ToLower(strings.TrimSpace(item.Pinyin))
		if item.ID <= 0 || name == "" {
			continue
		}
		if _, duplicate := seen[item.ID]; duplicate {
			continue
		}
		if needle != "" && !strings.Contains(strings.ToLower(name), needle) && !strings.Contains(pinyin, needle) {
			continue
		}
		seen[item.ID] = struct{}{}
		items = append(items, CinemaCity{ID: item.ID, Name: name, Pinyin: pinyin})
	}
	return CinemaCityResult{Items: items, Total: len(items)}, nil
}

func (d *maoyanCinemaDiscovery) Cinemas(
	ctx context.Context,
	cityID int64,
	query string,
	proxyProfile *model.ProxyProfile,
) (CinemaVenueResult, error) {
	query, err := normalizeCinemaDiscoverySearch(cityID, query)
	if err != nil {
		return CinemaVenueResult{}, err
	}
	var response struct {
		Cinemas struct {
			List []struct {
				ID        int64    `json:"id"`
				Name      string   `json:"nm"`
				Address   string   `json:"addr"`
				Distance  string   `json:"distance"`
				HallTypes []string `json:"hallType"`
			} `json:"list"`
		} `json:"cinemas"`
	}
	endpoint := cinemaDiscoverySearchURL(d.searchURL, cityID, query, 2)
	client, err := clientForMonitor(d.client, model.Monitor{Proxy: proxyProfile})
	if err != nil {
		return CinemaVenueResult{}, err
	}
	if err := d.fetchJSON(ctx, client, endpoint, &response); err != nil {
		return CinemaVenueResult{}, fmt.Errorf("search maoyan cinemas: %w", err)
	}

	seen := make(map[int64]struct{}, len(response.Cinemas.List))
	items := make([]CinemaVenue, 0, len(response.Cinemas.List))
	for _, item := range response.Cinemas.List {
		name := cleanCinemaText(item.Name)
		if item.ID <= 0 || name == "" {
			continue
		}
		if _, duplicate := seen[item.ID]; duplicate {
			continue
		}
		seen[item.ID] = struct{}{}
		items = append(items, CinemaVenue{
			ID: item.ID, Name: name, Address: cleanCinemaText(item.Address),
			Distance: cleanCinemaText(item.Distance), HallTypes: normalizedStringList(item.HallTypes),
		})
	}
	return CinemaVenueResult{Items: items, Total: len(items)}, nil
}

func (d *maoyanCinemaDiscovery) Movies(
	ctx context.Context,
	cityID int64,
	query string,
	proxyProfile *model.ProxyProfile,
) (CinemaMovieResult, error) {
	query, err := normalizeCinemaDiscoverySearch(cityID, query)
	if err != nil {
		return CinemaMovieResult{}, err
	}
	var response struct {
		Movies struct {
			List []struct {
				ID                 int64  `json:"id"`
				Name               string `json:"nm"`
				EnglishName        string `json:"enm"`
				Alias              string `json:"movieAlias"`
				MovieType          *int   `json:"movieType"`
				ReleaseDate        string `json:"rt"`
				ReleaseDescription string `json:"pubDesc"`
				Categories         string `json:"cat"`
				Version            string `json:"ver"`
			} `json:"list"`
		} `json:"movies"`
	}
	endpoint := cinemaDiscoverySearchURL(d.searchURL, cityID, query, 0)
	client, err := clientForMonitor(d.client, model.Monitor{Proxy: proxyProfile})
	if err != nil {
		return CinemaMovieResult{}, err
	}
	if err := d.fetchJSON(ctx, client, endpoint, &response); err != nil {
		return CinemaMovieResult{}, fmt.Errorf("search maoyan movies: %w", err)
	}

	seen := make(map[int64]struct{}, len(response.Movies.List))
	items := make([]CinemaMovie, 0, len(response.Movies.List))
	for _, item := range response.Movies.List {
		name := cleanCinemaText(item.Name)
		if item.MovieType == nil || *item.MovieType != 0 || item.ID <= 0 || name == "" {
			continue
		}
		if _, duplicate := seen[item.ID]; duplicate {
			continue
		}
		seen[item.ID] = struct{}{}
		items = append(items, CinemaMovie{
			ID: item.ID, Name: name, EnglishName: cleanCinemaText(item.EnglishName),
			Alias: cleanCinemaText(item.Alias), ReleaseDate: cleanCinemaText(item.ReleaseDate),
			ReleaseDescription: cleanCinemaText(item.ReleaseDescription),
			Categories:         normalizedCinemaCategories(item.Categories), Version: cleanCinemaText(item.Version),
		})
	}
	return CinemaMovieResult{Items: items, Total: len(items)}, nil
}

func (d *maoyanCinemaDiscovery) fetchJSON(ctx context.Context, client *http.Client, endpoint string, destination any) error {
	requestCtx, cancel := context.WithTimeout(ctx, cinemaDiscoveryTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")
	req.Header.Set("User-Agent", "WatchBell/0.1 (+self-hosted cinema discovery)")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("perform request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("upstream returned http %d", resp.StatusCode)
	}
	mediaType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || (!strings.EqualFold(mediaType, "application/json") && !strings.HasSuffix(strings.ToLower(mediaType), "+json")) {
		return fmt.Errorf("upstream returned unexpected content type %q", resp.Header.Get("Content-Type"))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxCinemaDiscoveryBytes+1))
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if len(body) > maxCinemaDiscoveryBytes {
		return fmt.Errorf("response exceeds %d bytes", maxCinemaDiscoveryBytes)
	}
	if err := json.Unmarshal(body, destination); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

func normalizeCinemaDiscoverySearch(cityID int64, query string) (string, error) {
	if cityID <= 0 {
		return "", fmt.Errorf("city id must be greater than zero")
	}
	return normalizeCinemaDiscoveryQuery(query, true)
}

func normalizeCinemaDiscoveryQuery(query string, required bool) (string, error) {
	query = strings.TrimSpace(query)
	if required && query == "" {
		return "", fmt.Errorf("search query is required")
	}
	if len([]rune(query)) > maxCinemaDiscoveryQueryRunes {
		return "", fmt.Errorf("search query exceeds %d characters", maxCinemaDiscoveryQueryRunes)
	}
	return query, nil
}

func cinemaDiscoverySearchURL(baseURL string, cityID int64, query string, searchType int) string {
	endpoint, err := url.Parse(baseURL)
	if err != nil {
		return ""
	}
	values := endpoint.Query()
	values.Set("cityId", fmt.Sprint(cityID))
	values.Set("kw", query)
	values.Set("stype", fmt.Sprint(searchType))
	endpoint.RawQuery = values.Encode()
	return endpoint.String()
}

func normalizedStringList(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = cleanCinemaText(value)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, value)
	}
	if result == nil {
		return []string{}
	}
	return result
}

func normalizedCinemaCategories(value string) []string {
	return normalizedStringList(strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == '，' || r == '/' || r == '、'
	}))
}
