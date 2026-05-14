package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"shitty-ci/internal/db"
	gh "shitty-ci/internal/github"
	"shitty-ci/internal/gitutil"
	"shitty-ci/internal/shittyyml"
	"shitty-ci/internal/types"
	"shitty-ci/internal/xdg"
)

func newBuildID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func (a *App) killTree(pid int) {
	if pid <= 0 {
		return
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL)
}

func logBuildStarted(buildID string, job BuildJob) {
	daemonLog.Printf("build %s %s/%s ref=%s sha=%s — started", shortBuildID(buildID), job.Owner, job.Name, job.Ref, shortSHA(job.SHA))
}

func logBuildRunningSteps(buildID string, job BuildJob) {
	daemonLog.Printf("build %s %s/%s — running steps", shortBuildID(buildID), job.Owner, job.Name)
}

func logBuildStepProgress(buildID string, job BuildJob, stepNum, stepTotal int, stepName string) {
	name := strings.TrimSpace(stepName)
	if name == "" {
		name = "(unnamed)"
	}
	daemonLog.Printf("build %s %s/%s — step %d/%d: %s", shortBuildID(buildID), job.Owner, job.Name, stepNum, stepTotal, name)
}

func logBuildDone(buildID string, job BuildJob, state types.BuildState, detail string, since time.Time) {
	repo := job.Owner + "/" + job.Name
	detail = strings.TrimSpace(detail)
	elapsed := ""
	if !since.IsZero() {
		elapsed = fmt.Sprintf(" in %s", time.Since(since).Truncate(time.Millisecond))
	}
	if detail != "" {
		daemonLog.Printf("build %s %s — finished: %s (%s)%s", shortBuildID(buildID), repo, state, ellipsize(detail, 200), elapsed)
		return
	}
	daemonLog.Printf("build %s %s — finished: %s%s", shortBuildID(buildID), repo, state, elapsed)
}

func (a *App) runBuild(parent context.Context, job BuildJob) {
	ctx := parent
	buildID, err := newBuildID()
	if err != nil {
		daemonLog.Printf("could not allocate build id: %v", err)
		return
	}
	logPath := filepath.Join(xdg.LogsDir(a.dataDir), buildID+".log")
	_ = os.MkdirAll(filepath.Dir(logPath), 0o755)

	if err := db.CreateBuild(ctx, a.db, buildID, job.RepoID, job.SHA, job.Ref, types.BuildPending, logPath); err != nil {
		daemonLog.Printf("build %s %s/%s — could not record build in database: %v", shortBuildID(buildID), job.Owner, job.Name, err)
		return
	}
	logBuildStarted(buildID, job)
	buildClock := time.Now()

	var curPID atomic.Int32
	a.setActivePID(buildID, &curPID)
	defer a.clearActive(buildID)

	wsPath, wsRow, err := a.acquireWorkspace(ctx, job.RepoID, job.Owner, job.Name)
	if err != nil {
		now := time.Now().Unix()
		_ = db.UpdateBuildState(ctx, a.db, buildID, types.BuildFailure, "", "workspace acquire failed", now, now)
		logBuildDone(buildID, job, types.BuildFailure, "workspace acquire failed", buildClock)
		return
	}
	defer func() { _ = a.releaseWorkspace(context.Background(), wsRow) }()

	logf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		now := time.Now().Unix()
		_ = db.UpdateBuildState(ctx, a.db, buildID, types.BuildFailure, "", "log file open failed", now, now)
		logBuildDone(buildID, job, types.BuildFailure, "log file open failed", buildClock)
		return
	}
	defer logf.Close()

	fmt.Fprintf(logf, "== shitty-ci build %s ==\nrepo: %s/%s\nsha: %s\nref: %s\n\n", buildID, job.Owner, job.Name, job.SHA, job.Ref)

	if _, err := gitutil.RunGitOutput(wsPath, "fetch", "--tags", "--prune"); err != nil {
		now := time.Now().Unix()
		msg := fmt.Sprintf("git fetch failed: %v", err)
		_ = db.UpdateBuildState(ctx, a.db, buildID, types.BuildFailure, "", msg, now, now)
		a.postGitHub(job.Owner, job.Name, job.SHA, "failure", gh.StatusDescriptionWithLogsHint(fmt.Sprintf("git fetch failed: %v", err), buildID))
		logBuildDone(buildID, job, types.BuildFailure, msg, buildClock)
		return
	}

	ymlData, err := gitutil.RunGitOutput(wsPath, "show", job.SHA+":.shitty-ci.yml")
	if err != nil {
		// Missing config: skip silently (no GitHub status).
		now := time.Now().Unix()
		_ = db.UpdateBuildState(ctx, a.db, buildID, types.BuildSuccess, "", "skipped (no .shitty-ci.yml)", now, now)
		logBuildDone(buildID, job, types.BuildSuccess, "skipped (no .shitty-ci.yml)", buildClock)
		return
	}
	sf, err := shittyyml.Parse(ymlData)
	if err != nil {
		now := time.Now().Unix()
		_ = db.UpdateBuildState(ctx, a.db, buildID, types.BuildFailure, "", "invalid .shitty-ci.yml", now, now)
		a.postGitHub(job.Owner, job.Name, job.SHA, "failure", gh.StatusDescriptionWithLogsHint(fmt.Sprintf("Invalid .shitty-ci.yml: %v", err), buildID))
		logBuildDone(buildID, job, types.BuildFailure, "invalid .shitty-ci.yml", buildClock)
		return
	}
	ok, err := sf.ShouldBuildRef(job.Ref)
	if err != nil || !ok {
		now := time.Now().Unix()
		_ = db.UpdateBuildState(ctx, a.db, buildID, types.BuildSuccess, "", "skipped (branch/tag filters)", now, now)
		logBuildDone(buildID, job, types.BuildSuccess, "skipped (branch/tag filters)", buildClock)
		return
	}
	if len(sf.Steps) == 0 {
		now := time.Now().Unix()
		_ = db.UpdateBuildState(ctx, a.db, buildID, types.BuildFailure, "", "no steps in .shitty-ci.yml", now, now)
		a.postGitHub(job.Owner, job.Name, job.SHA, "failure", gh.StatusDescriptionWithLogsHint("No steps configured", buildID))
		logBuildDone(buildID, job, types.BuildFailure, "no steps in .shitty-ci.yml", buildClock)
		return
	}

	// Tell GitHub we're working on this commit before checkout / long-running prep;
	// previously the first status only appeared after prepareWorkspace finished.
	a.postGitHub(job.Owner, job.Name, job.SHA, "pending", gh.StatusDescriptionWithLogsHint("Preparing workspace", buildID))

	cfg := a.store.Get()
	timeout := cfg.BuildTimeout
	if d, err := sf.StepBuildTimeout(); err == nil && d > 0 {
		timeout = d
	}
	buildCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	stopWatch := make(chan struct{})
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		select {
		case <-buildCtx.Done():
			a.killTree(int(curPID.Load()))
		case <-stopWatch:
		}
	}()
	defer func() {
		close(stopWatch)
		<-watchDone
	}()

	if err := prepareWorkspace(wsPath, job.SHA, logf); err != nil {
		now := time.Now().Unix()
		msg := fmt.Sprintf("checkout failed: %v", err)
		_ = db.UpdateBuildState(ctx, a.db, buildID, types.BuildFailure, "", msg, now, now)
		a.postGitHub(job.Owner, job.Name, job.SHA, "failure", gh.StatusDescriptionWithLogsHint(fmt.Sprintf("git checkout failed: %v", err), buildID))
		logBuildDone(buildID, job, types.BuildFailure, msg, buildClock)
		return
	}

	started := time.Now().Unix()
	_ = db.UpdateBuildState(ctx, a.db, buildID, types.BuildRunning, "", "", started, 0)
	a.postGitHub(job.Owner, job.Name, job.SHA, "pending", "Build in progress")
	logBuildRunningSteps(buildID, job)

	runnable := make([]shittyyml.Step, 0, len(sf.Steps))
	for _, step := range sf.Steps {
		ok, err := step.ShouldRunRef(job.Ref)
		if err != nil {
			now := time.Now().Unix()
			msg := fmt.Sprintf("invalid step ref filter: %v", err)
			_ = db.UpdateBuildState(ctx, a.db, buildID, types.BuildFailure, step.Name, msg, started, now)
			a.postGitHub(job.Owner, job.Name, job.SHA, "failure", gh.StatusDescriptionWithLogsHint(fmt.Sprintf("Invalid step filters: %v", err), buildID))
			logBuildDone(buildID, job, types.BuildFailure, fmt.Sprintf("step %q: %s", step.Name, msg), buildClock)
			return
		}
		if !ok {
			name := strings.TrimSpace(step.Name)
			if name == "" {
				name = "(unnamed)"
			}
			fmt.Fprintf(logf, "skipping step %q (branch/tag filters)\n", name)
			continue
		}
		runnable = append(runnable, step)
	}
	if len(runnable) == 0 {
		now := time.Now().Unix()
		_ = db.UpdateBuildState(ctx, a.db, buildID, types.BuildSuccess, "", "all steps skipped (step branch/tag filters)", now, now)
		fmt.Fprintf(logf, "\n== finished: success (no matching steps) ==\n")
		a.postGitHub(job.Owner, job.Name, job.SHA, "success", gh.StatusDescriptionWithLogsHint("All steps skipped (ref filters)", buildID))
		logBuildDone(buildID, job, types.BuildSuccess, "all steps skipped (step branch/tag filters)", buildClock)
		return
	}

	for i, step := range runnable {
		if step.Run == "" {
			now := time.Now().Unix()
			_ = db.UpdateBuildState(ctx, a.db, buildID, types.BuildFailure, step.Name, "empty run command", started, now)
			a.postGitHub(job.Owner, job.Name, job.SHA, "failure", gh.StepFailureDescription(step.Name, "empty run command", buildID))
			logBuildDone(buildID, job, types.BuildFailure, fmt.Sprintf("empty run command for step %q", step.Name), buildClock)
			return
		}
		_ = db.UpdateBuildState(ctx, a.db, buildID, types.BuildRunning, step.Name, "", started, 0)
		a.postGitHub(job.Owner, job.Name, job.SHA, "pending", gh.StatusDescriptionWithLogsHint("Running: "+step.Name, buildID))
		logBuildStepProgress(buildID, job, i+1, len(runnable), step.Name)

		secretEnv, err := db.GetRepoSecrets(ctx, a.db, job.RepoID)
		if err != nil {
			daemonLog.Printf("build %s %s/%s — could not load repo secrets: %v", shortBuildID(buildID), job.Owner, job.Name, err)
			secretEnv = nil
		}

		cmd := exec.Command("sh", "-c", step.Run)
		cmd.Dir = wsPath
		cmd.Stdout = logf
		cmd.Stderr = logf
		cmd.Env = append(os.Environ(), envPairs(sf.Env)...)
		cmd.Env = append(cmd.Env, envPairs(secretEnv)...)
		cmd.Env = append(cmd.Env,
			"SHITTY_CI_REF="+job.Ref,
			"SHITTY_CI_SHA="+job.SHA,
			"SHITTY_CI_REPO="+job.Owner+"/"+job.Name,
		)
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

		if err := cmd.Start(); err != nil {
			now := time.Now().Unix()
			_ = db.UpdateBuildState(ctx, a.db, buildID, types.BuildFailure, step.Name, err.Error(), started, now)
			a.postGitHub(job.Owner, job.Name, job.SHA, "failure", gh.StepFailureDescription(step.Name, fmt.Sprintf("failed to start: %v", err), buildID))
			logBuildDone(buildID, job, types.BuildFailure, fmt.Sprintf("step %q failed to start: %v", step.Name, err), buildClock)
			return
		}
		curPID.Store(int32(cmd.Process.Pid))
		waitErr := cmd.Wait()
		curPID.Store(0)

		if a.consumeUserCancel(buildID) {
			now := time.Now().Unix()
			_ = db.UpdateBuildState(ctx, a.db, buildID, types.BuildCancelled, step.Name, "cancelled", started, now)
			a.postGitHub(job.Owner, job.Name, job.SHA, "error", gh.StatusDescriptionWithLogsHint("Build cancelled by user", buildID))
			logBuildDone(buildID, job, types.BuildCancelled, fmt.Sprintf("cancelled during step %q", step.Name), buildClock)
			return
		}
		if buildCtx.Err() == context.DeadlineExceeded {
			now := time.Now().Unix()
			_ = db.UpdateBuildState(ctx, a.db, buildID, types.BuildTimedOut, step.Name, "timed out", started, now)
			a.postGitHub(job.Owner, job.Name, job.SHA, "error", gh.StatusDescriptionWithLogsHint(fmt.Sprintf("Build timed out after %s", timeout), buildID))
			logBuildDone(buildID, job, types.BuildTimedOut, fmt.Sprintf("timed out in step %q after %s", step.Name, timeout), buildClock)
			return
		}
		if waitErr != nil {
			now := time.Now().Unix()
			_ = db.UpdateBuildState(ctx, a.db, buildID, types.BuildFailure, step.Name, waitErr.Error(), started, now)
			a.postGitHub(job.Owner, job.Name, job.SHA, "failure", gh.StepFailureDescription(step.Name, waitErr.Error(), buildID))
			logBuildDone(buildID, job, types.BuildFailure, fmt.Sprintf("step %q failed: %v", step.Name, waitErr), buildClock)
			return
		}
	}

	now := time.Now().Unix()
	_ = db.UpdateBuildState(ctx, a.db, buildID, types.BuildSuccess, "", "", started, now)
	fmt.Fprintf(logf, "\n== finished: success ==\n")
	a.postGitHub(job.Owner, job.Name, job.SHA, "success", "All steps passed")
	logBuildDone(buildID, job, types.BuildSuccess, "", buildClock)
}

func envPairs(m map[string]string) []string {
	if m == nil {
		return nil
	}
	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	return out
}

func (a *App) postGitHub(owner, name, sha, state, desc string) {
	token := a.store.Get().GitHubToken
	if token == "" {
		return
	}
	if err := gh.PostStatus(token, owner, name, sha, state, desc, gh.CommitStatusTargetURL(owner, name, sha)); err != nil {
		daemonLog.Printf("github status post failed for %s/%s@%s: %v", owner, name, shortSHA(sha), err)
	}
}

func prepareWorkspace(wsPath, sha string, logf *os.File) error {
	if _, err := gitutil.RunGit(wsPath, "checkout", "--force", sha); err != nil {
		return err
	}
	if _, err := gitutil.RunGit(wsPath, "clean", "-fd"); err != nil {
		return err
	}
	if logf != nil {
		fmt.Fprintf(logf, "checked out %s and cleaned (-fd)\n", sha)
	}
	return nil
}
