package db

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveLogsTargetBySHA(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	conn, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	const full = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	sha := strings.Repeat("d", 40)
	_, err = conn.ExecContext(ctx, `INSERT INTO repos(owner, name, created_at) VALUES('o','n',1)`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = conn.ExecContext(ctx, `INSERT INTO builds(id, repo_id, sha, ref, state, log_path, created_at) VALUES(?,1,?,'refs/remotes/origin/main','success','/tmp/x',1)`, full, sha)
	if err != nil {
		t.Fatal(err)
	}

	got, err := ResolveLogsTarget(ctx, conn, "dddd")
	if err != nil || got != full {
		t.Fatalf("sha prefix: got %q err %v", got, err)
	}

	got, err = ResolveLogsTarget(ctx, conn, sha)
	if err != nil || got != full {
		t.Fatalf("full sha: got %q err %v", got, err)
	}

	_, err = ResolveLogsTarget(ctx, conn, "ffffffff")
	if !errors.Is(err, ErrUnknownBuild) {
		t.Fatalf("missing sha: %v", err)
	}
}

func TestResolveCancelTargetByRunningSHA(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	conn, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	const id = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	sha := strings.Repeat("c", 40)
	_, err = conn.ExecContext(ctx, `INSERT INTO repos(owner, name, created_at) VALUES('o','n',1)`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = conn.ExecContext(ctx, `INSERT INTO builds(id, repo_id, sha, ref, state, log_path, created_at) VALUES(?,1,?,'refs/remotes/origin/main','running','/tmp/x',1)`, id, sha)
	if err != nil {
		t.Fatal(err)
	}

	got, err := ResolveCancelTarget(ctx, conn, "cccc")
	if err != nil || got != id {
		t.Fatalf("running sha prefix: got %q err %v", got, err)
	}

	_, err = conn.ExecContext(ctx, `UPDATE builds SET state='success' WHERE id=?`, id)
	if err != nil {
		t.Fatal(err)
	}
	_, err = ResolveCancelTarget(ctx, conn, "cccc")
	if !errors.Is(err, ErrUnknownBuild) {
		t.Fatalf("non-running sha: %v", err)
	}
}
