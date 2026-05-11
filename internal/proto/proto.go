package proto

import "shitty-ci/internal/types"

type Request struct {
	Cmd     string `json:"cmd"`
	Repo    string `json:"repo,omitempty"`
	Limit   int    `json:"limit,omitempty"`
	BuildID string `json:"build_id,omitempty"`
	// SecretKey / SecretValue are used by secret_* RPCs (per-repo secrets).
	SecretKey   string `json:"secret_key,omitempty"`
	SecretValue string `json:"secret_value,omitempty"`
}

type Response struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	Data  any    `json:"data,omitempty"`
}

type ConfigView struct {
	Path            string `json:"path"`
	PollInterval    string `json:"poll_interval"`
	MaxConcurrent   int    `json:"max_concurrent_builds"`
	BuildTimeout    string `json:"build_timeout"`
	HasGitHubToken  bool   `json:"has_github_token"`
	DataDirOverride string `json:"data_dir_override,omitempty"`
	WorkspaceTTL    string `json:"workspace_ttl"`
	ResolvedDataDir string `json:"resolved_data_dir"`
}

type LogsData struct {
	Path string `json:"path"`
	Text string `json:"text"`
}

type CancelData struct {
	BuildID string `json:"build_id"`
	State   string `json:"state"`
}

func OK(data any) Response {
	return Response{OK: true, Data: data}
}

func Err(msg string) Response {
	return Response{OK: false, Error: msg}
}

type BuildsData struct {
	Builds []types.Build `json:"builds"`
}

type ReposData struct {
	Repos []types.Repo `json:"repos"`
}

// SecretKeysData lists secret names for a repo (never values).
type SecretKeysData struct {
	Keys []string `json:"keys"`
}
