package server

import (
	"context"
	"fmt"
	"strings"
	"time"

	"shitty-ci/internal/db"
	gh "shitty-ci/internal/github"
	"shitty-ci/internal/types"
)

func (a *App) recoverStaleBuilds(ctx context.Context) {
	builds, err := db.ListUnfinishedBuilds(ctx, a.db)
	if err != nil {
		daemonLog.Printf("recovery: could not list unfinished builds: %v", err)
		return
	}
	if len(builds) == 0 {
		return
	}

	now := time.Now().Unix()
	daemonLog.Printf("recovery: reconciling %d unfinished build(s) from a prior daemon run", len(builds))

	for _, b := range builds {
		desc := "daemon restarted while this build was in progress"
		if s := strings.TrimSpace(b.Step); s != "" {
			desc = fmt.Sprintf("daemon restarted during step %q", s)
		}
		if err := db.MarkBuildInterrupted(ctx, a.db, b.ID, desc, now); err != nil {
			daemonLog.Printf("recovery: could not mark build %s: %v", shortBuildID(b.ID), err)
			continue
		}
		daemonLog.Printf("recovery: build %s %s/%s@%s — marked interrupted", shortBuildID(b.ID), b.Owner, b.Name, shortSHA(b.SHA))

		token := a.store.Get().GitHubToken
		if token == "" {
			continue
		}
		_, ghDesc := gh.MapBuildState(string(types.BuildInterrupted))
		if err := gh.PostStatus(token, b.Owner, b.Name, b.SHA, "error", gh.StatusDescriptionWithLogsHint(ghDesc, b.ID), gh.CommitStatusTargetURL(b.Owner, b.Name, b.SHA)); err != nil {
			daemonLog.Printf("recovery: github status post failed for %s/%s@%s: %v", b.Owner, b.Name, shortSHA(b.SHA), err)
		}
	}
}
