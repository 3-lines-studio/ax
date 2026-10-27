// Package botdir implements the Botdir conventions this repository's
// programs share: where a bot keeps its definition and runtime data, how
// bot.toml and secrets are read, and which directory a standalone tool
// provider should work in.
//
// The normative specification is docs/botdir/BOTDIR.md.
package botdir

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// Paths are the directories and inputs a consumer derives from a bot root.
// Definition paths (Skills, Prompt) stay on the bot root; runtime paths
// (Workspace) resolve under BOT_DATA when it is set.
type Paths struct {
	Root      string
	DataRoot  string
	Workspace string
	Skills    string
	Prompt    string
	APIKey    string
	HasAPIKey bool
	IsBot     bool
}

// LoadTOML decodes root/bot.toml into v. A missing bot.toml is not an
// error: standalone use simply has no bot configuration.
func LoadTOML(root string, v any) error {
	data, err := os.ReadFile(filepath.Join(root, "bot.toml"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read bot.toml: %w", err)
	}
	if err := toml.Unmarshal(data, v); err != nil {
		return fmt.Errorf("parse bot.toml: %w", err)
	}
	return nil
}

// Resolve derives a consumer's paths from root. When root is a bot and the
// runtime workspace is missing, Resolve creates it.
func Resolve(root string) Paths {
	paths := Paths{Root: root, IsBot: Exists(filepath.Join(root, "bot.toml"))}
	paths.DataRoot = DataRoot(root)
	workspace := filepath.Join(paths.DataRoot, "workspace")
	if IsDir(workspace) {
		paths.Workspace = workspace
	} else if paths.IsBot {
		if err := os.MkdirAll(workspace, 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "warning: create workspace: %v\n", err)
		}
		paths.Workspace = workspace
	}
	if paths.Workspace == "" {
		paths.Workspace = paths.DataRoot
	}
	if skills := filepath.Join(root, "skills"); IsDir(skills) {
		paths.Skills = skills
	}
	if prompt := filepath.Join(root, "bot.md"); Exists(prompt) {
		if data, err := os.ReadFile(prompt); err == nil {
			paths.Prompt = strings.TrimSpace(string(data))
		}
	}
	if key := filepath.Join(root, "secrets", "api-key"); Exists(key) {
		if data, err := os.ReadFile(key); err == nil {
			paths.APIKey = strings.TrimSpace(string(data))
			paths.HasAPIKey = true
		}
	}
	return paths
}

// DataRoot is the runtime data root: BOT_DATA when a host provides one,
// otherwise the bot root itself.
func DataRoot(root string) string {
	if d := os.Getenv("BOT_DATA"); d != "" {
		return d
	}
	return root
}

// EnvRoot is the runtime root a host passed to this process: BOT_DATA when
// set, otherwise BOT_ROOT, otherwise empty (standalone).
func EnvRoot() string {
	if data := os.Getenv("BOT_DATA"); data != "" {
		return data
	}
	return os.Getenv("BOT_ROOT")
}

// Workspace is the directory a tool provider works in when it was started
// with BOT_ROOT/BOT_DATA in the environment: the runtime root's workspace/
// when it exists, the runtime root otherwise, and ./workspace (or the current
// directory) when running standalone. The path is not made absolute or
// validated; callers that need a filesystem root must do that themselves.
func Workspace() string {
	root := EnvRoot()
	if root != "" {
		if workspace := filepath.Join(root, "workspace"); IsDir(workspace) {
			return workspace
		}
		return root
	}
	if IsDir(filepath.Join(".", "workspace")) {
		return filepath.Join(".", "workspace")
	}
	return "."
}

// ReadSecret prefers the environment variable and falls back to
// root/secrets/<fileName>. A missing file yields an empty value, not an
// error: callers decide whether the secret is required.
func ReadSecret(root, envName, fileName string) (string, error) {
	if v := os.Getenv(envName); v != "" {
		return v, nil
	}
	data, err := os.ReadFile(filepath.Join(root, "secrets", fileName))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("read secret %s: %w", fileName, err)
	}
	return strings.TrimSpace(string(data)), nil
}

// Exists reports whether path can be stat'ed.
func Exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// IsDir reports whether path exists and is a directory.
func IsDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
