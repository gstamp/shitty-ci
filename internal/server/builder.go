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
	buildID := job.BuildID
	if buildID == "" {
		var err error
		buildID, err = newBuildID()
		if err != nil {
			daemonLog.Printf("could not allocate build id: %v", err)
			return
		}
	}
	logPath := filepath.Join(xdg.LogsDir(a.dataDir), buildID+".log")
	_ = os.MkdirAll(filepath.Dir(logPath), 0o755)

	if err := db.CreateBuild(ctx, a.db, buildID, job.RepoID, job.SHA, job.Ref, types.BuildPending, logPath); err != nil {
		daemonLog.Printf("build %s %s/%s — could not record build in database: %v", shortBuildID(buildID), job.Owner, job.Name, err)
		return
	}
	logBuildStarted(buildID, job)
	buildClock := time.Now()

	// Create a build-level check run for infrastructure failure reporting.
	a.createBuildCheckRun(job.Owner, job.Name, job.SHA, buildID)

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
		a.postBuildFailure(job.Owner, job.Name, job.SHA, buildID, logPath, "git fetch", err.Error())
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
		a.postBuildFailure(job.Owner, job.Name, job.SHA, buildID, logPath, "config", fmt.Sprintf("Invalid .shitty-ci.yml: %v", err))
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
		a.postBuildFailure(job.Owner, job.Name, job.SHA, buildID, logPath, "config", "No steps configured")
		logBuildDone(buildID, job, types.BuildFailure, "no steps in .shitty-ci.yml", buildClock)
		return
	}

	// Tell GitHub we're working on this commit before checkout / long-running prep;
	// previously the first status only appeared after prepareWorkspace finished.
	a.updateCheckRun(buildID, job.Owner, job.Name, job.SHA, "in_progress", "", nil, gh.StatusDescriptionWithLogsHint("Preparing workspace", buildID), -1)

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
		a.postBuildFailure(job.Owner, job.Name, job.SHA, buildID, logPath, "checkout", err.Error())
		logBuildDone(buildID, job, types.BuildFailure, msg, buildClock)
		return
	}

	started := time.Now().Unix()
	_ = db.UpdateBuildState(ctx, a.db, buildID, types.BuildRunning, "", "", started, 0)
	a.updateCheckRun(buildID, job.Owner, job.Name, job.SHA, "in_progress", "", nil, "Build in progress", -1)
	logBuildRunningSteps(buildID, job)

	runnable := make([]shittyyml.Step, 0, len(sf.Steps))
	for _, step := range sf.Steps {
		ok, err := step.ShouldRunRef(job.Ref)
		if err != nil {
			now := time.Now().Unix()
			msg := fmt.Sprintf("invalid step ref filter: %v", err)
			_ = db.UpdateBuildState(ctx, a.db, buildID, types.BuildFailure, step.Name, msg, started, now)
			a.postBuildFailure(job.Owner, job.Name, job.SHA, buildID, logPath, step.Name, fmt.Sprintf("Invalid step filters: %v", err))
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
		a.updateCheckRun(buildID, job.Owner, job.Name, job.SHA, "completed", "success", gh.BuildSuccessOutput(), gh.StatusDescriptionWithLogsHint("All steps skipped (ref filters)", buildID), -1)
		logBuildDone(buildID, job, types.BuildSuccess, "all steps skipped (step branch/tag filters)", buildClock)
		return
	}

	for i, step := range runnable {
		if step.Run == "" {
			now := time.Now().Unix()
			_ = db.UpdateBuildState(ctx, a.db, buildID, types.BuildFailure, step.Name, "empty run command", started, now)
			a.createStepCheckRun(job.Owner, job.Name, job.SHA, buildID, step.Name, i)
			a.postStepFailure(job.Owner, job.Name, job.SHA, buildID, logPath, step.Name, "empty run command", i)
			logBuildDone(buildID, job, types.BuildFailure, fmt.Sprintf("empty run command for step %q", step.Name), buildClock)
			return
		}
		_ = db.UpdateBuildState(ctx, a.db, buildID, types.BuildRunning, step.Name, "", started, 0)
		a.createStepCheckRun(job.Owner, job.Name, job.SHA, buildID, step.Name, i)
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
			a.postStepFailure(job.Owner, job.Name, job.SHA, buildID, logPath, step.Name, fmt.Sprintf("failed to start: %v", err), i)
			logBuildDone(buildID, job, types.BuildFailure, fmt.Sprintf("step %q failed to start: %v", step.Name, err), buildClock)
			return
		}
		curPID.Store(int32(cmd.Process.Pid))
		waitErr := cmd.Wait()
		curPID.Store(0)

		if a.consumeUserCancel(buildID) {
			now := time.Now().Unix()
			_ = db.UpdateBuildState(ctx, a.db, buildID, types.BuildCancelled, step.Name, "cancelled", started, now)
			a.updateCheckRun(buildID, job.Owner, job.Name, job.SHA, "completed", "cancelled", gh.BuildCancelledOutput(), gh.StatusDescriptionWithLogsHint("Build cancelled by user", buildID), i)
			a.updateCheckRun(buildID, job.Owner, job.Name, job.SHA, "completed", "cancelled", nil, gh.StatusDescriptionWithLogsHint("Build cancelled by user", buildID), -1)
			logBuildDone(buildID, job, types.BuildCancelled, fmt.Sprintf("cancelled during step %q", step.Name), buildClock)
			return
		}
		if buildCtx.Err() == context.DeadlineExceeded {
			now := time.Now().Unix()
			_ = db.UpdateBuildState(ctx, a.db, buildID, types.BuildTimedOut, step.Name, "timed out", started, now)
			a.updateCheckRun(buildID, job.Owner, job.Name, job.SHA, "completed", "timed_out", gh.BuildTimedOutOutput(timeout.String()), gh.StatusDescriptionWithLogsHint(fmt.Sprintf("Build timed out after %s", timeout), buildID), i)
			a.updateCheckRun(buildID, job.Owner, job.Name, job.SHA, "completed", "timed_out", nil, gh.StatusDescriptionWithLogsHint(fmt.Sprintf("Build timed out after %s", timeout), buildID), -1)
			logBuildDone(buildID, job, types.BuildTimedOut, fmt.Sprintf("timed out in step %q after %s", step.Name, timeout), buildClock)
			return
		}
		if waitErr != nil {
			now := time.Now().Unix()
			_ = db.UpdateBuildState(ctx, a.db, buildID, types.BuildFailure, step.Name, waitErr.Error(), started, now)
			a.postStepFailure(job.Owner, job.Name, job.SHA, buildID, logPath, step.Name, waitErr.Error(), i)
			logBuildDone(buildID, job, types.BuildFailure, fmt.Sprintf("step %q failed: %v", step.Name, waitErr), buildClock)
			return
		}
	}

	now := time.Now().Unix()
	_ = db.UpdateBuildState(ctx, a.db, buildID, types.BuildSuccess, "", "", started, now)
	fmt.Fprintf(logf, "\n== finished: success ==\n")
	a.updateCheckRun(buildID, job.Owner, job.Name, job.SHA, "completed", "success", gh.BuildSuccessOutput(), "All steps passed", -1)
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

// getGitHubAppToken fetches a fresh GitHub App installation token.
// Returns empty string if the GitHub App is not configured or if token
// acquisition fails (errors are logged, not returned).
func (a *App) getGitHubAppToken() string {
	cfg := a.store.Get()
	if cfg.GitHubApp.AppID == 0 || cfg.GitHubApp.InstallationID == 0 || cfg.GitHubApp.PrivateKeyPath == "" {
		return ""
	}
	pem, err := os.ReadFile(cfg.GitHubApp.PrivateKeyPath)
	if err != nil {
		daemonLog.Printf("github app: could not read private key %s: %v", cfg.GitHubApp.PrivateKeyPath, err)
		return ""
	}
	token, err := gh.GetInstallationToken(cfg.GitHubApp.AppID, cfg.GitHubApp.InstallationID, pem)
	if err != nil {
		daemonLog.Printf("github app: could not get installation token: %v", err)
		return ""
	}
	return token
}

// apiToken returns a token suitable for any GitHub API call. If a GitHub App
// is configured it returns a fresh installation token (which can create check
// runs and post commit statuses). Otherwise returns the PAT. Returns empty
// string when neither credential is available.
func (a *App) apiToken() string {
	cfg := a.store.Get()
	if cfg.GitHubApp.AppID > 0 || cfg.GitHubApp.InstallationID > 0 || cfg.GitHubApp.PrivateKeyPath != "" {
		return a.getGitHubAppToken()
	}
	return cfg.GitHubToken
}

// postStatus posts a commit status. Uses the best available credential
// (GitHub App installation token > PAT). Silently skips when neither is set.
func (a *App) postStatus(owner, name, sha, state, desc string) {
	token := a.apiToken()
	if token == "" {
		return
	}
	if err := gh.PostStatus(token, owner, name, sha, state, desc, gh.CommitStatusTargetURL(owner, name, sha)); err != nil {
		daemonLog.Printf("github status post failed for %s/%s@%s: %v", owner, name, shortSHA(sha), err)
	}
}

// checkRunKey returns the sync.Map key for a check run. stepIndex -1 means
// the build-level check run; non-negative means a per-step check run.
func checkRunKey(buildID string, stepIndex int) string {
	if stepIndex < 0 {
		return buildID + ":"
	}
	return fmt.Sprintf("%s:%d", buildID, stepIndex)
}

// createBuildCheckRun creates a build-level check run named "shitty-ci".
// Posts a pending commit status alongside. Falls back to status-only when
// no GitHub App is configured.
func (a *App) createBuildCheckRun(owner, name, sha, buildID string) {
	token := a.getGitHubAppToken()
	if token == "" {
		a.postStatus(owner, name, sha, "pending", "Build started")
		return
	}
	targetURL := gh.CommitStatusTargetURL(owner, name, sha)
	id, err := gh.CreateCheckRun(token, owner, name, sha, "shitty-ci", targetURL)
	if err != nil {
		daemonLog.Printf("github build check run creation failed for %s/%s@%s: %v", owner, name, shortSHA(sha), err)
		a.postStatus(owner, name, sha, "pending", "Build started")
		return
	}
	a.checkRuns.Store(checkRunKey(buildID, -1), id)
	a.postStatus(owner, name, sha, "pending", "Build started")
}

// createStepCheckRun creates a per-step check run "shitty-ci / <stepName>"
// and posts a pending commit status.
func (a *App) createStepCheckRun(owner, name, sha, buildID, stepName string, stepIndex int) {
	if stepName == "" {
		return
	}
	token := a.getGitHubAppToken()
	if token == "" {
		a.postStatus(owner, name, sha, "pending", "Running: "+stepName)
		return
	}
	targetURL := gh.CommitStatusTargetURL(owner, name, sha)
	checkName := "shitty-ci / " + stepName
	id, err := gh.CreateCheckRun(token, owner, name, sha, checkName, targetURL)
	if err != nil {
		daemonLog.Printf("github check run creation failed for step %q: %v", stepName, err)
		a.postStatus(owner, name, sha, "pending", "Running: "+stepName)
		return
	}
	a.checkRuns.Store(checkRunKey(buildID, stepIndex), id)
	a.postStatus(owner, name, sha, "pending", "Running: "+stepName)
}

// updateCheckRun updates a check run identified by buildID+stepIndex.
// Pass stepIndex=-1 for build-level check runs.
// Falls back to posting a commit status if the check run doesn't exist.
func (a *App) updateCheckRun(buildID, owner, name, sha, status, conclusion string, output *gh.CheckRunOutput, statusDesc string, stepIndex int) {
	token := a.apiToken()
	if token == "" {
		return
	}

	// Try check run first.
	key := checkRunKey(buildID, stepIndex)
	if v, ok := a.checkRuns.Load(key); ok {
		crID, ok := v.(int64)
		if ok {
			targetURL := gh.CommitStatusTargetURL(owner, name, sha)
			if err := gh.UpdateCheckRun(token, owner, name, crID, status, conclusion, output, targetURL); err != nil {
				daemonLog.Printf("github check run update failed for build %s: %v (falling back to commit status)", shortBuildID(buildID), err)
			}
		}
	}

	// Always post a commit status as well.
	if statusDesc != "" {
		if err := gh.PostStatus(token, owner, name, sha, stateFromConclusion(conclusion, status), statusDesc, gh.CommitStatusTargetURL(owner, name, sha)); err != nil {
			daemonLog.Printf("github status post failed for %s/%s@%s: %v", owner, name, shortSHA(sha), err)
		}
	}
}

// postBuildFailure posts a build-level failure (check run + commit status).
// Used for infrastructure errors before steps start.
func (a *App) postBuildFailure(owner, name, sha, buildID, logPath, phase, errText string) {
	token := a.apiToken()
	if token == "" {
		return
	}

	var logTail string
	if logPath != "" {
		if data, err := gh.ReadLogTail(logPath, gh.LogTailBytes); err == nil {
			logTail = data
		}
	}

	output := gh.BuildFailureOutput(phase, errText, buildID, logTail)

	// Try build-level check run first.
	if v, ok := a.checkRuns.Load(checkRunKey(buildID, -1)); ok {
		crID, ok := v.(int64)
		if ok {
			targetURL := gh.CommitStatusTargetURL(owner, name, sha)
			if err := gh.UpdateCheckRun(token, owner, name, crID, "completed", "failure", output, targetURL); err != nil {
				daemonLog.Printf("github check run update failed for build %s: %v (falling back to commit status)", shortBuildID(buildID), err)
			}
		}
	}

	desc := gh.StepFailureDescription(phase, errText, buildID)
	if err := gh.PostStatus(token, owner, name, sha, "failure", desc, gh.CommitStatusTargetURL(owner, name, sha)); err != nil {
		daemonLog.Printf("github status post failed for %s/%s@%s: %v", owner, name, shortSHA(sha), err)
	}
}

// postStepFailure posts a step-level failure (check run + commit status).
func (a *App) postStepFailure(owner, name, sha, buildID, logPath, stepName, errText string, stepIndex int) {
	token := a.apiToken()
	if token == "" {
		return
	}

	var logTail string
	if logPath != "" {
		if data, err := gh.ReadLogTail(logPath, gh.LogTailBytes); err == nil {
			logTail = data
		}
	}

	output := gh.BuildFailureOutput(stepName, errText, buildID, logTail)

	// Try step's check run first.
	if v, ok := a.checkRuns.Load(checkRunKey(buildID, stepIndex)); ok {
		crID, ok := v.(int64)
		if ok {
			targetURL := gh.CommitStatusTargetURL(owner, name, sha)
			if err := gh.UpdateCheckRun(token, owner, name, crID, "completed", "failure", output, targetURL); err != nil {
				daemonLog.Printf("github check run update failed for build %s: %v (falling back to commit status)", shortBuildID(buildID), err)
			}
		}
	}

	desc := gh.StepFailureDescription(stepName, errText, buildID)
	if err := gh.PostStatus(token, owner, name, sha, "failure", desc, gh.CommitStatusTargetURL(owner, name, sha)); err != nil {
		daemonLog.Printf("github status post failed for %s/%s@%s: %v", owner, name, shortSHA(sha), err)
	}
}

// stateFromConclusion maps a check run conclusion to a commit status API state.
// When conclusion is empty (still in progress), it returns the check run status
// mapped to a valid commit status value: "in_progress" → "pending", anything else
// is passed through as-is or defaults to "pending".
func stateFromConclusion(conclusion, checkRunStatus string) string {
	if conclusion != "" {
		switch conclusion {
		case "success":
			return "success"
		case "failure":
			return "failure"
		case "neutral", "cancelled", "timed_out", "action_required":
			return "error"
		default:
			return "error"
		}
	}
	// No conclusion means still in progress.
	switch checkRunStatus {
	case "in_progress", "queued":
		return "pending"
	default:
		return "pending"
	}
}

// buildFinishedConclusion maps an internal BuildState to a Check Run conclusion.
func buildFinishedConclusion(state types.BuildState) string {
	switch state {
	case types.BuildSuccess:
		return "success"
	case types.BuildFailure:
		return "failure"
	case types.BuildTimedOut:
		return "timed_out"
	case types.BuildCancelled:
		return "cancelled"
	case types.BuildInterrupted:
		return "neutral"
	default:
		return "neutral"
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
