package xdg

import (
	"os"
	"path/filepath"
)

func homeDir() string {
	if h := os.Getenv("HOME"); h != "" {
		return h
	}
	return os.Getenv("USERPROFILE")
}

// ConfigHome returns $XDG_CONFIG_HOME or ~/.config
func ConfigHome() string {
	if v := os.Getenv("XDG_CONFIG_HOME"); v != "" {
		return v
	}
	return filepath.Join(homeDir(), ".config")
}

// DataHome returns $XDG_DATA_HOME or ~/.local/share
func DataHome() string {
	if v := os.Getenv("XDG_DATA_HOME"); v != "" {
		return v
	}
	return filepath.Join(homeDir(), ".local", "share")
}

func ConfigDir() string {
	return filepath.Join(ConfigHome(), "shitty-ci")
}

func ConfigPath() string {
	return filepath.Join(ConfigDir(), "config.yml")
}

func DataDir(custom string) string {
	if custom != "" {
		return filepath.Join(custom, "shitty-ci")
	}
	return filepath.Join(DataHome(), "shitty-ci")
}

func DBPath(dataDir string) string {
	return filepath.Join(dataDir, "shitty-ci.db")
}

func SocketPath(dataDir string) string {
	return filepath.Join(dataDir, "server.sock")
}

func LogsDir(dataDir string) string {
	return filepath.Join(dataDir, "logs")
}

func WorkspacesRoot(dataDir string) string {
	return filepath.Join(dataDir, "workspaces")
}

func AuthTokenPath(dataDir string) string {
	return filepath.Join(dataDir, "auth_token")
}
