package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"shitty-ci/internal/gitutil"
	"shitty-ci/internal/proto"
)

// TrackedRepoFull resolves owner/repo for commands that scope to a single tracked repository.
// If repoFlag is non-empty, it must be tracked. If empty, uses the GitHub origin detected from cwd
// (when that repo is tracked).
func TrackedRepoFull(repoFlag string) (string, error) {
	repoFlag = strings.TrimSpace(repoFlag)
	if repoFlag != "" {
		return mustBeTracked(repoFlag)
	}
	owner, name, ok := gitutil.DetectGitHubRepoFromDir("")
	if !ok {
		return "", fmt.Errorf("could not detect a github.com origin remote in the current directory; pass --repo owner/repo")
	}
	return mustBeTracked(owner + "/" + name)
}

func mustBeTracked(candidate string) (string, error) {
	lr, err := RPC(proto.Request{Cmd: "repos_list"})
	if err != nil {
		return "", err
	}
	if !lr.OK {
		return "", fmt.Errorf("%s", strings.TrimSpace(lr.Error))
	}
	raw, err := json.Marshal(lr.Data)
	if err != nil {
		return "", err
	}
	var rd proto.ReposData
	if err := json.Unmarshal(raw, &rd); err != nil {
		return "", err
	}
	for _, r := range rd.Repos {
		if r.Full == candidate {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("repo %s is not tracked by shitty-ci; add it with `shitty-ci repos add`", candidate)
}
