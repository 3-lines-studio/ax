package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/3-lines-studio/ax/internal/botdir"
)

func doctor() int {
	root := os.Getenv("BOT_ROOT")
	if root == "" {
		wd, err := os.Getwd()
		if err != nil {
			fmt.Printf("bot rooterror: %v\n", err)
			return 1
		}
		root = wd
	}
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		fmt.Printf("bot root     missing: %s\n", root)
		return 1
	}
	fmt.Printf("bot root     %s\n", root)

	bc, err := loadBotConfig(root)
	if err != nil {
		fmt.Printf("bot.toml     error: %v\n", err)
		return 1
	}
	if bc.Model != "" {
		fmt.Printf("model        %s\n", bc.Model)
	} else {
		fmt.Printf("model        note: bot.toml has no model (default used)\n")
	}
	if bc.BaseURL != "" {
		fmt.Printf("base_url     %s\n", bc.BaseURL)
	}
	tools := bc.Tools
	if len(tools) == 0 {
		fmt.Printf("tools        note: bot.toml has no tools (AX runs without tools)\n")
	} else {
		fmt.Printf("tools        %s\n", strings.Join(tools, " "))
	}

	missing := false
	report := func(name string, ok bool) {
		if ok {
			fmt.Printf("   ok      %-32s\n", name)
		} else {
			fmt.Printf("   missing %-32s\n", name)
			missing = true
		}
	}

	appToken, err := botdir.ReadSecret(root, "SLACK_APP_TOKEN", "slack-app-token")
	if err != nil {
		fmt.Printf("secret       error: %v\n", err)
		missing = true
	} else {
		report("slack app token (SLACK_APP_TOKEN | secrets/slack-app-token)", appToken != "")
	}
	botToken, err := botdir.ReadSecret(root, "SLACK_BOT_TOKEN", "slack-bot-token")
	if err != nil {
		fmt.Printf("secret       error: %v\n", err)
		missing = true
	} else {
		report("slack bot token (SLACK_BOT_TOKEN | secrets/slack-bot-token)", botToken != "")
	}
	report("model key (OPENAI_API_KEY | secrets/api-key)",
		os.Getenv("OPENAI_API_KEY") != "" || botdir.Exists(filepath.Join(root, "secrets", "api-key")))

	axCmd := os.Getenv("SLAXI_AX_PATH")
	if axCmd == "" {
		axCmd = "ax"
	}
	report("ax binary ("+axCmd+")", axCommand(axCmd))

	for _, tool := range tools {
		report("tool "+tool, axCommand(tool))
	}

	if missing {
		fmt.Printf("\nmissing required inputs; see the lines above\n")
		return 1
	}
	fmt.Printf("\nall required inputs present\n")
	return 0
}

func axCommand(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}
