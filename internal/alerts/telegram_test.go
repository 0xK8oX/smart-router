package alerts

import (
	"os"
	"testing"

	"smart-router/internal/db"
	"smart-router/internal/health"
)

func TestBotCommandParsing(t *testing.T) {
	dir, err := os.MkdirTemp("", "telegram-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	sqlitePath := dir + "/test.db"
	database, err := db.Open(sqlitePath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	ht, err := health.New(dir + "/health")
	if err != nil {
		t.Fatalf("open health: %v", err)
	}
	defer ht.Close()

	b := &Bot{
		token:  "dummy",
		db:     database,
		health: ht,
	}

	// Test help command
	reply := b.buildReply("/help")
	if reply == "" {
		t.Error("expected help reply")
	}

	// Test unknown command
	reply = b.buildReply("/unknown")
	if reply == "" {
		t.Error("expected unknown command reply")
	}
}
