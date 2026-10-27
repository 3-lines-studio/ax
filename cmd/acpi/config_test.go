package main

import "testing"

// acpi must not pin a provider: when ACPI_MODEL/ACPI_BASE_URL are unset, the
// empty flags let ax apply its own defaults and bot.toml.
func TestLoadConfigLeavesProviderToAx(t *testing.T) {
	t.Setenv("ACPI_AX_PATH", "")
	t.Setenv("ACPI_MODEL", "")
	t.Setenv("ACPI_BASE_URL", "")
	t.Setenv("ACPI_SYSTEM_FILE", "")
	t.Setenv("ACPI_SESSION_DIR", "")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.AxPath != "ax" {
		t.Errorf("AxPath = %q, want ax", cfg.AxPath)
	}
	if cfg.Model != "" || cfg.Base != "" {
		t.Errorf("Model, Base = %q, %q; want empty so ax decides", cfg.Model, cfg.Base)
	}
	if cfg.SessionDir != "data/sessions" {
		t.Errorf("SessionDir = %q, want data/sessions", cfg.SessionDir)
	}
}
