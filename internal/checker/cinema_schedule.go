package checker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/watchbell/watchbell/internal/eventvars"
	"github.com/watchbell/watchbell/internal/model"
)

const (
	defaultCinemaScheduleProvider = "maoyan"
	defaultMaoyanURL              = "https://www.maoyan.com"
	maxCinemaScheduleBytes        = 5 * 1024 * 1024
	cinemaScheduleTimeZone        = "Asia/Shanghai"
)

type CinemaScheduleConfig struct {
	Provider          string   `json:"provider"`
	CinemaID          int64    `json:"cinemaId"`
	MovieID           int64    `json:"movieId"`
	MovieName         string   `json:"movieName"`
	TargetDate        string   `json:"targetDate"`
	HallPatterns      []string `json:"hallPatterns"`
	NotifyExisting    bool     `json:"notifyExisting"`
	NotifyNewSessions bool     `json:"notifyNewSessions"`
	TimeoutSeconds    int      `json:"timeoutSeconds"`
}

type cinemaScheduleState struct {
	Initialized    bool     `json:"initialized"`
	Source         string   `json:"source,omitempty"`
	Available      bool     `json:"available"`
	Expired        bool     `json:"expired,omitempty"`
	SeenSessionIDs []string `json:"seenSessionIds,omitempty"`
}

type cinemaSession struct {
	ID        string
	Date      string
	StartTime string
	EndTime   string
	Language  string
	Hall      string
	URL       string
}

type cinemaScheduleSnapshot struct {
	CinemaName string
	MovieName  string
	URL        string
	Sessions   []cinemaSession
}

// cinemaScheduleSource keeps provider-specific fetching and parsing behind a
// narrow boundary so another ticket platform can be added without changing
// monitor state or notification semantics.
type cinemaScheduleSource interface {
	Fetch(context.Context, model.Monitor, CinemaScheduleConfig) (cinemaScheduleSnapshot, error)
}

type maoyanScheduleSource struct {
	client  *http.Client
	baseURL string
}

type CinemaScheduleChecker struct {
	source cinemaScheduleSource
	now    func() time.Time
}

func NewCinemaScheduleChecker() *CinemaScheduleChecker {
	return &CinemaScheduleChecker{
		source: &maoyanScheduleSource{client: &http.Client{}, baseURL: defaultMaoyanURL},
		now:    time.Now,
	}
}

func (c *CinemaScheduleChecker) Type() string {
	return model.MonitorTypeCinemaSchedule
}

func (c *CinemaScheduleChecker) Plugin() model.MonitorPlugin {
	return model.MonitorPlugin{
		ID: model.MonitorTypeCinemaSchedule, Name: "影院排期", Builtin: true,
		Description:            "监控指定影院、影片和日期的 IMAX 或杜比影院排期。",
		DefaultIntervalSeconds: 900,
		DefaultConfig: map[string]any{
			"provider": defaultCinemaScheduleProvider,
			"cinemaId": 16655, "movieId": 1490607, "movieName": "蜘蛛侠：崭新之日",
			"targetDate":     "2026-08-01",
			"hallPatterns":   []string{"IMAX", "杜比影院", "Dolby Cinema"},
			"notifyExisting": false, "notifyNewSessions": false, "timeoutSeconds": 15,
		},
		ConfigFields: []model.PluginConfigField{
			{Key: "cinemaId", Label: "猫眼影院 ID", Type: "number", Required: true, Description: "影院页面地址 /cinema/ 后面的数字"},
			{Key: "movieId", Label: "猫眼影片 ID", Type: "number", Required: true, Description: "影院页面 movieId 参数中的数字"},
			{Key: "movieName", Label: "影片名称", Type: "string", Required: true},
			{Key: "targetDate", Label: "观影日期", Type: "date", Required: true},
			{Key: "hallPatterns", Label: "目标影厅", Type: "string-list", Required: true, Description: "按影厅名或版本名进行不区分大小写的文字匹配"},
			{Key: "notifyExisting", Label: "首次检查已有排期也通知", Type: "boolean"},
			{Key: "notifyNewSessions", Label: "已有排期后新增场次也通知", Type: "boolean"},
			{Key: "timeoutSeconds", Label: "超时时间（秒）", Type: "number"},
		},
		Events:            []string{"cinema.schedule.available"},
		TemplateVariables: eventvars.EventVariableKeys(model.MonitorTypeCinemaSchedule),
	}
}

func (c *CinemaScheduleChecker) Check(ctx context.Context, monitor model.Monitor) (model.CheckResult, error) {
	cfg, sourceKey, err := decodeCinemaScheduleConfig(monitor)
	if err != nil {
		return model.CheckResult{}, err
	}
	state := DecodeState(monitor, cinemaScheduleState{})
	if state.Source != "" && state.Source != sourceKey {
		state = cinemaScheduleState{}
	}
	state.Source = sourceKey

	if c.targetDateExpired(cfg.TargetDate) {
		state.Initialized = true
		state.Available = false
		state.Expired = true
		return model.CheckResult{
			Status: "ok", Message: "目标观影日期已结束", State: stateToMap(state),
		}, nil
	}

	snapshot, err := c.source.Fetch(ctx, monitor, cfg)
	if err != nil {
		return model.CheckResult{}, err
	}
	seen := make(map[string]struct{}, len(state.SeenSessionIDs))
	for _, id := range state.SeenSessionIDs {
		seen[id] = struct{}{}
	}

	eventSessions := make([]cinemaSession, 0)
	if len(snapshot.Sessions) > 0 {
		switch {
		case !state.Initialized && cfg.NotifyExisting:
			eventSessions = append(eventSessions, snapshot.Sessions...)
		case state.Initialized && !state.Available:
			eventSessions = append(eventSessions, snapshot.Sessions...)
		case state.Initialized && state.Available && cfg.NotifyNewSessions:
			for _, session := range snapshot.Sessions {
				if _, ok := seen[session.ID]; !ok {
					eventSessions = append(eventSessions, session)
				}
			}
		}
	}

	events := make([]model.EventData, 0, 1)
	if len(eventSessions) > 0 {
		events = append(events, cinemaScheduleEvent(cfg, snapshot, eventSessions))
	}

	state.Initialized = true
	state.Available = len(snapshot.Sessions) > 0
	state.Expired = false
	for _, session := range snapshot.Sessions {
		seen[session.ID] = struct{}{}
	}
	state.SeenSessionIDs = sortedStringSet(seen)

	message := "暂无匹配的影院排期"
	if state.Available {
		message = fmt.Sprintf("发现 %d 场匹配的影院排期", len(snapshot.Sessions))
	}
	return model.CheckResult{
		Status: "ok", Message: message, State: stateToMap(state), Events: events,
	}, nil
}

func (c *CinemaScheduleChecker) Inspect(ctx context.Context, monitor model.Monitor) (model.Observation, error) {
	cfg, _, err := decodeCinemaScheduleConfig(monitor)
	if err != nil {
		return model.Observation{}, err
	}
	if c.targetDateExpired(cfg.TargetDate) {
		snapshot := cinemaScheduleSnapshot{
			MovieName: cfg.MovieName,
			URL:       cinemaScheduleURL(defaultMaoyanURL, cfg.CinemaID, cfg.MovieID),
		}
		return model.Observation{
			Type: "cinema.schedule.available", Message: "目标观影日期已结束", Available: false,
			Payload: cinemaSchedulePayload(cfg, snapshot, nil, "expired"),
		}, nil
	}
	snapshot, err := c.source.Fetch(ctx, monitor, cfg)
	if err != nil {
		return model.Observation{}, err
	}
	available := len(snapshot.Sessions) > 0
	message := "暂无匹配的影院排期"
	if available {
		message = fmt.Sprintf("发现 %d 场匹配的影院排期", len(snapshot.Sessions))
	}
	observation := model.Observation{
		Type: "cinema.schedule.available", Message: message, Available: available,
		Payload: cinemaSchedulePayload(cfg, snapshot, snapshot.Sessions, availabilityStatus(available)),
	}
	if available {
		observation.Fingerprint = cinemaScheduleFingerprint(cfg, snapshot.Sessions)
	}
	return observation, nil
}

func decodeCinemaScheduleConfig(monitor model.Monitor) (CinemaScheduleConfig, string, error) {
	cfg, err := DecodeConfig(monitor, CinemaScheduleConfig{
		Provider: defaultCinemaScheduleProvider, TimeoutSeconds: 15,
		HallPatterns: []string{"IMAX", "杜比影院", "Dolby Cinema"},
	})
	if err != nil {
		return CinemaScheduleConfig{}, "", err
	}
	cfg.Provider = strings.ToLower(strings.TrimSpace(cfg.Provider))
	if cfg.Provider == "" {
		cfg.Provider = defaultCinemaScheduleProvider
	}
	if cfg.Provider != defaultCinemaScheduleProvider {
		return CinemaScheduleConfig{}, "", fmt.Errorf("unsupported cinema schedule provider %q", cfg.Provider)
	}
	if cfg.CinemaID <= 0 {
		return CinemaScheduleConfig{}, "", fmt.Errorf("cinema id must be greater than zero")
	}
	if cfg.MovieID <= 0 {
		return CinemaScheduleConfig{}, "", fmt.Errorf("movie id must be greater than zero")
	}
	cfg.MovieName = strings.TrimSpace(cfg.MovieName)
	if cfg.MovieName == "" {
		return CinemaScheduleConfig{}, "", fmt.Errorf("movie name is required")
	}
	if _, err := time.Parse("2006-01-02", cfg.TargetDate); err != nil {
		return CinemaScheduleConfig{}, "", fmt.Errorf("target date must use YYYY-MM-DD format")
	}
	cfg.HallPatterns = normalizedHallPatterns(cfg.HallPatterns)
	if len(cfg.HallPatterns) == 0 {
		return CinemaScheduleConfig{}, "", fmt.Errorf("at least one hall pattern is required")
	}
	if cfg.TimeoutSeconds <= 0 {
		cfg.TimeoutSeconds = 15
	}
	if cfg.TimeoutSeconds > 120 {
		cfg.TimeoutSeconds = 120
	}
	sourceKey := fmt.Sprintf(
		"%s|%d|%d|%s|%s",
		cfg.Provider, cfg.CinemaID, cfg.MovieID, cfg.TargetDate,
		strings.Join(cfg.HallPatterns, "\x1f"),
	)
	return cfg, sourceKey, nil
}

func (c *CinemaScheduleChecker) targetDateExpired(targetDate string) bool {
	location, err := time.LoadLocation(cinemaScheduleTimeZone)
	if err != nil {
		location = time.FixedZone("CST", 8*60*60)
	}
	now := time.Now()
	if c != nil && c.now != nil {
		now = c.now()
	}
	return now.In(location).Format("2006-01-02") > targetDate
}

func (s *maoyanScheduleSource) Fetch(
	ctx context.Context,
	monitor model.Monitor,
	cfg CinemaScheduleConfig,
) (cinemaScheduleSnapshot, error) {
	endpoint := cinemaScheduleURL(s.baseURL, cfg.CinemaID, cfg.MovieID)
	requestCtx, cancel := context.WithTimeout(ctx, time.Duration(cfg.TimeoutSeconds)*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, endpoint, nil)
	if err != nil {
		return cinemaScheduleSnapshot{}, err
	}
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")
	req.Header.Set("User-Agent", "WatchBell/0.1 (+self-hosted schedule monitor)")

	baseClient, err := clientForMonitor(s.client, monitor)
	if err != nil {
		return cinemaScheduleSnapshot{}, err
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		return cinemaScheduleSnapshot{}, fmt.Errorf("create cinema schedule cookie jar: %w", err)
	}
	client := *baseClient
	client.Jar = jar
	resp, err := client.Do(req)
	if err != nil {
		return cinemaScheduleSnapshot{}, fmt.Errorf("fetch maoyan cinema schedule: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return cinemaScheduleSnapshot{}, fmt.Errorf("maoyan cinema schedule fetch failed: http %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxCinemaScheduleBytes+1))
	if err != nil {
		return cinemaScheduleSnapshot{}, fmt.Errorf("read maoyan cinema schedule: %w", err)
	}
	if len(body) > maxCinemaScheduleBytes {
		return cinemaScheduleSnapshot{}, fmt.Errorf("maoyan cinema schedule response exceeds %d bytes", maxCinemaScheduleBytes)
	}
	return parseMaoyanCinemaSchedule(body, endpoint, cfg)
}

func parseMaoyanCinemaSchedule(
	body []byte,
	endpoint string,
	cfg CinemaScheduleConfig,
) (cinemaScheduleSnapshot, error) {
	document, err := goquery.NewDocumentFromReader(strings.NewReader(string(body)))
	if err != nil {
		return cinemaScheduleSnapshot{}, fmt.Errorf("parse maoyan cinema schedule: %w", err)
	}
	cinemaName := cleanCinemaText(document.Find("h1.name").First().Text())
	if cinemaName == "" || document.Find(".movie-list-container").Length() == 0 {
		return cinemaScheduleSnapshot{}, fmt.Errorf("maoyan cinema schedule page is missing expected content")
	}
	snapshot := cinemaScheduleSnapshot{
		CinemaName: cinemaName,
		MovieName:  cfg.MovieName,
		URL:        endpoint,
	}

	movieID := strconv.FormatInt(cfg.MovieID, 10)
	movieIndex := ""
	document.Find(".movie-list .movie").EachWithBreak(func(_ int, selection *goquery.Selection) bool {
		if value, ok := selection.Attr("data-movieid"); ok && strings.TrimSpace(value) == movieID {
			movieIndex, _ = selection.Attr("data-index")
			return false
		}
		return true
	})
	if movieIndex == "" {
		return snapshot, nil
	}

	showList := document.Find(".show-list").FilterFunction(func(_ int, selection *goquery.Selection) bool {
		value, _ := selection.Attr("data-index")
		return strings.TrimSpace(value) == strings.TrimSpace(movieIndex)
	}).First()
	if showList.Length() == 0 {
		return snapshot, nil
	}
	if movieName := cleanCinemaText(showList.Find(".movie-info .movie-name").First().Text()); movieName != "" {
		snapshot.MovieName = movieName
	}

	showList.Find(".plist-container").Each(func(_ int, container *goquery.Selection) {
		container.Find("tbody tr").Each(func(_ int, row *goquery.Selection) {
			session, ok := parseMaoyanSession(row, endpoint, cfg.TargetDate)
			if !ok || !matchesHallPatterns(session.Hall+" "+session.Language, cfg.HallPatterns) {
				return
			}
			snapshot.Sessions = append(snapshot.Sessions, session)
		})
	})
	sort.Slice(snapshot.Sessions, func(i, j int) bool {
		if snapshot.Sessions[i].StartTime == snapshot.Sessions[j].StartTime {
			return snapshot.Sessions[i].ID < snapshot.Sessions[j].ID
		}
		return snapshot.Sessions[i].StartTime < snapshot.Sessions[j].StartTime
	})
	return snapshot, nil
}

func parseMaoyanSession(row *goquery.Selection, endpoint, targetDate string) (cinemaSession, bool) {
	href, _ := row.Find(`a[href*="/xseats/"]`).First().Attr("href")
	href = strings.TrimSpace(href)
	if href == "" {
		return cinemaSession{}, false
	}
	sessionID := maoyanSessionID(href)
	if len(sessionID) < 8 {
		return cinemaSession{}, false
	}
	rawDate := sessionID[:8]
	if len(rawDate) != 8 {
		return cinemaSession{}, false
	}
	sessionDate := rawDate[:4] + "-" + rawDate[4:6] + "-" + rawDate[6:8]
	if sessionDate != targetDate {
		return cinemaSession{}, false
	}
	startTime := cleanCinemaText(row.Find(".begin-time").First().Text())
	endTime := strings.TrimSuffix(cleanCinemaText(row.Find(".end-time").First().Text()), "散场")
	language := cleanCinemaText(row.Find(".lang").First().Text())
	hall := cleanCinemaText(row.Find(".hall").First().Text())
	if hall == "" {
		return cinemaSession{}, false
	}
	return cinemaSession{
		ID: sessionID, Date: sessionDate, StartTime: startTime, EndTime: endTime,
		Language: language, Hall: hall, URL: resolveCinemaURL(endpoint, href),
	}, true
}

func maoyanSessionID(href string) string {
	const marker = "/xseats/"
	index := strings.Index(href, marker)
	if index < 0 {
		return ""
	}
	value := href[index+len(marker):]
	if cut := strings.IndexAny(value, "?#/"); cut >= 0 {
		value = value[:cut]
	}
	return strings.TrimSpace(value)
}

func cinemaScheduleEvent(
	cfg CinemaScheduleConfig,
	snapshot cinemaScheduleSnapshot,
	sessions []cinemaSession,
) model.EventData {
	return model.EventData{
		Type:        "cinema.schedule.available",
		Fingerprint: cinemaScheduleFingerprint(cfg, sessions),
		Payload:     cinemaSchedulePayload(cfg, snapshot, sessions, "available"),
	}
}

func cinemaScheduleFingerprint(cfg CinemaScheduleConfig, sessions []cinemaSession) string {
	ids := make([]string, 0, len(sessions))
	for _, session := range sessions {
		ids = append(ids, session.ID)
	}
	sort.Strings(ids)
	sum := sha256.Sum256([]byte(strings.Join(ids, "\x1f")))
	return fmt.Sprintf(
		"cinema:schedule:%d:%d:%s:%s",
		cfg.CinemaID, cfg.MovieID, cfg.TargetDate, hex.EncodeToString(sum[:6]),
	)
}

func cinemaSchedulePayload(
	cfg CinemaScheduleConfig,
	snapshot cinemaScheduleSnapshot,
	sessions []cinemaSession,
	status string,
) map[string]any {
	sessionValues := make([]map[string]any, 0, len(sessions))
	hallSet := make(map[string]struct{})
	firstStartTime := ""
	purchaseURL := ""
	for _, session := range sessions {
		if firstStartTime == "" {
			firstStartTime = session.StartTime
		}
		if purchaseURL == "" {
			purchaseURL = session.URL
		}
		hallSet[session.Hall] = struct{}{}
		sessionValues = append(sessionValues, map[string]any{
			"id": session.ID, "date": session.Date, "startTime": session.StartTime,
			"endTime": session.EndTime, "language": session.Language,
			"hall": session.Hall, "url": session.URL,
		})
	}
	halls := sortedStringSet(hallSet)
	movieName := snapshot.MovieName
	if strings.TrimSpace(movieName) == "" {
		movieName = cfg.MovieName
	}
	summary := cinemaScheduleSummary(movieName, snapshot.CinemaName, cfg.TargetDate, sessions, status)
	return map[string]any{"cinema": map[string]any{
		"provider": cfg.Provider, "cinemaId": cfg.CinemaID, "cinemaName": snapshot.CinemaName,
		"movieId": cfg.MovieID, "movieName": movieName, "targetDate": cfg.TargetDate,
		"hallPatterns": cfg.HallPatterns, "sessionCount": len(sessions), "sessions": sessionValues,
		"firstStartTime": firstStartTime, "halls": halls, "url": snapshot.URL,
		"purchaseUrl": purchaseURL, "summary": summary, "status": status,
	}}
}

func cinemaScheduleSummary(
	movieName, cinemaName, targetDate string,
	sessions []cinemaSession,
	status string,
) string {
	subject := strings.TrimSpace(movieName)
	if cinemaName != "" {
		subject += "在" + cinemaName
	}
	switch status {
	case "expired":
		return fmt.Sprintf("%s的 %s 观影日期已结束", subject, targetDate)
	case "available":
		parts := make([]string, 0, len(sessions))
		for _, session := range sessions {
			parts = append(parts, strings.TrimSpace(session.StartTime+" "+session.Hall))
		}
		return fmt.Sprintf("%s的 %s 已有 %d 场目标影厅排期：%s", subject, targetDate, len(sessions), strings.Join(parts, "；"))
	default:
		return fmt.Sprintf("%s的 %s 暂无目标影厅排期", subject, targetDate)
	}
}

func cinemaScheduleURL(baseURL string, cinemaID, movieID int64) string {
	base, err := url.Parse(strings.TrimRight(baseURL, "/") + "/")
	if err != nil {
		return ""
	}
	relative := &url.URL{Path: "cinema/" + strconv.FormatInt(cinemaID, 10)}
	query := relative.Query()
	query.Set("movieId", strconv.FormatInt(movieID, 10))
	relative.RawQuery = query.Encode()
	return base.ResolveReference(relative).String()
}

func resolveCinemaURL(baseURL, href string) string {
	base, err := url.Parse(baseURL)
	if err != nil {
		return href
	}
	relative, err := url.Parse(href)
	if err != nil {
		return href
	}
	return base.ResolveReference(relative).String()
}

func normalizedHallPatterns(patterns []string) []string {
	seen := make(map[string]struct{})
	result := make([]string, 0, len(patterns))
	for _, pattern := range patterns {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			continue
		}
		key := strings.ToLower(pattern)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, pattern)
	}
	return result
}

func matchesHallPatterns(value string, patterns []string) bool {
	value = strings.ToLower(value)
	for _, pattern := range patterns {
		if strings.Contains(value, strings.ToLower(strings.TrimSpace(pattern))) {
			return true
		}
	}
	return false
}

func cleanCinemaText(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func sortedStringSet[T ~string](set map[T]struct{}) []string {
	result := make([]string, 0, len(set))
	for value := range set {
		result = append(result, string(value))
	}
	sort.Strings(result)
	return result
}

func availabilityStatus(available bool) string {
	if available {
		return "available"
	}
	return "unavailable"
}
