package main

import (
	"fmt"
	"os"
)

// Config is acpi's own configuration, read from env so it fits the rest of
// the AX ecosystem's env-only convention (see ax's run-ax.sh in alfred).
type Config struct {
	AxPath     string // ACPI_AX_PATH: path to the ax binary
	Model      string // ACPI_MODEL: passed to ax as -model
	Base       string // ACPI_BASE_URL: passed to ax as -base
	System     string // contents of ACPI_SYSTEM_FILE, passed to ax as -system
	SessionDir string // ACPI_SESSION_DIR: where per-session ax --session files live
}

func loadConfig() (*Config, error) {
	// Model and Base are empty by default: ax applies its own defaults
	// (DeepSeek) and bot.toml, so acpi does not pin a provider.
	cfg := &Config{
		AxPath:     envOr("ACPI_AX_PATH", "ax"),
		Model:      os.Getenv("ACPI_MODEL"),
		Base:       os.Getenv("ACPI_BASE_URL"),
		SessionDir: envOr("ACPI_SESSION_DIR", "data/sessions"),
	}
	if systemFile := os.Getenv("ACPI_SYSTEM_FILE"); systemFile != "" {
		data, err := os.ReadFile(systemFile)
		if err != nil {
			return nil, fmt.Errorf("acpi: reading ACPI_SYSTEM_FILE %q: %w", systemFile, err)
		}
		cfg.System = string(data)
	}
	return cfg, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
