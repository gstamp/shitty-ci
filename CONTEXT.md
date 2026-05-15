# Shitty CI

A lightweight, local-first CI/CD system tailored to personal needs. Runs on a private home server, polls GitHub repos for changes, executes builds natively (no containers), and reports statuses back to GitHub.

## Language

**Implementation Language**:
Go, chosen for its native concurrency model (goroutines for parallel builds), single-binary deployment, and strong stdlib support for process management and git operations.
_Avoid_: TypeScript, Python, shell scripts for core logic

## Architecture

**Process Model**:
The system ships as a single Go binary with subcommands. `shitty-ci server` starts the long-running daemon (polling, scheduling, build execution). `shitty-ci` with other subcommands communicates with the daemon over a local Unix socket.

**CLI Commands**:
- `shitty-ci server` — start the daemon
- `shitty-ci server --install-systemd` — install a systemd user service for the daemon
- `shitty-ci repos add <owner/repo>` — add a repo to track
- `shitty-ci repos remove <owner/repo>` — stop tracking
- `shitty-ci repos list` — list tracked repos
- `shitty-ci builds [--repo] [--limit]` — list recent builds
- `shitty-ci logs <build-id>` — show build output
- `shitty-ci cancel <build-id>` — cancel a running build
- `shitty-ci status` — daemon health and queue info
- `shitty-ci config` — show current configuration

All CLI commands provide clear, actionable error messages instead of raw connection errors.

## Server Configuration

**Config File** (`~/.config/shitty-ci/config.yml`):
```yaml
poll_interval: 30s
max_concurrent_builds: 4
build_timeout: 30m
github_token: ghp_...
# data_dir: /custom/path    # override for $XDG_DATA_HOME/shitty-ci/
# workspace_ttl: 24h         # idle workspace lifetime before pruning
```
If the file doesn't exist, all values use sensible defaults.

**Config Reload**:
The daemon watches its config file and hot-reloads on change. Poll interval, concurrency limit, and GitHub token take effect without restarting.

**State Storage**:
SQLite (via `modernc.org/sqlite`, pure Go, no CGO) persists tracked repos, last-known SHAs, workspace state, and build history. Stored under `$XDG_DATA_HOME/shitty-ci/` (default `~/.local/share/shitty-ci/`).

**Git Authentication**:
The daemon inherits the user's SSH agent (`SSH_AUTH_SOCK`) for git operations. No additional credential setup required beyond having SSH keys configured for GitHub.

## Runtime Behavior

**Polling Strategy**:
New commits are detected by running `git fetch` periodically against each tracked repo, then comparing the fetched remote ref to the last-known SHA.
_Avoid_: GitHub API polling for commit detection

**Polling Interval**:
Every 30s by default. Configurable globally in `config.yml`.

**Concurrency**:
At most 4 builds run simultaneously across all repos. Builds beyond the limit are queued. Configurable globally.

**Build State Machine**:
`pending` (queued) → `running` → `success` / `failure` / `timed_out` / `cancelled` / `interrupted`

_On daemon startup, any `pending` or `running` rows without a `finished_at` from a prior run are marked `interrupted` and GitHub is updated when a token is configured._

**Build Timeout**:
30 minutes by default. Killed with SIGKILL to the process group. Overridable per-repo in `.shitty-ci.yml`.

**Cancellation**:
`shitty-ci cancel <build-id>` sends SIGKILL to the build's process group immediately. No grace period.

**Build Logs**:
stdout/stderr streamed to `$XDG_DATA_HOME/shitty-ci/logs/<build-id>.log`. The CLI reads and displays these on demand.
_Avoid_: Storing full logs in SQLite

## GitHub Integration

**Status Reporting**:
Build outcomes are reported via the Commit Statuses API (`/repos/{owner}/{repo}/statuses/{sha}`). `success` → green, `failure` → red, `timed_out`/`cancelled`/`interrupted` → error (with description). Authenticated with a Personal Access Token.
_Avoid_: GitHub Check Runs API, GitHub App registration

## Build Model

**Build Execution**:
Builds run natively on the host machine — no Docker containers.
_Avoid_: Docker, containers, VMs for build isolation

**Workspaces**:
Each tracked repo has a dynamically-sized pool of persistent checkout directories. Before a build, the workspace is cleaned via `git checkout --force <sha>` + `git clean -fd` (preserving gitignored dependency caches). Idle workspaces (>24h untouched) are pruned by a background goroutine.
_Avoid_: Fresh `git clone` per build, single workspace per repo that blocks

## Build Configuration

**Config File** (`.shitty-ci.yml` at repo root):
Read from the checked-out commit before executing. If it doesn't exist, the build is skipped (no commit status posted).

**Triggers**:
All branches trigger builds by default, configurable per-repo via `branches` and `tags` patterns in `.shitty-ci.yml`. When a repo is first added, the daemon waits for the next new commit before building.

**Schema** (`.shitty-ci.yml`):
```yaml
# Branches to build (default: all)
# branches:
#   - main

# Tags to build (default: none)
# tags:
#   - "v*"

# Sequential build steps
steps:
  - name: Build
    run: go build ./...

# Environment variables for all steps
# env:
#   GOPROXY: "https://proxy.golang.org,direct"
```
Commands run via `sh -c`. A non-zero exit in any step fails the build. Steps are sequential.
Each step also receives **`SHITTY_CI_REF`**, **`SHITTY_CI_SHA`**, and **`SHITTY_CI_REPO`** (`owner/name`) from the daemon so scripts can branch on the queued ref without guessing from detached `HEAD`.
_Avoid_: Server-side per-repo config files, external configuration services

## Example dialogue

> **Dev:** "I just added a new repo. Will it build the latest commit?"
> **CI expert:** "No — `shitty-ci repos add` registers it but doesn't trigger a build. It waits for the next new commit to arrive via polling. That way you don't get a spurious build of an arbitrary commit before you've configured `.shitty-ci.yml`."
>
> **Dev:** "What happens if the repo doesn't have a `.shitty-ci.yml`?"
> **CI expert:** "The build is skipped silently. No commit status is posted. If you want to see why a repo isn't building, check `shitty-ci builds --repo owner/repo`."
>
> **Dev:** "Two commits pushed to the same repo fast — will they build in parallel?"
> **CI expert:** "Yes, if there's an idle workspace available. Each build gets its own checkout directory. They'll run simultaneously up to the global concurrency limit (default 4)."

## Flagged ambiguities

_(none yet)_
