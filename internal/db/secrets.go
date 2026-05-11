package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// UpsertRepoSecret stores or updates a per-repo secret value.
func UpsertRepoSecret(ctx context.Context, dbConn *sql.DB, repoID int64, key, value string) error {
	now := time.Now().Unix()
	_, err := dbConn.ExecContext(ctx, `INSERT INTO repo_secrets(repo_id, key, value, updated_at) VALUES(?,?,?,?)
		ON CONFLICT(repo_id, key) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at`,
		repoID, key, value, now)
	return err
}

// DeleteRepoSecret removes a secret key for a repo. It is not an error if the key did not exist.
func DeleteRepoSecret(ctx context.Context, dbConn *sql.DB, repoID int64, key string) error {
	_, err := dbConn.ExecContext(ctx, `DELETE FROM repo_secrets WHERE repo_id=? AND key=?`, repoID, key)
	return err
}

// ListRepoSecretKeys returns sorted secret names for a repo (never values).
func ListRepoSecretKeys(ctx context.Context, dbConn *sql.DB, repoID int64) ([]string, error) {
	rows, err := dbConn.QueryContext(ctx, `SELECT key FROM repo_secrets WHERE repo_id=? ORDER BY key`, repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// GetRepoSecrets returns all secret key/value pairs for a repo.
func GetRepoSecrets(ctx context.Context, dbConn *sql.DB, repoID int64) (map[string]string, error) {
	rows, err := dbConn.QueryContext(ctx, `SELECT key, value FROM repo_secrets WHERE repo_id=?`, repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]string)
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// ValidateSecretKey enforces a conservative env-var-like key shape.
func ValidateSecretKey(key string) error {
	if key == "" {
		return fmt.Errorf("secret key is empty")
	}
	if len(key) > 256 {
		return fmt.Errorf("secret key is too long")
	}
	for i, r := range key {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '_':
		default:
			return fmt.Errorf("invalid secret key %q (use letters, digits, underscore)", key)
		}
		if i == 0 && r >= '0' && r <= '9' {
			return fmt.Errorf("invalid secret key %q (must not start with a digit)", key)
		}
	}
	return nil
}
