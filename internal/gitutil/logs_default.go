package gitutil

import (
	"fmt"
	"path/filepath"
	"strings"
)

// LogsDefaultSelection describes how to pick the default build for `shitty-ci logs`
// when no explicit build id / SHA is provided.
type LogsDefaultSelection struct {
	HeadSHA       string
	RefCandidates []string
	Detached      bool
}

// DetectLogsDefaultFromWorktree inspects the git worktree at dir (empty means cwd)
// and returns HEAD plus ref names that should match rows stored by the daemon
// (typically refs/remotes/<remote>/<branch>).
func DetectLogsDefaultFromWorktree(dir string) (sel LogsDefaultSelection, err error) {
	if dir == "" {
		dir = "."
	}
	wd, err := filepath.Abs(dir)
	if err != nil {
		return sel, err
	}

	in, err := RunGit(wd, "rev-parse", "--is-inside-work-tree")
	if err != nil || strings.TrimSpace(in) != "true" {
		return sel, fmt.Errorf("not a git repository")
	}

	head, err := RunGit(wd, "rev-parse", "HEAD")
	if err != nil {
		return sel, err
	}
	sel.HeadSHA = strings.TrimSpace(head)
	if sel.HeadSHA == "" {
		return sel, fmt.Errorf("could not resolve HEAD")
	}

	abbr, err := RunGit(wd, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return sel, err
	}
	abbr = strings.TrimSpace(abbr)
	if abbr == "" || strings.EqualFold(abbr, "HEAD") {
		sel.Detached = true
		return sel, nil
	}

	seen := map[string]struct{}{}
	add := func(ref string) {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			return
		}
		if _, ok := seen[ref]; ok {
			return
		}
		seen[ref] = struct{}{}
		sel.RefCandidates = append(sel.RefCandidates, ref)
	}

	// Prefer the configured upstream (e.g. origin/main -> refs/remotes/origin/main).
	if up, err := RunGit(wd, "rev-parse", "--abbrev-ref", "@{upstream}"); err == nil {
		up = strings.TrimSpace(up)
		if up != "" && !strings.EqualFold(up, "HEAD") {
			add("refs/remotes/" + up)
		}
	}

	// Common fallback when upstream isn't set (or points somewhere unexpected).
	add("refs/remotes/origin/" + abbr)

	return sel, nil
}
