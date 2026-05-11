package db

import (
	"context"
	"path/filepath"
	"testing"
)

func TestRepoSecrets_roundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")
	conn, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	ctx := context.Background()
	res, err := conn.ExecContext(ctx, `INSERT INTO repos(owner, name, created_at) VALUES('o','n',123)`)
	if err != nil {
		t.Fatal(err)
	}
	repoID, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}

	if err := UpsertRepoSecret(ctx, conn, repoID, "API_TOKEN", "hunter2"); err != nil {
		t.Fatal(err)
	}
	m, err := GetRepoSecrets(ctx, conn, repoID)
	if err != nil {
		t.Fatal(err)
	}
	if got := m["API_TOKEN"]; got != "hunter2" {
		t.Fatalf("token: got %q", got)
	}
	keys, err := ListRepoSecretKeys(ctx, conn, repoID)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || keys[0] != "API_TOKEN" {
		t.Fatalf("keys: %#v", keys)
	}
	if err := DeleteRepoSecret(ctx, conn, repoID, "API_TOKEN"); err != nil {
		t.Fatal(err)
	}
	m2, err := GetRepoSecrets(ctx, conn, repoID)
	if err != nil {
		t.Fatal(err)
	}
	if len(m2) != 0 {
		t.Fatalf("expected empty map, got %#v", m2)
	}
}

func TestValidateSecretKey(t *testing.T) {
	t.Parallel()
	if err := ValidateSecretKey(""); err == nil {
		t.Fatal("expected error")
	}
	if err := ValidateSecretKey("0BAD"); err == nil {
		t.Fatal("expected error")
	}
	if err := ValidateSecretKey("BAD-KEY"); err == nil {
		t.Fatal("expected error")
	}
	if err := ValidateSecretKey("API_TOKEN"); err != nil {
		t.Fatal(err)
	}
}
