# shitty-ci

<img src="logo.png" alt="logo" />

A small, **local-first** continuous integration helper: one Go binary runs a daemon on your machine, **polls GitHub repos with `git fetch`**, runs builds **natively** (no containers), and posts results back to GitHub as **commit statuses**.

This is made mainly just for me to resolve exactly the problem I wanted solved. It is aimed at a home server or workstation that is not internet-facing enough for webhooks, but where you still want green/red dots on commits and PRs.

## What you get

- **Daemon + CLI** over a Unix socket (`server.sock` under your data directory).
- **SQLite** state (tracked repos, refs, builds, workspace pool).
- **YAML config** with hot reload when the file changes.
- **Per-repo** `.shitty-ci.yml` in the repo (read from the commit being built).
- **Workspace pool** with `git checkout --force` + `git clean -fd` between builds (keeps typical dependency caches that live in ignored dirs).
- **GitHub commit statuses** (`pending` / `success` / `failure` / `error`) via the REST API.

## Requirements

- **Go 1.22+** to build from source.
- **`git`** on `PATH`.
- **SSH access to GitHub** for the user running the daemon (repos are cloned with `git@github.com:owner/repo.git`). The daemon uses your normal environment, including `SSH_AUTH_SOCK` when present.
- **GitHub personal access token** (optional but needed for status posting) with scope appropriate for posting statuses (for example **`repo:status`** on a classic token, or fine-grained access including **Commit statuses: Read and write**).

## Build and install

From the repository root:

```bash
go build -o shitty-ci ./cmd/shitty-ci
```

Put the binary somewhere on your `PATH`, or run it via an absolute path.

## Quick start

1. **Start the daemon** (foreground; stop with Ctrl+C):

   ```bash
   shitty-ci server
   ```

2. In another terminal, **track a repo**:

   ```bash
   shitty-ci repos add owner/repo
   ```

   The first add **clones** the repo and **records current SHAs** for branches/tags it sees. It does **not** enqueue a build for that snapshot; the next **new** commit discovered by polling will enqueue a build (if `.shitty-ci.yml` exists and branch/tag rules allow it).

3. **Inspect activity**:

   ```bash
   shitty-ci status
   shitty-ci builds --limit 20
   shitty-ci logs <build-id>
   ```

## Configuration

Default config path:

- `$XDG_CONFIG_HOME/shitty-ci/config.yml`  
  (usually `~/.config/shitty-ci/config.yml`)

If the file is missing, built-in defaults apply. Example:

```yaml
poll_interval: 30s
max_concurrent_builds: 4
build_timeout: 30m
github_token: "" # set to a PAT to enable status posting

# Optional:
# data_dir: /custom/parent   # resolved to .../shitty-ci (see below)
# workspace_ttl: 24h        # idle workspace directories pruned after this
```

**Hot reload:** while the daemon is running, edit and save `config.yml`. The daemon notices **mtime** changes within a couple of seconds and picks up `poll_interval`, `max_concurrent_builds`, `build_timeout`, `github_token`, `data_dir`, and `workspace_ttl` without a restart.

**Data directory:** by default everything lives under:

- `$XDG_DATA_HOME/shitty-ci/`  
  (typically `~/.local/share/shitty-ci/`)

That folder holds `shitty-ci.db`, `server.sock`, `logs/`, and `workspaces/`. If you set `data_dir` in config, the daemon uses `<data_dir>/shitty-ci` as the root (same layout inside).

**CLI ↔ daemon (local only):** every subcommand except `shitty-ci server` dials `server.sock` on the **same machine** using the same resolved data directory as above. There is **no** configurable remote host, URL, or TCP listener for the control plane; run the CLI where the daemon runs (or use something like SSH to a shell on that host). GitHub status posting is separate: that goes to GitHub’s HTTP API when `github_token` is set. A **possible future** extension is the same JSON control protocol over TCP or TLS with explicit authentication, but that is not implemented today.

## CLI reference

| Command | Purpose |
|--------|---------|
| `shitty-ci server` | Run the daemon |
| `shitty-ci server --install-systemd` | Write a **user** systemd unit under `~/.config/systemd/user/` and print next steps |
| `shitty-ci repos add <owner/repo>` | Track a repo (clone + seed SHAs, no initial build) |
| `shitty-ci repos remove <owner/repo>` | Stop tracking; remove workspace dirs for that repo |
| `shitty-ci repos list` | List tracked repos |
| `shitty-ci builds [--repo owner/repo] [--limit N]` | Recent builds |
| `shitty-ci logs <build-id>` | Print the log file for a build |
| `shitty-ci cancel <build-id>` | **SIGKILL** the running build’s process group |
| `shitty-ci status` | Queue depth, running builds, limits, poll interval, data dir |
| `shitty-ci config` | Show effective settings (token presence is boolean only) |

If the CLI cannot connect to the socket, it prints a short hint to run `shitty-ci server` first instead of a raw errno string.

## Repo build config (`.shitty-ci.yml`)

Add a file at the repository root, versioned with your code:

```yaml
# Optional: restrict which remote branches build (default: all branches)
# branches:
#   - main
#   - "release/*"

# Optional: opt in to tag builds (default: no tag builds)
# tags:
#   - "v*"

steps:
  - name: Build
    run: go build ./...
  - name: Test
    run: go test ./... -count=1

# Optional: env for every step
# env:
#   CGO_ENABLED: "0"

# Optional: per-repo file timeout override
# build_timeout: 45m
```

**Semantics:**

- Steps run **sequentially** via `sh -c "<run>"` in the workspace directory.
- Each step process also receives **`SHITTY_CI_REF`** (full git ref for this build, for example `refs/remotes/origin/main`), **`SHITTY_CI_SHA`** (the checked-out commit), and **`SHITTY_CI_REPO`** (`owner/name`). These are appended **after** the optional `env:` map so they always reflect the actual queued build (even if the YAML reused those names).
- First non-zero exit marks the build failed; later steps are skipped.
- If `.shitty-ci.yml` is **missing** for a commit, the poller **advances the ref** without building and **does not** post a status. If a build was already scheduled and the file vanished (rare), the daemon records a skip in the local build history and still does not post to GitHub.

## systemd (user service)

`--install-systemd` writes `shitty-ci.service` next to your other user units. Typical activation:

```bash
systemctl --user daemon-reload
systemctl --user enable --now shitty-ci.service
```

A reference unit is also kept in this repo at `contrib/shitty-ci.service` (paths may differ from the generated unit, which uses your current binary path).

## GitHub statuses

When `github_token` is set, the daemon posts to:

`POST /repos/{owner}/{repo}/statuses/{sha}`

Rough mapping:

| Internal outcome | GitHub `state` | Typical description |
|------------------|----------------|---------------------|
| Running | `pending` | Build in progress |
| Success | `success` | All steps passed |
| Failure | `failure` | Step failed / misconfiguration |
| Timed out | `error` | Includes configured timeout |
| Cancelled | `error` | Build cancelled by user |

Context string used on GitHub: `continuous-integration/shitty-ci`.

Failure descriptions are **short** (GitHub caps them at **140 characters**), but they now include a **one-line error snippet** when available, plus a **`shitty-ci logs <build-id>`** hint so you can pull the full stdout/stderr from the daemon host. The complete trace is always in `<data-dir>/logs/<build-id>.log`.

## Security notes

- Treat `config.yml` as a **secret** if it contains a PAT.
- The daemon runs **shell commands from your repo’s YAML** on the host. Only track repositories you trust, same as any self-hosted runner.

## Design doc

See `docs/plan.html` for the full design rationale and behavior notes.
