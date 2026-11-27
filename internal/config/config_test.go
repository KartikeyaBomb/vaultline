package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "vaultline.yaml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg != Default() {
		t.Fatalf("got %+v, want defaults", cfg)
	}
}

func TestLoadOverridesDefaults(t *testing.T) {
	cfg, err := Load(writeConfig(t, "log_level: debug\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LogLevel != "debug" || cfg.DataDir != Default().DataDir {
		t.Fatalf("got %+v", cfg)
	}
}

func TestLoadEmptyFile(t *testing.T) {
	cfg, err := Load(writeConfig(t, ""))
	if err != nil {
		t.Fatal(err)
	}
	if cfg != Default() {
		t.Fatalf("got %+v, want defaults", cfg)
	}
}

func TestLoadRejectsUnknownKey(t *testing.T) {
	if _, err := Load(writeConfig(t, "log_levle: debug\n")); err == nil {
		t.Fatal("typo'd key accepted")
	}
}

func TestLoadRejectsBadLevel(t *testing.T) {
	if _, err := Load(writeConfig(t, "log_level: loud\n")); err == nil {
		t.Fatal("invalid level accepted")
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "nope.yaml")); err == nil {
		t.Fatal("missing file accepted")
	}
}
