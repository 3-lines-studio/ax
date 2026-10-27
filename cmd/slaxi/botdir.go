package main

import "github.com/3-lines-studio/ax/internal/botdir"

type botConfig struct {
	Model   string     `toml:"model"`
	BaseURL string     `toml:"base_url"`
	Tools   []string   `toml:"tools"`
	Slack   slackTable `toml:"slack"`
}

type slackTable struct {
	MentionOnly bool `toml:"mention_only"`
}

func loadBotConfig(root string) (botConfig, error) {
	var bc botConfig
	err := botdir.LoadTOML(root, &bc)
	return bc, err
}
