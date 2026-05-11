package db

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

const minHexQueryLen = 4

// ResolveLogsTarget resolves a logs argument to a full build id.
// It prefers build id (exact or unique prefix), then falls back to the latest build matching a commit SHA (exact or prefix).
func ResolveLogsTarget(ctx context.Context, dbConn *sql.DB, spec string) (string, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return "", ErrMissingBuildID
	}

	id, err := ResolveBuildID(ctx, dbConn, spec)
	if err == nil {
		return id, nil
	}
	if errors.Is(err, ErrAmbiguousBuildID) {
		return "", err
	}
	if errors.Is(err, ErrMissingBuildID) {
		return "", err
	}
	if err != nil && !errors.Is(err, ErrUnknownBuild) {
		return "", err
	}

	if !isHexString(spec) || len(spec) < minHexQueryLen {
		return "", ErrUnknownBuild
	}
	return resolveLatestBuildByCommitSHA(ctx, dbConn, strings.ToLower(spec))
}

// ResolveCancelTarget resolves a cancel argument to a full build id for a running build.
func ResolveCancelTarget(ctx context.Context, dbConn *sql.DB, spec string) (string, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return "", ErrMissingBuildID
	}

	id, err := ResolveBuildID(ctx, dbConn, spec)
	if err == nil {
		return id, nil
	}
	if errors.Is(err, ErrAmbiguousBuildID) {
		return "", err
	}
	if errors.Is(err, ErrMissingBuildID) {
		return "", err
	}
	if err != nil && !errors.Is(err, ErrUnknownBuild) {
		return "", err
	}

	if !isHexString(spec) || len(spec) < minHexQueryLen {
		return "", ErrUnknownBuild
	}
	return resolveRunningBuildByCommitSHA(ctx, dbConn, strings.ToLower(spec))
}

func isHexString(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'a' && r <= 'f':
		case r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return true
}

func resolveLatestBuildByCommitSHA(ctx context.Context, dbConn *sql.DB, shaLower string) (string, error) {
	var id string
	err := dbConn.QueryRowContext(ctx, `SELECT id FROM builds WHERE lower(sha)=? ORDER BY created_at DESC LIMIT 1`, shaLower).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}

	err = dbConn.QueryRowContext(ctx, `SELECT id FROM builds WHERE lower(sha) LIKE ? ORDER BY created_at DESC LIMIT 1`, shaLower+"%").Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrUnknownBuild
	}
	if err != nil {
		return "", err
	}
	return id, nil
}

func resolveRunningBuildByCommitSHA(ctx context.Context, dbConn *sql.DB, shaLower string) (string, error) {
	var id string
	err := dbConn.QueryRowContext(ctx, `SELECT id FROM builds WHERE state='running' AND lower(sha)=? ORDER BY created_at DESC LIMIT 1`, shaLower).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}

	err = dbConn.QueryRowContext(ctx, `SELECT id FROM builds WHERE state='running' AND lower(sha) LIKE ? ORDER BY created_at DESC LIMIT 1`, shaLower+"%").Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrUnknownBuild
	}
	if err != nil {
		return "", err
	}
	return id, nil
}
