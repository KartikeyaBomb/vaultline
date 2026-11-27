// Package config loads Vaultline configuration from a YAML file.
//
// Precedence, highest first: command-line flags, the config file, defaults.
// Flag overrides are applied by the CLI after Load.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"

	"gopkg.in/yaml.v3"
)

// Config is the top-level configuration. Fields are added as features land.
type Config struct {
	// LogLevel is one of debug, info, warn, error.
	LogLevel string `yaml:"log_level"`
	// DataDir is where a node keeps its chunks and metadata.
	DataDir string `yaml:"data_dir"`
}

// Default returns the built-in defaults.
func Default() Config {
	return Config{
		LogLevel: "info",
		DataDir:  "./data",
	}
}

// Load reads path on top of the defaults. An empty path returns the defaults.
// Unknown keys are rejected so typos don't silently do nothing.
func Load(path string) (Config, error) {
	cfg := Default()
	if path == "" {
		return cfg, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return cfg, fmt.Errorf("config: %w", err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil && !errors.Is(err, io.EOF) {
		return cfg, fmt.Errorf("config: parse %s: %w", path, err)
	}
	return cfg, cfg.Validate()
}

// Validate checks that the config is usable.
func (c Config) Validate() error {
	if _, err := c.SlogLevel(); err != nil {
		return err
	}
	if c.DataDir == "" {
		return fmt.Errorf("config: data_dir must not be empty")
	}
	return nil
}

// SlogLevel parses LogLevel.
func (c Config) SlogLevel() (slog.Level, error) {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(c.LogLevel)); err != nil {
		return lvl, fmt.Errorf("config: invalid log_level %q (want debug, info, warn or error)", c.LogLevel)
	}
	return lvl, nil
}
