package botdir

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

type Paths struct {
	Root      string
	DataRoot  string
	State     string
	Workspace string
	Skills    string
	Routines  string
	Prompt    string
	APIKey    string
	HasAPIKey bool
	IsBot     bool
}

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

func Resolve(root string) Paths {
	return ResolveData(root, DataRoot(root))
}

func ResolveData(root, dataRoot string) Paths {
	if dataRoot == "" {
		dataRoot = root
	}
	paths := Paths{Root: root, IsBot: Exists(filepath.Join(root, "bot.toml"))}
	paths.DataRoot = dataRoot
	paths.State = filepath.Join(dataRoot, "state")
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
	if routines := filepath.Join(root, "routines"); IsDir(routines) {
		paths.Routines = routines
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

func DataRoot(root string) string {
	if d := os.Getenv("BOT_DATA"); d != "" {
		return d
	}
	return root
}

func StateRoot(root string) string {
	return filepath.Join(DataRoot(root), "state")
}

func EnvRoot() string {
	if data := os.Getenv("BOT_DATA"); data != "" {
		return data
	}
	return os.Getenv("BOT_ROOT")
}

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

func Exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func IsDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
