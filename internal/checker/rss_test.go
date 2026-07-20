package checker

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/watchbell/watchbell/internal/model"
)

func TestRSSCheckerUsesChineseNoNewArticleMessage(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 2 {
			if got := r.Header.Get("If-None-Match"); got != `"feed-v1"` {
				t.Errorf("If-None-Match = %q, want feed-v1", got)
			}
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Content-Type", "application/rss+xml")
		w.Header().Set("ETag", `"feed-v1"`)
		_, _ = fmt.Fprint(w, `<?xml version="1.0"?><rss version="2.0"><channel><title>Feed</title><item><guid>one</guid><title>First</title></item></channel></rss>`)
	}))
	defer server.Close()

	checker := NewRSSChecker()
	monitor := model.Monitor{Config: testJSON(t, RSSConfig{URL: server.URL})}
	first, err := checker.Check(context.Background(), monitor)
	if err != nil {
		t.Fatal(err)
	}
	if first.Message != "暂无新增文章" || len(first.Events) != 0 {
		t.Fatalf("first result = %#v", first)
	}

	monitor.State = testJSON(t, first.State)
	unchanged, err := checker.Check(context.Background(), monitor)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Message != "暂无新增文章" || len(unchanged.Events) != 0 {
		t.Fatalf("unchanged result = %#v", unchanged)
	}
}
