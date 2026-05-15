package db

import (
	"context"
	"path/filepath"
	"testing"

	"shitty-ci/internal/types"
)

func TestRecoverStaleBuilds(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	conn, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	_, err = conn.ExecContext(ctx, `INSERT INTO repos(owner, name, created_at) VALUES('o','n',1)`)
	if err != nil {
		t.Fatal(err)
	}

	const logPath = "/tmp/x"
	_, err = conn.ExecContext(ctx, `INSERT INTO builds(id, repo_id, sha, ref, state, step_name, description, created_at, started_at, finished_at, log_path)
		VALUES('aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',1,'deadbeef','refs/remotes/origin/main','running','compile','',1,2,0,?)`, logPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = conn.ExecContext(ctx, `INSERT INTO builds(id, repo_id, sha, ref, state, step_name, description, created_at, started_at, finished_at, log_path)
		VALUES('bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',1,'cafe','refs/remotes/origin/main','pending','','',3,0,0,?)`, logPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = conn.ExecContext(ctx, `INSERT INTO builds(id, repo_id, sha, ref, state, log_path, created_at, started_at, finished_at)
		VALUES('cccccccccccccccccccccccccccccccc',1,'done','refs/remotes/origin/main','success','/tmp/y',4,4,4)`)
	if err != nil {
		t.Fatal(err)
	}

	got, err := ListUnfinishedBuilds(ctx, conn)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("unfinished: got %d rows, want 2 (%+v)", len(got), got)
	}

	now := int64(99)
	if err := MarkBuildInterrupted(ctx, conn, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "daemon restarted during step \"compile\"", now); err != nil {
		t.Fatal(err)
	}
	if err := MarkBuildInterrupted(ctx, conn, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "daemon restarted while this build was in progress", now); err != nil {
		t.Fatal(err)
	}

	var states []string
	rows, err := conn.QueryContext(ctx, `SELECT id, state, finished_at FROM builds ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, state string
		var finished int64
		if err := rows.Scan(&id, &state, &finished); err != nil {
			t.Fatal(err)
		}
		switch id {
		case "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa":
			if state != string(types.BuildInterrupted) || finished != now {
				t.Fatalf("running row: state=%q finished=%d", state, finished)
			}
		case "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb":
			if state != string(types.BuildInterrupted) || finished != now {
				t.Fatalf("pending row: state=%q finished=%d", state, finished)
			}
		case "cccccccccccccccccccccccccccccccc":
			if state != "success" || finished != 4 {
				t.Fatalf("finished row mutated: state=%q finished=%d", state, finished)
			}
		default:
			t.Fatalf("unexpected id %q", id)
		}
		states = append(states, state)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(states) != 3 {
		t.Fatalf("row count: got %d want 3", len(states))
	}

	got2, err := ListUnfinishedBuilds(ctx, conn)
	if err != nil {
		t.Fatal(err)
	}
	if len(got2) != 0 {
		t.Fatalf("after recovery: got %d unfinished, want 0", len(got2))
	}

	// Idempotent second mark should not error and should not change success row.
	if err := MarkBuildInterrupted(ctx, conn, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "nope", 123); err != nil {
		t.Fatal(err)
	}
	var st string
	err = conn.QueryRowContext(ctx, `SELECT state FROM builds WHERE id='aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'`).Scan(&st)
	if err != nil {
		t.Fatal(err)
	}
	if st != string(types.BuildInterrupted) {
		t.Fatalf("re-mark changed state: %q", st)
	}
}
