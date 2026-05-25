package db

import (
	"strings"
	"testing"

	"smart-router/internal/types"
)

func TestEncryptDecryptRoundtrip(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}

	plaintext := "sk-test-secret-key-12345"
	enc, err := encryptValue(key, plaintext)
	if err != nil {
		t.Fatalf("encrypt failed: %v", err)
	}
	if enc == plaintext {
		t.Fatal("ciphertext should differ from plaintext")
	}
	if enc == "" {
		t.Fatal("ciphertext should not be empty")
	}

	dec, err := decryptValue(key, enc)
	if err != nil {
		t.Fatalf("decrypt failed: %v", err)
	}
	if dec != plaintext {
		t.Fatalf("expected %q, got %q", plaintext, dec)
	}
}

func TestEncryptDecryptEmpty(t *testing.T) {
	key := make([]byte, 32)
	enc, err := encryptValue(key, "")
	if err != nil {
		t.Fatalf("encrypt empty failed: %v", err)
	}
	if enc != "" {
		t.Fatalf("expected empty, got %q", enc)
	}
}

func TestDecryptPlaintextLegacy(t *testing.T) {
	key := make([]byte, 32)
	plaintext := "legacy-plaintext-key"
	dec, err := decryptValue(key, plaintext)
	if err != nil {
		t.Fatalf("decrypt legacy failed: %v", err)
	}
	if dec != plaintext {
		t.Fatalf("expected %q, got %q", plaintext, dec)
	}
}

func TestDecryptWithoutKey(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	plaintext := "secret"
	enc, _ := encryptValue(key, plaintext)

	_, err := decryptValue(nil, enc)
	if err == nil {
		t.Fatal("expected error decrypting with no key")
	}
}

func TestSavePlanEncryptsAndGetPlanDecrypts(t *testing.T) {
	tmp := t.TempDir() + "/test.db"
	database, err := Open(tmp)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	database.WithEncryptionKey(key)

	plan := types.PlanConfig{
		Providers: []types.ProviderConfig{
			{Name: "openai", APIKey: "sk-openai-secret"},
		},
	}
	if err := database.SavePlan("test", plan); err != nil {
		t.Fatalf("save plan: %v", err)
	}

	loaded, err := database.GetPlan("test")
	if err != nil {
		t.Fatalf("get plan: %v", err)
	}
	if len(loaded.Providers) != 1 || loaded.Providers[0].APIKey != "sk-openai-secret" {
		t.Fatalf("api_key not decrypted correctly: got %q", loaded.Providers[0].APIKey)
	}
}

func TestGetPlanLegacyPlaintext(t *testing.T) {
	tmp := t.TempDir() + "/test.db"
	database, err := Open(tmp)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	// Save without encryption key
	plan := types.PlanConfig{
		Providers: []types.ProviderConfig{
			{Name: "openai", APIKey: "sk-plaintext"},
		},
	}
	if err := database.SavePlan("legacy", plan); err != nil {
		t.Fatalf("save plan: %v", err)
	}

	// Now load WITH encryption key — should still work
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	database.WithEncryptionKey(key)

	loaded, err := database.GetPlan("legacy")
	if err != nil {
		t.Fatalf("get plan: %v", err)
	}
	if loaded.Providers[0].APIKey != "sk-plaintext" {
		t.Fatalf("expected legacy plaintext, got %q", loaded.Providers[0].APIKey)
	}
}

func TestSavePlanWithEmptyEncKeyStoresPlaintext(t *testing.T) {
	tmp := t.TempDir() + "/test.db"
	database, err := Open(tmp)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	// Set an empty (non-nil) encryption key
	database.WithEncryptionKey([]byte{})

	plan := types.PlanConfig{
		Providers: []types.ProviderConfig{
			{Name: "openai", APIKey: "sk-should-be-plain"},
		},
	}
	if err := database.SavePlan("empty-key", plan); err != nil {
		t.Fatalf("save plan: %v", err)
	}

	// Load back and verify the key is still plaintext (not prefixed with "enc:")
	loaded, err := database.GetPlan("empty-key")
	if err != nil {
		t.Fatalf("get plan: %v", err)
	}
	if loaded.Providers[0].APIKey != "sk-should-be-plain" {
		t.Fatalf("expected plaintext api key, got %q", loaded.Providers[0].APIKey)
	}
	if strings.HasPrefix(loaded.Providers[0].APIKey, "enc:") {
		t.Fatal("api key should not be encrypted with empty enc key")
	}
}
