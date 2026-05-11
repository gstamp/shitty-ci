package gitutil

import (
	"path/filepath"
	"strings"
)

// DetectGitHubRepoFromDir returns owner/name when dir is inside a git work tree
// whose origin remote points at github.com. dir may be empty to use the process cwd.
func DetectGitHubRepoFromDir(dir string) (owner, name string, ok bool) {
	if dir == "" {
		var err error
		dir, err = filepath.Abs(".")
		if err != nil {
			return "", "", false
		}
	}
	s, err := RunGit(dir, "rev-parse", "--is-inside-work-tree")
	if err != nil || s != "true" {
		return "", "", false
	}
	remote, err := RunGit(dir, "remote", "get-url", "origin")
	if err != nil || remote == "" {
		return "", "", false
	}
	return ParseGitHubRemote(remote)
}

// ParseGitHubRemote parses owner/repo from common GitHub clone URLs.
func ParseGitHubRemote(remote string) (owner, name string, ok bool) {
	remote = strings.TrimSpace(remote)
	remote = strings.TrimSuffix(remote, "/")
	remote = strings.TrimSuffix(remote, ".git")

	switch {
	case strings.HasPrefix(remote, "git@github.com:"):
		rest := strings.TrimPrefix(remote, "git@github.com:")
		i := strings.IndexByte(rest, '/')
		if i <= 0 || i == len(rest)-1 {
			return "", "", false
		}
		return rest[:i], rest[i+1:], true

	case strings.HasPrefix(remote, "ssh://git@github.com/"):
		rest := strings.TrimPrefix(remote, "ssh://git@github.com/")
		i := strings.IndexByte(rest, '/')
		if i <= 0 || i == len(rest)-1 {
			return "", "", false
		}
		return rest[:i], rest[i+1:], true

	default:
		const pfx = "https://github.com/"
		if !strings.HasPrefix(strings.ToLower(remote), pfx) {
			return "", "", false
		}
		// Preserve case for owner/repo segments.
		idx := strings.Index(strings.ToLower(remote), pfx)
		rest := remote[idx+len(pfx):]
		parts := strings.Split(rest, "/")
		if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
			return "", "", false
		}
		return parts[0], parts[1], true
	}
}
