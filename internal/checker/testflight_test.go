package checker

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/watchbell/watchbell/internal/model"
)

func TestTestFlightCheckerNotifiesWhenStatusBecomesAvailable(t *testing.T) {
	body := "This beta is full."
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()

	checker := NewTestFlightChecker()
	monitor := model.Monitor{Config: testJSON(t, TestFlightConfig{URL: server.URL, TimeoutSeconds: 5})}
	full, err := checker.Check(context.Background(), monitor)
	if err != nil {
		t.Fatal(err)
	}
	if full.Status != "full" || full.Message != "TestFlight 测试名额已满" || len(full.Events) != 0 {
		t.Fatalf("unexpected full result: %#v", full)
	}

	monitor.State = testJSON(t, full.State)
	body = "Start testing. View in TestFlight."
	available, err := checker.Check(context.Background(), monitor)
	if err != nil {
		t.Fatal(err)
	}
	if available.Status != "available" || len(available.Events) != 1 {
		t.Fatalf("unexpected available result: %#v", available)
	}
	if available.Events[0].Type != "testflight.available" {
		t.Fatalf("unexpected event: %#v", available.Events[0])
	}
}
