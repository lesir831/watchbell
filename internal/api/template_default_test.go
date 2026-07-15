package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/watchbell/watchbell/internal/model"
)

func TestDefaultTemplateUsesExplicitFlagAndCannotBeDeleted(t *testing.T) {
	server, _ := newTestServer(t)
	response, err := http.Get(server.URL + "/api/templates")
	if err != nil {
		t.Fatal(err)
	}
	var templates []model.NotificationTemplate
	if err := json.NewDecoder(response.Body).Decode(&templates); err != nil {
		response.Body.Close()
		t.Fatal(err)
	}
	response.Body.Close()
	if len(templates) == 0 || !templates[0].IsDefault {
		t.Fatalf("default template flag missing: %#v", templates)
	}

	request, err := http.NewRequest(http.MethodDelete, server.URL+"/api/templates/1", nil)
	if err != nil {
		t.Fatal(err)
	}
	deleted, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer deleted.Body.Close()
	if deleted.StatusCode != http.StatusConflict {
		t.Fatalf("delete default status = %d, want %d", deleted.StatusCode, http.StatusConflict)
	}
}
