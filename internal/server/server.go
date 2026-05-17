package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"database/sql"

	"shitty-ci/internal/config"
	"shitty-ci/internal/db"
	"shitty-ci/internal/gitutil"
	"shitty-ci/internal/proto"
	"shitty-ci/internal/shittyyml"
	"shitty-ci/internal/types"
	"shitty-ci/internal/xdg"
)

// daemonLog writes to stderr; the standard logger serializes concurrent writes.
var daemonLog = log.New(os.Stderr, "shitty-ci: ", log.LstdFlags)

// BuildJob is queued work for one commit on a ref.
type BuildJob struct {
	RepoID int64
	Owner  string
	Name   string
	SHA    string
	Ref    string
}

type App struct {
	db         *sql.DB
	store      *config.Store
	dataDir    string
	socketPath string

	queue chan BuildJob

	runMu   sync.Mutex
	running int

	activeMu sync.Mutex
	active   map[string]*activeBuild
}

type activeBuild struct {
	userCancel atomic.Bool
	pid        *atomic.Int32
}

func NewApp(dbConn *sql.DB, store *config.Store, dataDir, socketPath string) *App {
	return &App{
		db:         dbConn,
		store:      store,
		dataDir:    dataDir,
		socketPath: socketPath,
		queue:      make(chan BuildJob, 256),
		active:     make(map[string]*activeBuild),
	}
}

func (a *App) setActivePID(buildID string, pid *atomic.Int32) {
	a.activeMu.Lock()
	defer a.activeMu.Unlock()
	a.active[buildID] = &activeBuild{pid: pid}
}

func (a *App) clearActive(buildID string) {
	a.activeMu.Lock()
	delete(a.active, buildID)
	a.activeMu.Unlock()
}

func (a *App) consumeUserCancel(buildID string) bool {
	a.activeMu.Lock()
	ab, ok := a.active[buildID]
	a.activeMu.Unlock()
	if !ok {
		return false
	}
	return ab.userCancel.Load()
}

func (a *App) requestCancel(buildID string) bool {
	a.activeMu.Lock()
	ab, ok := a.active[buildID]
	a.activeMu.Unlock()
	if !ok || ab.pid == nil {
		return false
	}
	ab.userCancel.Store(true)
	if pid := int(ab.pid.Load()); pid != 0 {
		a.killTree(pid)
	}
	return true
}

func (a *App) Run(ctx context.Context) error {
	if err := ensureDataDirs(a.dataDir); err != nil {
		return err
	}
	_ = a.store.Refresh()

	a.recoverStaleBuilds(ctx)

	go a.configReloader(ctx)
	go a.scheduler(ctx)
	go a.pruner(ctx)

	removeSocket(a.socketPath)
	if err := os.MkdirAll(filepath.Dir(a.socketPath), 0o755); err != nil {
		return err
	}
	ln, err := net.Listen("unix", a.socketPath)
	if err != nil {
		return err
	}
	defer ln.Close()
	_ = os.Chmod(a.socketPath, 0o600)

	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	a.printStartupSummary(ctx)

	go a.poller(ctx)

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		go a.handleConn(conn)
	}
}

func (a *App) configReloader(ctx context.Context) {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_ = a.store.Refresh()
		}
	}
}

func (a *App) maxConcurrent() int {
	cfg := a.store.Get()
	if cfg.MaxConcurrentBuilds <= 0 {
		return 4
	}
	return cfg.MaxConcurrentBuilds
}

func (a *App) scheduler(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case job := <-a.queue:
			for {
				a.runMu.Lock()
				if a.running < a.maxConcurrent() {
					a.running++
					a.runMu.Unlock()
					break
				}
				a.runMu.Unlock()
				select {
				case <-ctx.Done():
					return
				case <-time.After(50 * time.Millisecond):
				}
			}
			go func(j BuildJob) {
				defer func() {
					a.runMu.Lock()
					a.running--
					a.runMu.Unlock()
				}()
				a.runBuild(ctx, j)
			}(job)
		}
	}
}

func (a *App) pruner(ctx context.Context) {
	t := time.NewTicker(10 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			ttl := a.store.Get().WorkspaceTTL
			_ = a.pruneWorkspaces(ctx, ttl)
		}
	}
}

func (a *App) poller(ctx context.Context) {
	d := a.store.Get().PollInterval
	if d <= 0 {
		d = 30 * time.Second
	}
	tick := time.NewTicker(d)
	defer tick.Stop()
	for {
		a.pollOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			d = a.store.Get().PollInterval
			if d <= 0 {
				d = 30 * time.Second
			}
			tick.Reset(d)
		}
	}
}

func (a *App) pollOnce(ctx context.Context) {
	repos, err := db.ListRepos(ctx, a.db)
	if err != nil {
		return
	}
	for _, r := range repos {
		a.pollRepo(ctx, r)
	}
}

func (a *App) pollRepo(ctx context.Context, r types.Repo) {
	var jobs []BuildJob
	func() {
		wsPath, wsRow, err := a.acquireWorkspace(ctx, r.ID, r.Owner, r.Name)
		if err != nil {
			daemonLog.Printf("poll %s/%s: workspace acquire failed: %v", r.Owner, r.Name, err)
			return
		}
		defer func() { _ = a.releaseWorkspace(context.Background(), wsRow) }()

		if _, err := gitutil.RunGitOutput(wsPath, "fetch", "--tags", "--prune"); err != nil {
			daemonLog.Printf("poll %s/%s: git fetch failed: %v", r.Owner, r.Name, err)
			return
		}

		refs, err := listInterestingRefs(wsPath)
		if err != nil {
			daemonLog.Printf("poll %s/%s: list refs failed: %v", r.Owner, r.Name, err)
			return
		}
		repoLabel := r.Owner + "/" + r.Name
		for _, rf := range refs {
			tip, err := refTipSHA(wsPath, rf)
			if err != nil || tip == "" {
				continue
			}
			last, err := db.GetRefSHA(ctx, a.db, r.ID, rf)
			if err != nil {
				continue
			}
			if last == tip {
				continue
			}

			lastShort, tipShort := shortSHA(last), shortSHA(tip)

			ymlData, err := gitutil.RunGitOutput(wsPath, "show", tip+":.shitty-ci.yml")
			if err != nil {
				daemonLog.Printf("poll %s ref=%s: %s -> %s (skipped: no .shitty-ci.yml)", repoLabel, rf, lastShort, tipShort)
				_ = db.SetRefSHA(ctx, a.db, r.ID, rf, tip)
				continue
			}
			sf, err := shittyyml.Parse(ymlData)
			if err != nil {
				daemonLog.Printf("poll %s ref=%s: %s -> %s (skipped: invalid .shitty-ci.yml: %v)", repoLabel, rf, lastShort, tipShort, err)
				_ = db.SetRefSHA(ctx, a.db, r.ID, rf, tip)
				continue
			}
			ok, err := sf.ShouldBuildRef(rf)
			if err != nil || !ok {
				reason := "branch/tag filters"
				if err != nil {
					reason = fmt.Sprintf("branch/tag filters: %v", err)
				}
				daemonLog.Printf("poll %s ref=%s: %s -> %s (skipped: %s)", repoLabel, rf, lastShort, tipShort, reason)
				_ = db.SetRefSHA(ctx, a.db, r.ID, rf, tip)
				continue
			}

			daemonLog.Printf("poll %s ref=%s: %s -> %s (queued build)", repoLabel, rf, lastShort, tipShort)
			jobs = append(jobs, BuildJob{RepoID: r.ID, Owner: r.Owner, Name: r.Name, SHA: tip, Ref: rf})
		}
	}()

	for _, j := range jobs {
		select {
		case a.queue <- j:
		case <-ctx.Done():
			return
		}
		if err := db.SetRefSHA(ctx, a.db, j.RepoID, j.Ref, j.SHA); err != nil {
			return
		}
	}
}

func listInterestingRefs(ws string) ([]string, error) {
	out, err := gitutil.RunGit(ws, "for-each-ref", "--format=%(refname)", "refs/remotes/origin", "refs/tags")
	if err != nil {
		return nil, err
	}
	var refs []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasSuffix(line, "/HEAD") {
			continue
		}
		refs = append(refs, line)
	}
	return refs, nil
}

func refTipSHA(ws, ref string) (string, error) {
	// for remote branches, ref already points to commit; for tags too.
	return gitutil.RunGit(ws, "rev-parse", ref)
}

func (a *App) handleConn(c net.Conn) {
	defer c.Close()
	dec := json.NewDecoder(c)
	var req proto.Request
	if err := dec.Decode(&req); err != nil {
		_ = json.NewEncoder(c).Encode(proto.Err("invalid request"))
		return
	}
	resp := a.dispatch(context.Background(), req)
	_ = json.NewEncoder(c).Encode(resp)
}

func (a *App) dispatch(ctx context.Context, req proto.Request) proto.Response {
	switch req.Cmd {
	case "ping":
		return proto.OK("pong")
	case "repos_list":
		repos, err := db.ListRepos(ctx, a.db)
		if err != nil {
			return proto.Err(err.Error())
		}
		return proto.OK(proto.ReposData{Repos: repos})
	case "repos_add":
		owner, name, ok := splitOwnerRepo(req.Repo)
		if !ok {
			return proto.Err("repos add expects owner/repo")
		}
		if _, err := db.GetRepo(ctx, a.db, owner, name); err == nil {
			return proto.Err("repo already tracked")
		} else if !errors.Is(err, sql.ErrNoRows) {
			return proto.Err(err.Error())
		}
		id, err := db.AddRepo(ctx, a.db, owner, name)
		if err != nil {
			return proto.Err(err.Error())
		}
		if err := a.seedNewRepo(ctx, id, owner, name); err != nil {
			_ = db.RemoveRepo(ctx, a.db, owner, name)
			return proto.Err(err.Error())
		}
		return proto.OK(nil)
	case "repos_remove":
		owner, name, ok := splitOwnerRepo(req.Repo)
		if !ok {
			return proto.Err("repos remove expects owner/repo")
		}
		if err := a.removeRepoDisk(ctx, owner, name); err != nil {
			return proto.Err(err.Error())
		}
		if err := db.RemoveRepo(ctx, a.db, owner, name); err != nil {
			return proto.Err(err.Error())
		}
		return proto.OK(nil)
	case "builds_list":
		builds, err := db.ListBuilds(ctx, a.db, req.Repo, req.Limit)
		if err != nil {
			return proto.Err(err.Error())
		}
		return proto.OK(proto.BuildsData{Builds: builds})
	case "logs_get":
		if req.BuildID == "" {
			return proto.Err("missing build_id")
		}
		id, err := db.ResolveLogsTarget(ctx, a.db, req.BuildID)
		if err != nil {
			return proto.Err(err.Error())
		}
		p, err := db.BuildLogPath(ctx, a.db, id)
		if err != nil {
			return proto.Err("unknown build")
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return proto.Err(err.Error())
		}
		return proto.OK(proto.LogsData{Path: p, Text: string(b)})
	case "cancel":
		if req.BuildID == "" {
			return proto.Err("missing build_id")
		}
		id, err := db.ResolveCancelTarget(ctx, a.db, req.BuildID)
		if err != nil {
			return proto.Err(err.Error())
		}
		b, err := db.GetBuild(ctx, a.db, id)
		if err != nil {
			return proto.Err("unknown build")
		}
		if b.State != types.BuildRunning {
			return proto.Err("build is not running")
		}
		if !a.requestCancel(id) {
			return proto.Err("unable to cancel (not running)")
		}
		return proto.OK(proto.CancelData{BuildID: id, State: string(types.BuildCancelled)})
	case "status":
		a.runMu.Lock()
		running := a.running
		a.runMu.Unlock()
		cfg := a.store.Get()
		return proto.OK(types.DaemonStatus{
			OK:            true,
			QueueDepth:    len(a.queue),
			RunningBuilds: running,
			MaxConcurrent: a.maxConcurrent(),
			PollInterval:  cfg.PollInterval.String(),
			DataDir:       a.dataDir,
		})
	case "secret_set":
		owner, name, ok := splitOwnerRepo(strings.TrimSpace(req.Repo))
		if !ok {
			return proto.Err("missing or invalid repo (expected owner/repo)")
		}
		key := strings.TrimSpace(req.SecretKey)
		if err := db.ValidateSecretKey(key); err != nil {
			return proto.Err(err.Error())
		}
		r, err := db.GetRepo(ctx, a.db, owner, name)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return proto.Err("unknown repo; add it with `shitty-ci repos add` first")
			}
			return proto.Err(err.Error())
		}
		if err := db.UpsertRepoSecret(ctx, a.db, r.ID, key, req.SecretValue); err != nil {
			return proto.Err(err.Error())
		}
		return proto.OK(nil)
	case "secret_unset":
		owner, name, ok := splitOwnerRepo(strings.TrimSpace(req.Repo))
		if !ok {
			return proto.Err("missing or invalid repo (expected owner/repo)")
		}
		key := strings.TrimSpace(req.SecretKey)
		if err := db.ValidateSecretKey(key); err != nil {
			return proto.Err(err.Error())
		}
		r, err := db.GetRepo(ctx, a.db, owner, name)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return proto.Err("unknown repo; add it with `shitty-ci repos add` first")
			}
			return proto.Err(err.Error())
		}
		if err := db.DeleteRepoSecret(ctx, a.db, r.ID, key); err != nil {
			return proto.Err(err.Error())
		}
		return proto.OK(nil)
	case "secret_list":
		owner, name, ok := splitOwnerRepo(strings.TrimSpace(req.Repo))
		if !ok {
			return proto.Err("missing or invalid repo (expected owner/repo)")
		}
		r, err := db.GetRepo(ctx, a.db, owner, name)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return proto.Err("unknown repo; add it with `shitty-ci repos add` first")
			}
			return proto.Err(err.Error())
		}
		keys, err := db.ListRepoSecretKeys(ctx, a.db, r.ID)
		if err != nil {
			return proto.Err(err.Error())
		}
		return proto.OK(proto.SecretKeysData{Keys: keys})
	case "retry":
		if req.BuildID == "" {
			return proto.Err("missing build_id")
		}
		id, err := db.ResolveLogsTarget(ctx, a.db, req.BuildID)
		if err != nil {
			return proto.Err(err.Error())
		}
		b, err := db.GetBuild(ctx, a.db, id)
		if err != nil {
			return proto.Err("unknown build")
		}
		if b.State == types.BuildRunning || b.State == types.BuildPending {
			return proto.Err("build is still running or pending — cancel it first")
		}

		active, err := db.HasActiveBuildForCommit(ctx, a.db, b.RepoID, b.SHA, b.Ref)
		if err != nil {
			return proto.Err(err.Error())
		}
		if active {
			return proto.Err("a build for this commit is already pending or running (the poller may have already queued it)")
		}

		owner, name, ok := splitOwnerRepo(b.Repo)
		if !ok {
			return proto.Err("invalid repo in build record")
		}
		newBuildID, err := newBuildID()
		if err != nil {
			return proto.Err(err.Error())
		}
		logPath := filepath.Join(xdg.LogsDir(a.dataDir), newBuildID+".log")
		_ = os.MkdirAll(filepath.Dir(logPath), 0o755)

		if err := db.CreateBuild(ctx, a.db, newBuildID, b.RepoID, b.SHA, b.Ref, types.BuildPending, logPath); err != nil {
			return proto.Err(err.Error())
		}

		daemonLog.Printf("retry %s/%s ref=%s sha=%s — queued as build %s", owner, name, b.Ref, shortSHA(b.SHA), shortBuildID(newBuildID))

		a.queue <- BuildJob{
			RepoID: b.RepoID,
			Owner:  owner,
			Name:   name,
			SHA:    b.SHA,
			Ref:    b.Ref,
		}

		return proto.OK(map[string]any{"build_id": newBuildID})
	case "config_show":
		cfg := a.store.Get()
		return proto.OK(proto.ConfigView{
			Path:            a.store.Path(),
			PollInterval:    cfg.PollInterval.String(),
			MaxConcurrent:   cfg.MaxConcurrentBuilds,
			BuildTimeout:    cfg.BuildTimeout.String(),
			HasGitHubToken:  cfg.GitHubToken != "",
			DataDirOverride: cfg.DataDir,
			WorkspaceTTL:    cfg.WorkspaceTTL.String(),
			ResolvedDataDir: a.dataDir,
		})
	default:
		return proto.Err("unknown command")
	}
}

func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

func shortBuildID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func ellipsize(s string, max int) string {
	s = strings.TrimSpace(s)
	if s == "" || max <= 0 {
		return s
	}
	if len(s) <= max {
		return s
	}
	if max <= 3 {
		return s[:max]
	}
	return s[:max-3] + "..."
}

func (a *App) printStartupSummary(ctx context.Context) {
	daemonLog.Printf("listening on unix socket %s", a.socketPath)

	repos, err := db.ListRepos(ctx, a.db)
	if err != nil {
		daemonLog.Printf("could not list tracked repos: %v", err)
		return
	}
	if len(repos) == 0 {
		daemonLog.Print("no repositories tracked yet; use `shitty-ci repos add owner/repo`")
		return
	}

	daemonLog.Printf("monitoring %d repo(s):", len(repos))
	for _, r := range repos {
		b, err := db.LastBuildForRepo(ctx, a.db, r.ID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				daemonLog.Printf("  %s/%s  last build: (none)", r.Owner, r.Name)
				continue
			}
			daemonLog.Printf("  %s/%s  last build: (error loading history: %v)", r.Owner, r.Name, err)
			continue
		}

		when := ""
		switch {
		case b.FinishedAt != 0:
			when = ", finished " + time.Unix(b.FinishedAt, 0).Format(time.RFC3339)
		case b.StartedAt != 0:
			when = ", started " + time.Unix(b.StartedAt, 0).Format(time.RFC3339)
		}

		note := ""
		if d := strings.TrimSpace(b.Description); d != "" {
			note = " — " + ellipsize(d, 120)
		} else if s := strings.TrimSpace(b.Step); s != "" {
			note = fmt.Sprintf(" — step %q", s)
		}

		daemonLog.Printf("  %s/%s  last build: %s  ref=%s sha=%s%s%s", r.Owner, r.Name, b.State, b.Ref, shortSHA(b.SHA), when, note)
	}
}

func splitOwnerRepo(s string) (owner, name string, ok bool) {
	i := strings.IndexByte(s, '/')
	if i <= 0 || i == len(s)-1 || strings.Contains(s[i+1:], "/") {
		return "", "", false
	}
	return s[:i], s[i+1:], true
}

func (a *App) seedNewRepo(ctx context.Context, repoID int64, owner, name string) error {
	wsPath, wsRow, err := a.acquireWorkspace(ctx, repoID, owner, name)
	if err != nil {
		return err
	}
	defer func() { _ = a.releaseWorkspace(context.Background(), wsRow) }()
	if _, err := gitutil.RunGitOutput(wsPath, "fetch", "--tags", "--prune"); err != nil {
		return err
	}
	refs, err := listInterestingRefs(wsPath)
	if err != nil {
		return err
	}
	for _, rf := range refs {
		tip, err := refTipSHA(wsPath, rf)
		if err != nil || tip == "" {
			continue
		}
		if err := db.SetRefSHA(ctx, a.db, repoID, rf, tip); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) removeRepoDisk(ctx context.Context, owner, name string) error {
	r, err := db.GetRepo(ctx, a.db, owner, name)
	if err != nil {
		return err
	}
	ws, err := db.ListWorkspaces(ctx, a.db, r.ID)
	if err != nil {
		return err
	}
	root := a.workspaceRoot()
	for _, w := range ws {
		_ = os.RemoveAll(filepath.Join(root, w.DirName))
	}
	return nil
}
