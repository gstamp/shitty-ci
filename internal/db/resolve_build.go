package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

var (
	// ErrMissingBuildID is returned when the caller passes an empty id.
	ErrMissingBuildID = errors.New("missing build id")
	// ErrUnknownBuild is returned when no build matches the given id/prefix.
	ErrUnknownBuild = errors.New("unknown build")
	// ErrAmbiguousBuildID is returned when multiple builds share the same id prefix.
	ErrAmbiguousBuildID = errors.New("ambiguous build id")
)

// ResolveBuildID returns the full build primary key for an exact id or a unique hex prefix.
// Prefix matches require at least minBuildIDPrefixLen characters.
const minBuildIDPrefixLen = 4

func ResolveBuildID(ctx context.Context, dbConn *sql.DB, idOrPrefix string) (string, error) {
	idOrPrefix = strings.TrimSpace(idOrPrefix)
	if idOrPrefix == "" {
		return "", ErrMissingBuildID
	}

	var id string
	err := dbConn.QueryRowContext(ctx, `SELECT id FROM builds WHERE id=?`, idOrPrefix).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}

	if len(idOrPrefix) < minBuildIDPrefixLen {
		return "", ErrUnknownBuild
	}

	rows, err := dbConn.QueryContext(ctx, `SELECT id FROM builds WHERE id LIKE ?`, idOrPrefix+"%")
	if err != nil {
		return "", err
	}
	defer rows.Close()

	var matches []string
	for rows.Next() {
		var bid string
		if err := rows.Scan(&bid); err != nil {
			return "", err
		}
		matches = append(matches, bid)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}

	switch len(matches) {
	case 0:
		return "", ErrUnknownBuild
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("%w: %q matches %d builds", ErrAmbiguousBuildID, idOrPrefix, len(matches))
	}
}
