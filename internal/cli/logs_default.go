package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"shitty-ci/internal/gitutil"
	"shitty-ci/internal/proto"
)

// DefaultLogsBuildID picks the most recent build for the GitHub repo in the current
// directory, scoped to the current branch (or detached HEAD commit).
func DefaultLogsBuildID() (string, error) {
	owner, name, ok := gitutil.DetectGitHubRepoFromDir("")
	if !ok {
		return "", fmt.Errorf("could not detect a github.com origin remote in the current directory; pass a build id prefix or commit sha")
	}
	repoFull := owner + "/" + name

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
	tracked := false
	for _, r := range rd.Repos {
		if r.Full == repoFull {
			tracked = true
			break
		}
	}
	if !tracked {
		return "", fmt.Errorf("repo %s is not tracked by shitty-ci; pass a build id prefix or commit sha", repoFull)
	}

	sel, err := gitutil.DetectLogsDefaultFromWorktree("")
	if err != nil {
		return "", err
	}

	br, err := RPC(proto.Request{Cmd: "builds_list", Repo: repoFull, Limit: 200})
	if err != nil {
		return "", err
	}
	if !br.OK {
		return "", fmt.Errorf("%s", strings.TrimSpace(br.Error))
	}
	raw, err = json.Marshal(br.Data)
	if err != nil {
		return "", err
	}
	var bd proto.BuildsData
	if err := json.Unmarshal(raw, &bd); err != nil {
		return "", err
	}

	for _, b := range bd.Builds {
		if sel.Detached {
			if strings.EqualFold(strings.TrimSpace(b.SHA), sel.HeadSHA) {
				return b.ID, nil
			}
			continue
		}
		for _, ref := range sel.RefCandidates {
			if b.Ref == ref {
				return b.ID, nil
			}
		}
	}

	if sel.Detached {
		short := sel.HeadSHA
		if len(short) > 7 {
			short = short[:7]
		}
		return "", fmt.Errorf("no build found for %s at commit %s; pass a build id prefix or commit sha", repoFull, short)
	}
	return "", fmt.Errorf("no build found for %s on the current branch; pass a build id prefix or commit sha", repoFull)
}
