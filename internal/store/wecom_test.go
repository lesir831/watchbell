package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestWeComReceiptSurvivesRestartAndSeparatesChannels(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "watchbell.db")
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := db.ClaimWeComCallback(ctx, 1, "message")
	if err != nil || !claimed {
		t.Fatalf("claim: %t %v", claimed, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	claimed, err = db.ClaimWeComCallback(ctx, 1, "message")
	if err != nil || claimed {
		t.Fatalf("replay after restart: %t %v", claimed, err)
	}
	claimed, err = db.ClaimWeComCallback(ctx, 2, "message")
	if err != nil || !claimed {
		t.Fatalf("channels not isolated: %t %v", claimed, err)
	}
}
