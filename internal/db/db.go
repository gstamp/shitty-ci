package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"

	"shitty-ci/internal/types"
)

func Open(path string) (*sql.DB, error) {
	d, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)")
	if err != nil {
		return nil, err
	}
	if err := migrate(d); err != nil {
		_ = d.Close()
		return nil, err
	}
	return d, nil
}

func migrate(db *sql.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS repos (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			owner TEXT NOT NULL,
			name TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			UNIQUE(owner, name)
		);`,
		`CREATE TABLE IF NOT EXISTS repo_refs (
			repo_id INTEGER NOT NULL,
			ref TEXT NOT NULL,
			last_sha TEXT NOT NULL,
			PRIMARY KEY (repo_id, ref),
			FOREIGN KEY (repo_id) REFERENCES repos(id) ON DELETE CASCADE
		);`,
		`CREATE TABLE IF NOT EXISTS workspaces (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			repo_id INTEGER NOT NULL,
			dir_name TEXT NOT NULL,
			status TEXT NOT NULL,
			last_used INTEGER NOT NULL,
			UNIQUE(repo_id, dir_name),
			FOREIGN KEY (repo_id) REFERENCES repos(id) ON DELETE CASCADE
		);`,
		`CREATE TABLE IF NOT EXISTS builds (
			id TEXT PRIMARY KEY,
			repo_id INTEGER NOT NULL,
			sha TEXT NOT NULL,
			ref TEXT NOT NULL,
			state TEXT NOT NULL,
			step_name TEXT NOT NULL DEFAULT '',
			description TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL,
			started_at INTEGER NOT NULL DEFAULT 0,
			finished_at INTEGER NOT NULL DEFAULT 0,
			log_path TEXT NOT NULL,
			FOREIGN KEY (repo_id) REFERENCES repos(id) ON DELETE CASCADE
		);`,
		`CREATE TABLE IF NOT EXISTS repo_secrets (
			repo_id INTEGER NOT NULL,
			key TEXT NOT NULL,
			value TEXT NOT NULL,
			updated_at INTEGER NOT NULL,
			PRIMARY KEY (repo_id, key),
			FOREIGN KEY (repo_id) REFERENCES repos(id) ON DELETE CASCADE
		);`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			return err
		}
	}
	return nil
}

func AddRepo(ctx context.Context, dbConn *sql.DB, owner, name string) (int64, error) {
	now := time.Now().Unix()
	res, err := dbConn.ExecContext(ctx, `INSERT INTO repos(owner, name, created_at) VALUES(?,?,?)`, owner, name, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func RemoveRepo(ctx context.Context, dbConn *sql.DB, owner, name string) error {
	_, err := dbConn.ExecContext(ctx, `DELETE FROM repos WHERE owner=? AND name=?`, owner, name)
	return err
}

func ListRepos(ctx context.Context, dbConn *sql.DB) ([]types.Repo, error) {
	rows, err := dbConn.QueryContext(ctx, `SELECT id, owner, name FROM repos ORDER BY owner, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []types.Repo
	for rows.Next() {
		var r types.Repo
		if err := rows.Scan(&r.ID, &r.Owner, &r.Name); err != nil {
			return nil, err
		}
		r.Full = r.Owner + "/" + r.Name
		out = append(out, r)
	}
	return out, rows.Err()
}

func GetRepo(ctx context.Context, dbConn *sql.DB, owner, name string) (types.Repo, error) {
	var r types.Repo
	err := dbConn.QueryRowContext(ctx, `SELECT id, owner, name FROM repos WHERE owner=? AND name=?`, owner, name).
		Scan(&r.ID, &r.Owner, &r.Name)
	if err != nil {
		return r, err
	}
	r.Full = r.Owner + "/" + r.Name
	return r, nil
}

func SetRefSHA(ctx context.Context, dbConn *sql.DB, repoID int64, ref, sha string) error {
	_, err := dbConn.ExecContext(ctx, `INSERT INTO repo_refs(repo_id, ref, last_sha) VALUES(?,?,?)
		ON CONFLICT(repo_id, ref) DO UPDATE SET last_sha=excluded.last_sha`, repoID, ref, sha)
	return err
}

func GetRefSHA(ctx context.Context, dbConn *sql.DB, repoID int64, ref string) (string, error) {
	var sha string
	err := dbConn.QueryRowContext(ctx, `SELECT last_sha FROM repo_refs WHERE repo_id=? AND ref=?`, repoID, ref).Scan(&sha)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return sha, err
}

func InsertWorkspace(ctx context.Context, dbConn *sql.DB, repoID int64, dirName string, status string, lastUsed int64) (int64, error) {
	res, err := dbConn.ExecContext(ctx, `INSERT INTO workspaces(repo_id, dir_name, status, last_used) VALUES(?,?,?,?)`, repoID, dirName, status, lastUsed)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func ListWorkspaces(ctx context.Context, dbConn *sql.DB, repoID int64) ([]struct {
	ID      int64
	DirName string
	Status  string
	Last    int64
}, error) {
	rows, err := dbConn.QueryContext(ctx, `SELECT id, dir_name, status, last_used FROM workspaces WHERE repo_id=?`, repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []struct {
		ID      int64
		DirName string
		Status  string
		Last    int64
	}
	for rows.Next() {
		var w struct {
			ID      int64
			DirName string
			Status  string
			Last    int64
		}
		if err := rows.Scan(&w.ID, &w.DirName, &w.Status, &w.Last); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

func SetWorkspaceStatus(ctx context.Context, dbConn *sql.DB, id int64, status string, lastUsed int64) error {
	_, err := dbConn.ExecContext(ctx, `UPDATE workspaces SET status=?, last_used=? WHERE id=?`, status, lastUsed, id)
	return err
}

func DeleteWorkspaceRow(ctx context.Context, dbConn *sql.DB, id int64) error {
	_, err := dbConn.ExecContext(ctx, `DELETE FROM workspaces WHERE id=?`, id)
	return err
}

func CreateBuild(ctx context.Context, dbConn *sql.DB, id string, repoID int64, sha, ref string, state types.BuildState, logPath string) error {
	now := time.Now().Unix()
	_, err := dbConn.ExecContext(ctx, `INSERT INTO builds(id, repo_id, sha, ref, state, step_name, description, created_at, log_path) VALUES(?,?,?,?,?,?,?,?,?)`,
		id, repoID, sha, ref, string(state), "", "", now, logPath)
	return err
}

func UpdateBuildState(ctx context.Context, dbConn *sql.DB, id string, state types.BuildState, step, desc string, started, finished int64) error {
	if started == 0 {
		_, err := dbConn.ExecContext(ctx, `UPDATE builds SET state=?, step_name=?, description=? WHERE id=?`, string(state), step, desc, id)
		return err
	}
	if finished == 0 {
		_, err := dbConn.ExecContext(ctx, `UPDATE builds SET state=?, step_name=?, description=?, started_at=? WHERE id=?`, string(state), step, desc, started, id)
		return err
	}
	_, err := dbConn.ExecContext(ctx, `UPDATE builds SET state=?, step_name=?, description=?, started_at=?, finished_at=? WHERE id=?`, string(state), step, desc, started, finished, id)
	return err
}

func GetBuild(ctx context.Context, dbConn *sql.DB, id string) (types.Build, error) {
	var b types.Build
	var repoOwner, repoName string
	err := dbConn.QueryRowContext(ctx, `SELECT b.id, r.owner, r.name, b.sha, b.ref, b.state, b.step_name, b.description, b.created_at, b.started_at, b.finished_at
		FROM builds b JOIN repos r ON r.id=b.repo_id WHERE b.id=?`, id).
		Scan(&b.ID, &repoOwner, &repoName, &b.SHA, &b.Ref, &b.State, &b.Step, &b.Description, &b.CreatedAt, &b.StartedAt, &b.FinishedAt)
	if err != nil {
		return b, err
	}
	b.Repo = repoOwner + "/" + repoName
	return b, nil
}

// LastBuildForRepo returns the most recent build for repoID by created_at, or sql.ErrNoRows.
func LastBuildForRepo(ctx context.Context, dbConn *sql.DB, repoID int64) (types.Build, error) {
	var b types.Build
	var owner, name string
	err := dbConn.QueryRowContext(ctx, `SELECT b.id, r.owner, r.name, b.sha, b.ref, b.state, b.step_name, b.description, b.created_at, b.started_at, b.finished_at
		FROM builds b JOIN repos r ON r.id=b.repo_id WHERE b.repo_id=? ORDER BY b.created_at DESC LIMIT 1`, repoID).
		Scan(&b.ID, &owner, &name, &b.SHA, &b.Ref, &b.State, &b.Step, &b.Description, &b.CreatedAt, &b.StartedAt, &b.FinishedAt)
	if err != nil {
		return b, err
	}
	b.Repo = owner + "/" + name
	return b, nil
}

func ListBuilds(ctx context.Context, dbConn *sql.DB, repoFull string, limit int) ([]types.Build, error) {
	if limit <= 0 {
		limit = 50
	}
	var rows *sql.Rows
	var err error
	if repoFull == "" {
		rows, err = dbConn.QueryContext(ctx, `SELECT b.id, r.owner, r.name, b.sha, b.ref, b.state, b.step_name, b.description, b.created_at, b.started_at, b.finished_at
			FROM builds b JOIN repos r ON r.id=b.repo_id ORDER BY b.created_at DESC LIMIT ?`, limit)
	} else {
		parts := splitRepo(repoFull)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid repo %q", repoFull)
		}
		rows, err = dbConn.QueryContext(ctx, `SELECT b.id, r.owner, r.name, b.sha, b.ref, b.state, b.step_name, b.description, b.created_at, b.started_at, b.finished_at
			FROM builds b JOIN repos r ON r.id=b.repo_id WHERE r.owner=? AND r.name=? ORDER BY b.created_at DESC LIMIT ?`, parts[0], parts[1], limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []types.Build
	for rows.Next() {
		var b types.Build
		var owner, name string
		if err := rows.Scan(&b.ID, &owner, &name, &b.SHA, &b.Ref, &b.State, &b.Step, &b.Description, &b.CreatedAt, &b.StartedAt, &b.FinishedAt); err != nil {
			return nil, err
		}
		b.Repo = owner + "/" + name
		out = append(out, b)
	}
	return out, rows.Err()
}

func splitRepo(s string) []string {
	for i := 0; i < len(s); i++ {
		if s[i] == '/' {
			return []string{s[:i], s[i+1:]}
		}
	}
	return nil
}

func BuildLogPath(ctx context.Context, dbConn *sql.DB, id string) (string, error) {
	var p string
	err := dbConn.QueryRowContext(ctx, `SELECT log_path FROM builds WHERE id=?`, id).Scan(&p)
	return p, err
}
