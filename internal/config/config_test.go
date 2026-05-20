package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadPlans(t *testing.T) {
	cfg, err := LoadFromFile("../../config/plans.yaml")
	if err != nil {
		t.Fatalf("LoadFromFile failed: %v", err)
	}

	if cfg == nil {
		t.Fatal("expected non-nil config")
	}

	if len(cfg.Plans) == 0 {
		t.Fatal("expected at least 1 plan")
	}

	jasonPlan, ok := cfg.Plans["jason"]
	if !ok {
		t.Fatal("expected 'jason' plan to exist")
	}

	if len(jasonPlan.Providers) != 5 {
		t.Fatalf("expected 5 providers in 'jason' plan, got %d", len(jasonPlan.Providers))
	}

	expectedProviders := []struct {
		name    string
		baseURL string
		model   string
		format  string
		timeout int
	}{
		{"jason-kimi-2", "https://api.kimi.com/coding/", "k2p6", "anthropic", 60},
		{"jason-kimi", "https://api.kimi.com/coding/", "k2p6", "anthropic", 60},
		{"jason-kimi-debbie", "https://api.kimi.com/coding/", "k2p6", "anthropic", 60},
		{"jason-volcengine", "https://ark.cn-beijing.volces.com/api/coding", "kimi-k2.6", "anthropic", 60},
		{"jason-minimax", "https://api.minimaxi.com/anthropic", "minimax2.7", "anthropic", 60},
	}

	for i, exp := range expectedProviders {
		p := jasonPlan.Providers[i]
		if p.Name != exp.name {
			t.Errorf("provider[%d].Name = %q, want %q", i, p.Name, exp.name)
		}
		if p.BaseURL != exp.baseURL {
			t.Errorf("provider[%d].BaseURL = %q, want %q", i, p.BaseURL, exp.baseURL)
		}
		if p.Model != exp.model {
			t.Errorf("provider[%d].Model = %q, want %q", i, p.Model, exp.model)
		}
		if p.Format != exp.format {
			t.Errorf("provider[%d].Format = %q, want %q", i, p.Format, exp.format)
		}
		if p.Timeout != exp.timeout {
			t.Errorf("provider[%d].Timeout = %d, want %d", i, p.Timeout, exp.timeout)
		}
	}
}

func TestLoadFromFile_NotExist(t *testing.T) {
	_, err := LoadFromFile("/nonexistent/path/plans.yaml")
	if err == nil {
		t.Fatal("expected error for nonexistent file, got nil")
	}
}

func TestLoadFromFile_InvalidYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "invalid.yaml")
	if err := os.WriteFile(path, []byte("not: valid: yaml: ["), 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	_, err := LoadFromFile(path)
	if err == nil {
		t.Fatal("expected error for invalid YAML, got nil")
	}
}

func TestLoadFromFile_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.yaml")
	if err := os.WriteFile(path, []byte(""), 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	cfg, err := LoadFromFile(path)
	if err != nil {
		t.Fatalf("LoadFromFile failed for empty file: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config for empty file")
	}
	if len(cfg.Plans) != 0 {
		t.Fatalf("expected 0 plans for empty file, got %d", len(cfg.Plans))
	}
}

func TestLoadFromFile_EmptyPlans(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty_plans.yaml")
	content := `plans: {}`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	cfg, err := LoadFromFile(path)
	if err != nil {
		t.Fatalf("LoadFromFile failed: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
	if len(cfg.Plans) != 0 {
		t.Fatalf("expected 0 plans, got %d", len(cfg.Plans))
	}
}
