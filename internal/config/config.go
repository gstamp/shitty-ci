package config

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"shitty-ci/internal/xdg"
)

// GitHubAppConfig holds credentials for a GitHub App, used to authenticate
// with the Checks API and Commit Statuses API via installation tokens.
type GitHubAppConfig struct {
	AppID          int64  `yaml:"app_id"`
	InstallationID int64  `yaml:"installation_id"`
	PrivateKeyPath string `yaml:"private_key_path"`
}

type Config struct {
	PollInterval        time.Duration `yaml:"poll_interval"`
	MaxConcurrentBuilds int           `yaml:"max_concurrent_builds"`
	BuildTimeout        time.Duration `yaml:"build_timeout"`
	GitHubToken         string        `yaml:"github_token"`
	GitHubApp           GitHubAppConfig `yaml:"github_app,omitempty"`
	DataDir             string        `yaml:"data_dir"`
	WorkspaceTTL        time.Duration `yaml:"workspace_ttl"`
	Listen              string        `yaml:"listen"`
}

func Default() Config {
	return Config{
		PollInterval:        30 * time.Second,
		MaxConcurrentBuilds: 4,
		BuildTimeout:        30 * time.Minute,
		WorkspaceTTL:        24 * time.Hour,
	}
}

func Load(path string) (Config, error) {
	c := Default()
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return c, nil
		}
		return Config{}, err
	}
	var aux struct {
		PollInterval        string          `yaml:"poll_interval"`
		MaxConcurrentBuilds int             `yaml:"max_concurrent_builds"`
		BuildTimeout        string          `yaml:"build_timeout"`
		GitHubToken         string          `yaml:"github_token"`
		GitHubApp           GitHubAppConfig `yaml:"github_app,omitempty"`
		DataDir             string          `yaml:"data_dir"`
		WorkspaceTTL        string          `yaml:"workspace_ttl"`
		Listen              string          `yaml:"listen"`
	}
	if err := yaml.Unmarshal(b, &aux); err != nil {
		return Config{}, err
	}
	if aux.MaxConcurrentBuilds > 0 {
		c.MaxConcurrentBuilds = aux.MaxConcurrentBuilds
	}
	if aux.GitHubToken != "" {
		c.GitHubToken = aux.GitHubToken
	}
	if aux.GitHubApp.AppID > 0 || aux.GitHubApp.InstallationID > 0 || aux.GitHubApp.PrivateKeyPath != "" {
		c.GitHubApp = aux.GitHubApp
	}
	if aux.DataDir != "" {
		c.DataDir = aux.DataDir
	}
	if aux.PollInterval != "" {
		d, err := time.ParseDuration(aux.PollInterval)
		if err != nil {
			return Config{}, err
		}
		c.PollInterval = d
	}
	if aux.BuildTimeout != "" {
		d, err := time.ParseDuration(aux.BuildTimeout)
		if err != nil {
			return Config{}, err
		}
		c.BuildTimeout = d
	}
	if aux.WorkspaceTTL != "" {
		d, err := time.ParseDuration(aux.WorkspaceTTL)
		if err != nil {
			return Config{}, err
		}
		c.WorkspaceTTL = d
	}
	if aux.Listen != "" {
		c.Listen = aux.Listen
	}
	return c, nil
}

// Store holds the latest config and its source path for display.
type Store struct {
	mu      sync.RWMutex
	path    string
	cfg     Config
	modTime time.Time
}

func NewStore(path string) *Store {
	return &Store{path: path}
}

func (s *Store) Path() string {
	return s.path
}

func (s *Store) Refresh() error {
	info, err := os.Stat(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			cfg := Default()
			s.mu.Lock()
			s.cfg = cfg
			s.modTime = time.Time{}
			s.mu.Unlock()
			return nil
		}
		return err
	}
	if !info.ModTime().After(s.modTime) && !s.modTime.IsZero() {
		return nil
	}
	cfg, err := Load(s.path)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.cfg = cfg
	s.modTime = info.ModTime()
	s.mu.Unlock()
	return nil
}

func (s *Store) Get() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

func (s *Store) DataDirResolved() string {
	cfg := s.Get()
	return xdg.DataDir(cfg.DataDir)
}

func EnsureConfigDir() error {
	return os.MkdirAll(xdg.ConfigDir(), 0o755)
}

func DefaultConfigPath() string {
	return xdg.ConfigPath()
}

func WriteExample(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	ex := []byte(`poll_interval: 30s
max_concurrent_builds: 4
build_timeout: 30m
github_token: ""

# Optional: GitHub App for Checks API (per-step check runs + commit statuses).
# When configured, takes precedence over github_token for all GitHub API calls.
# Learn more: https://docs.github.com/en/apps/creating-a-github-app
# github_app:
#   app_id: 123456
#   installation_id: 789012
#   private_key_path: /home/user/.config/shitty-ci/github-app.pem

# Optional:
# data_dir: /custom/path
# workspace_ttl: 24h
# listen: "127.0.0.1:9876"   # TCP address for remote CLI access
`)
	return os.WriteFile(path, ex, 0o644)
}
