package checker

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/watchbell/watchbell/internal/model"
)

func TestCinemaScheduleCheckerUsesCookieRedirectAndNotifiesAvailabilityTransition(t *testing.T) {
	available := false
	requestsWithCookie := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cookie, err := r.Cookie("visitor"); err != nil || cookie.Value != "watchbell" {
			http.SetCookie(w, &http.Cookie{Name: "visitor", Value: "watchbell", Path: "/"})
			http.Redirect(w, r, r.URL.RequestURI(), http.StatusFound)
			return
		}
		requestsWithCookie++
		_, _ = w.Write([]byte(maoyanScheduleFixture(available)))
	}))
	defer server.Close()

	checker := &CinemaScheduleChecker{
		source: &maoyanScheduleSource{client: &http.Client{}, baseURL: server.URL},
		now:    func() time.Time { return time.Date(2026, 7, 24, 12, 0, 0, 0, time.FixedZone("CST", 8*60*60)) },
	}
	monitor := cinemaScheduleTestMonitor(t, CinemaScheduleConfig{
		CinemaID: 16655, MovieID: 1490607, MovieName: "蜘蛛侠：崭新之日",
		TargetDate: "2026-08-01", HallPatterns: []string{"IMAX", "杜比影院", "Dolby Cinema"},
		TimeoutSeconds: 5,
	})

	first, err := checker.Check(context.Background(), monitor)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Events) != 0 || first.Message != "暂无匹配的影院排期" {
		t.Fatalf("unexpected baseline result: %#v", first)
	}

	available = true
	monitor.State = testJSON(t, first.State)
	second, err := checker.Check(context.Background(), monitor)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Events) != 1 || second.Message != "发现 1 场匹配的影院排期" {
		t.Fatalf("unexpected transition result: %#v", second)
	}
	cinema := second.Events[0].Payload["cinema"].(map[string]any)
	if cinema["cinemaName"] != "万达影城（丰台万达广场杜比影院店）" ||
		cinema["movieName"] != "蜘蛛侠：崭新之日" ||
		cinema["sessionCount"] != 1 ||
		cinema["firstStartTime"] != "19:30" {
		t.Fatalf("unexpected cinema payload: %#v", cinema)
	}
	sessions := cinema["sessions"].([]map[string]any)
	if len(sessions) != 1 || sessions[0]["hall"] != "杜比影院" ||
		!strings.Contains(fmt.Sprint(sessions[0]["url"]), "/xseats/2026080101665502") {
		t.Fatalf("unexpected sessions: %#v", sessions)
	}
	if requestsWithCookie != 2 {
		t.Fatalf("got %d completed cookie requests, want 2", requestsWithCookie)
	}
}

func TestCinemaScheduleCheckerNotifyExistingAndOnlyNewSessions(t *testing.T) {
	source := &stubCinemaScheduleSource{snapshot: cinemaScheduleSnapshot{
		CinemaName: "示例影院", MovieName: "示例影片",
		URL: "https://example.com/cinema/1",
		Sessions: []cinemaSession{
			{ID: "2026080101", Date: "2026-08-01", StartTime: "10:00", Hall: "IMAX厅", URL: "https://example.com/session/1"},
			{ID: "2026080102", Date: "2026-08-01", StartTime: "13:00", Hall: "杜比影院", URL: "https://example.com/session/2"},
		},
	}}
	checker := &CinemaScheduleChecker{
		source: source,
		now:    func() time.Time { return time.Date(2026, 7, 24, 0, 0, 0, 0, time.UTC) },
	}
	monitor := cinemaScheduleTestMonitor(t, CinemaScheduleConfig{
		CinemaID: 1, MovieID: 2, MovieName: "示例影片", TargetDate: "2026-08-01",
		HallPatterns: []string{"IMAX", "杜比影院"}, NotifyExisting: true,
		NotifyNewSessions: true, TimeoutSeconds: 5,
	})

	first, err := checker.Check(context.Background(), monitor)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Events) != 1 {
		t.Fatalf("notifyExisting events = %d, want 1", len(first.Events))
	}
	cinema := first.Events[0].Payload["cinema"].(map[string]any)
	if cinema["sessionCount"] != 2 {
		t.Fatalf("initial aggregate payload = %#v", cinema)
	}

	source.snapshot.Sessions = append(source.snapshot.Sessions,
		cinemaSession{ID: "2026080103", Date: "2026-08-01", StartTime: "16:00", Hall: "IMAX厅", URL: "https://example.com/session/3"},
	)
	monitor.State = testJSON(t, first.State)
	second, err := checker.Check(context.Background(), monitor)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Events) != 1 {
		t.Fatalf("new-session events = %d, want 1", len(second.Events))
	}
	cinema = second.Events[0].Payload["cinema"].(map[string]any)
	if cinema["sessionCount"] != 1 || cinema["firstStartTime"] != "16:00" {
		t.Fatalf("new-session payload should only contain unseen sessions: %#v", cinema)
	}
}

func TestCinemaScheduleInspectDoesNotMutateStateAndExpiredCheckSkipsSource(t *testing.T) {
	source := &stubCinemaScheduleSource{snapshot: cinemaScheduleSnapshot{
		CinemaName: "示例影院", MovieName: "示例影片", URL: "https://example.com/cinema/1",
		Sessions: []cinemaSession{{
			ID: "2026080101", Date: "2026-08-01", StartTime: "10:00",
			Hall: "IMAX厅", URL: "https://example.com/session/1",
		}},
	}}
	checker := &CinemaScheduleChecker{
		source: source,
		now:    func() time.Time { return time.Date(2026, 7, 24, 0, 0, 0, 0, time.UTC) },
	}
	state := `{"initialized":true,"available":false,"seenSessionIds":[]}`
	monitor := cinemaScheduleTestMonitor(t, CinemaScheduleConfig{
		CinemaID: 1, MovieID: 2, MovieName: "示例影片", TargetDate: "2026-08-01",
		HallPatterns: []string{"IMAX"}, TimeoutSeconds: 5,
	})
	monitor.State = []byte(state)
	observation, err := checker.Inspect(context.Background(), monitor)
	if err != nil {
		t.Fatal(err)
	}
	if !observation.Available || observation.Fingerprint == "" || string(monitor.State) != state {
		t.Fatalf("unexpected read-only observation: %#v state=%s", observation, monitor.State)
	}

	checker.now = func() time.Time { return time.Date(2026, 8, 2, 0, 0, 0, 0, time.FixedZone("CST", 8*60*60)) }
	beforeCalls := source.calls
	result, err := checker.Check(context.Background(), monitor)
	if err != nil {
		t.Fatal(err)
	}
	if result.Message != "目标观影日期已结束" || source.calls != beforeCalls {
		t.Fatalf("expired result fetched source: result=%#v calls=%d", result, source.calls)
	}
}

func TestParseMaoyanCinemaScheduleRejectsUnexpectedPage(t *testing.T) {
	_, err := parseMaoyanCinemaSchedule([]byte(`<html><title>验证</title></html>`), "https://www.maoyan.com/cinema/1?movieId=2", CinemaScheduleConfig{
		MovieID: 2, MovieName: "示例影片", TargetDate: "2026-08-01", HallPatterns: []string{"IMAX"},
	})
	if err == nil || !strings.Contains(err.Error(), "missing expected content") {
		t.Fatalf("unexpected error: %v", err)
	}
}

type stubCinemaScheduleSource struct {
	snapshot cinemaScheduleSnapshot
	calls    int
}

func (s *stubCinemaScheduleSource) Fetch(context.Context, model.Monitor, CinemaScheduleConfig) (cinemaScheduleSnapshot, error) {
	s.calls++
	return s.snapshot, nil
}

func cinemaScheduleTestMonitor(t *testing.T, config CinemaScheduleConfig) model.Monitor {
	t.Helper()
	return model.Monitor{Type: model.MonitorTypeCinemaSchedule, Config: testJSON(t, config)}
}

func maoyanScheduleFixture(available bool) string {
	dolbyRow := ""
	if available {
		dolbyRow = `<tr>
			<td><span class="begin-time">19:30</span><span class="end-time">22:00 散场</span></td>
			<td class="lang">国语 2D</td><td class="hall">杜比影院</td>
			<td><a href="/xseats/2026080101665502?movieId=1490607">选座购票</a></td>
		</tr>`
	}
	return `<html><body>
		<h1 class="name">万达影城（丰台万达广场杜比影院店）</h1>
		<div class="movie-list-container">
			<div class="movie-list">
				<div class="movie" data-movieid="99" data-index="0"></div>
				<div class="movie" data-movieid="1490607" data-index="10"></div>
			</div>
			<div class="show-list" data-index="0">
				<div class="plist-container"><table><tbody><tr>
					<td><span class="begin-time">18:00</span></td><td class="lang">IMAX 2D</td><td class="hall">IMAX厅</td>
					<td><a href="/xseats/2026080101665599?movieId=99">选座购票</a></td>
				</tr></tbody></table></div>
			</div>
			<div class="show-list" data-index="10">
				<div class="movie-info"><span class="movie-name">蜘蛛侠：崭新之日</span></div>
				<div class="plist-container"><table><tbody>
					<tr>
						<td><span class="begin-time">08:50</span><span class="end-time">11:20 散场</span></td>
						<td class="lang">国语 2D</td><td class="hall">杜比全景声厅</td>
						<td><a href="/xseats/2026080101665501?movieId=1490607">选座购票</a></td>
					</tr>
					` + dolbyRow + `
					<tr>
						<td><span class="begin-time">09:00</span></td><td class="lang">IMAX 2D</td><td class="hall">IMAX厅</td>
						<td><a href="/xseats/2026080201665503?movieId=1490607">选座购票</a></td>
					</tr>
				</tbody></table></div>
			</div>
		</div>
	</body></html>`
}
