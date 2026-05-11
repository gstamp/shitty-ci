package db

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestResolveBuildID(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	conn, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	const full = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	_, err = conn.ExecContext(ctx, `INSERT INTO repos(owner, name, created_at) VALUES('o','n',1)`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = conn.ExecContext(ctx, `INSERT INTO builds(id, repo_id, sha, ref, state, log_path, created_at) VALUES(?,1,'deadbeef','refs/remotes/origin/main','success','/tmp/x',1)`, full)
	if err != nil {
		t.Fatal(err)
	}

	got, err := ResolveBuildID(ctx, conn, full)
	if err != nil || got != full {
		t.Fatalf("exact: got %q err %v", got, err)
	}

	got, err = ResolveBuildID(ctx, conn, "aaaaaaaa")
	if err != nil || got != full {
		t.Fatalf("prefix: got %q err %v", got, err)
	}

	_, err = ResolveBuildID(ctx, conn, "aa")
	if err == nil || !errors.Is(err, ErrUnknownBuild) {
		t.Fatalf("short prefix: %v", err)
	}

	other := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa1"
	_, err = conn.ExecContext(ctx, `INSERT INTO builds(id, repo_id, sha, ref, state, log_path, created_at) VALUES(?,1,'cafe','refs/remotes/origin/main','success','/tmp/y',2)`, other)
	if err != nil {
		t.Fatal(err)
	}
	_, err = ResolveBuildID(ctx, conn, "aaaaaaaa")
	if err == nil || !errors.Is(err, ErrAmbiguousBuildID) {
		t.Fatalf("ambiguous prefix: %v", err)
	}
}
