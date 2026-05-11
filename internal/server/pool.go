package server

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"shitty-ci/internal/db"
	"shitty-ci/internal/gitutil"
	"shitty-ci/internal/xdg"
)

func sanitizeRepoDir(owner, name string) string {
	safe := func(s string) string { return strings.ReplaceAll(s, "/", "_") }
	return safe(owner) + "_" + safe(name)
}

func (a *App) workspaceRoot() string {
	return xdg.WorkspacesRoot(a.dataDir)
}

func (a *App) nextWorkspaceDirName(ctx context.Context, repoID int64, owner, name string) (string, error) {
	base := sanitizeRepoDir(owner, name)
	rows, err := db.ListWorkspaces(ctx, a.db, repoID)
	if err != nil {
		return "", err
	}
	hasBase := false
	maxN := 0
	for _, w := range rows {
		if w.DirName == base {
			hasBase = true
			continue
		}
		prefix := base + "-"
		if strings.HasPrefix(w.DirName, prefix) {
			var n int
			_, _ = fmt.Sscanf(w.DirName[len(prefix):], "%d", &n)
			if n > maxN {
				maxN = n
			}
		}
	}
	if !hasBase {
		return base, nil
	}
	return fmt.Sprintf("%s-%d", base, maxN+1), nil
}

func (a *App) acquireWorkspace(ctx context.Context, repoID int64, owner, name string) (wsPath string, wsRowID int64, err error) {
	now := time.Now().Unix()
	rows, err := db.ListWorkspaces(ctx, a.db, repoID)
	if err != nil {
		return "", 0, err
	}
	for _, w := range rows {
		if w.Status != "idle" {
			continue
		}
		path := filepath.Join(a.workspaceRoot(), w.DirName)
		if _, statErr := os.Stat(filepath.Join(path, ".git")); statErr != nil {
			continue
		}
		if err := db.SetWorkspaceStatus(ctx, a.db, w.ID, "busy", now); err != nil {
			return "", 0, err
		}
		return path, w.ID, nil
	}
	dirName, err := a.nextWorkspaceDirName(ctx, repoID, owner, name)
	if err != nil {
		return "", 0, err
	}
	if err := os.MkdirAll(a.workspaceRoot(), 0o755); err != nil {
		return "", 0, err
	}
	path := filepath.Join(a.workspaceRoot(), dirName)
	remote := fmt.Sprintf("git@github.com:%s/%s.git", owner, name)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		if out, gitErr := gitutil.RunGitOutput("", "clone", remote, path); gitErr != nil {
			return "", 0, fmt.Errorf("git clone: %w\n%s", gitErr, string(out))
		}
	}
	id, err := db.InsertWorkspace(ctx, a.db, repoID, dirName, "busy", now)
	if err != nil {
		return "", 0, err
	}
	return path, id, nil
}

func (a *App) releaseWorkspace(ctx context.Context, wsRowID int64) error {
	return db.SetWorkspaceStatus(ctx, a.db, wsRowID, "idle", time.Now().Unix())
}

func (a *App) pruneWorkspaces(ctx context.Context, ttl time.Duration) error {
	if ttl <= 0 {
		return nil
	}
	cut := time.Now().Add(-ttl).Unix()
	rows, err := a.db.QueryContext(ctx, `SELECT w.id, w.repo_id, w.dir_name, w.status, w.last_used, r.owner, r.name
		FROM workspaces w JOIN repos r ON r.id=w.repo_id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, repoID int64
		var dir, status string
		var last int64
		var owner, name string
		if err := rows.Scan(&id, &repoID, &dir, &status, &last, &owner, &name); err != nil {
			return err
		}
		if status != "idle" || last > cut {
			continue
		}
		path := filepath.Join(a.workspaceRoot(), dir)
		_ = os.RemoveAll(path)
		if err := db.DeleteWorkspaceRow(ctx, a.db, id); err != nil {
			return err
		}
	}
	return rows.Err()
}

func ensureDataDirs(dataDir string) error {
	if err := os.MkdirAll(xdg.LogsDir(dataDir), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(xdg.WorkspacesRoot(dataDir), 0o755); err != nil {
		return err
	}
	return nil
}

func removeSocket(path string) {
	_ = os.Remove(path)
}
