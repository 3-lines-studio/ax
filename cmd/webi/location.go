package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

func resolveRoot(explicit string) (root string, fresh bool, err error) {
	switch {
	case explicit != "":
		root = expandHome(explicit)
	case os.Getenv("BOT_ROOT") != "":
		root = os.Getenv("BOT_ROOT")
	case os.Getenv("BOT_DATA") != "":
		root = os.Getenv("BOT_DATA")
	default:
		root, err = appDataRoot()
		if err != nil {
			return "", false, err
		}
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return "", false, err
	}
	info, err := os.Stat(root)
	if err == nil {
		if !info.IsDir() {
			return "", false, fmt.Errorf("%s is not a directory", root)
		}
		return root, false, nil
	}
	if !os.IsNotExist(err) {
		return "", false, err
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", false, err
	}
	return root, true, nil
}

func appDataRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return appDataRootFor(runtime.GOOS, home, os.Getenv("XDG_DATA_HOME"), os.Getenv("LOCALAPPDATA")), nil
}

func appDataRootFor(goos, home, xdgData, localAppData string) string {
	switch goos {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "ax")
	case "windows":
		if localAppData != "" {
			return filepath.Join(localAppData, "ax")
		}
		return filepath.Join(home, "AppData", "Local", "ax")
	default:
		if xdgData != "" {
			return filepath.Join(xdgData, "ax")
		}
		return filepath.Join(home, ".local", "share", "ax")
	}
}

func expandHome(path string) string {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	if path == "~" {
		return home
	}
	return filepath.Join(home, path[2:])
}
