# shitty-ci

<img src="logo.png" alt="logo" />

A small, **local-first** continuous integration helper: one Go binary runs a daemon on your machine, **polls GitHub repos with `git fetch`**, runs builds **natively** (no containers), and posts results back to GitHub as **commit statuses**.

This is made mainly just for me to resolve exactly the problem I wanted solved. It is aimed at a home server or workstation that is not internet-facing enough for webhooks, but where you still want green/red dots on commits and PRs.

## What you get

- **Daemon + CLI** over a Unix socket (local) or TCP (remote machines).
- **SQLite** state (tracked repos, refs, builds, workspace pool).
- **YAML config** with hot reload when the file changes.
- **Per-repo** `.shitty-ci.yml` in the repo (read from the commit being built).
- **Workspace pool** with `git checkout --force` + `git clean -fd` between builds (keeps typical dependency caches that live in ignored dirs).
- **GitHub commit statuses** (`pending` / `success` / `failure` / `error`) via the REST API.

## Requirements

- **Go 1.22+** to build from source.
- **`git`** on `PATH`.
- **SSH access to GitHub** for the user running the daemon (repos are cloned with `git@github.com:owner/repo.git`). The daemon uses your normal environment, including `SSH_AUTH_SOCK` when present.
- **GitHub credentials** (optional, one of the following):
  - **GitHub personal access token** with scope appropriate for posting commit statuses (for example **`repo:status`** on a classic token, or fine-grained access including **Commit statuses: Read and write**). Statuses only, no check runs.
  - **GitHub App** (recommended) for per-step check runs and commit statuses. No public server needed (webhooks are optional and can be disabled). See [GitHub Checks](#github-checks) below.

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
github_token: "" # set to a PAT to enable commit status posting

# Recommended: GitHub App for per-step check runs + commit statuses.
# When configured, takes precedence over github_token.
# github_app:
#   app_id: 123456
#   installation_id: 789012
#   private_key_path: /home/user/.config/shitty-ci/github-app.pem

# Optional:
# data_dir: /custom/parent   # resolved to .../shitty-ci (see below)
# workspace_ttl: 24h        # idle workspace directories pruned after this
# listen: "127.0.0.1:9876"  # TCP address for remote CLI access
```

**Hot reload:** while the daemon is running, edit and save `config.yml`. The daemon notices **mtime** changes within a couple of seconds and picks up `poll_interval`, `max_concurrent_builds`, `build_timeout`, `github_token`, `github_app`, `data_dir`, and `workspace_ttl` without a restart. The `listen` field requires a daemon restart.

**Data directory:** by default everything lives under:

- `$XDG_DATA_HOME/shitty-ci/`  
  (typically `~/.local/share/shitty-ci/`)

That folder holds `shitty-ci.db`, `server.sock`, `logs/`, and `workspaces/`. If you set `data_dir` in config, the daemon uses `<data_dir>/shitty-ci` as the root (same layout inside).

**CLI ↔ daemon (remote access):** by default every subcommand dials the local Unix socket (`server.sock`). To manage a daemon on another machine, pass `--server host:port` and `--token <token>` (or set `SHITTY_CI_SERVER` / `SHITTY_CI_TOKEN` env vars). The daemon must have `listen` configured in its `config.yml` to accept TCP connections. See [Remote access](#remote-access) below.

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
| `shitty-ci github-app setup` | Interactive setup wizard for GitHub App (per-step check runs) |
| `shitty-ci token` | Print the auth token (local only; for remote CLI setup) |

If the CLI cannot connect to the socket, it prints a short hint to run `shitty-ci server` first instead of a raw errno string.

## Repo build config (`.shitty-ci.yml`)

Add a file at the repository root, versioned with your code:

```yaml
# Optional: restrict which remote branches build (default: all branches)
# Legacy: OR together a list of globs (see glob rules below)
# branches:
#   - main
#   - "release/*"

# Optional: build every branch except a short deny-list (include/exclude)
# branches:
#   include: ["**"]
#   exclude: [main, staging, production]

# Optional: opt in to tag builds (default: no tag builds)
# tags:
#   - "v*"

# tags also supports include/exclude:
# tags:
#   include: ["v*"]
#   exclude: ["v0.*"]

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

- Branch and tag filters use [doublestar](https://github.com/bmatcuk/doublestar) globs: `*` matches within one `/`-separated segment, and `**` matches across segments (so `**` is the usual “any branch name” pattern when you use slashes in branch names).
- Omit `branches:` to build **all** remote branches. A legacy empty list (`branches: []`) is treated the same way. Omit `tags:` (or use `tags: []`) to skip tag builds.
- `branches:` / `tags:` may instead be a mapping with required `include:` and optional `exclude:` lists. A ref matches when it matches **any** glob in `include` and matches **none** of the `exclude` globs.
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

## GitHub integration

shitty-ci can report build results to GitHub in two ways:

1. **Commit statuses** — a single `pending/success/failure/error` dot on the commit.
2. **Check runs** (requires a GitHub App) — per-step expandable results in the Checks tab, with log tails, file annotations, and richer detail.

### Setup

**Option A: Personal access token (statuses only)**

Set `github_token` in `config.yml` to a PAT with `repo:status` scope (or `Commit statuses: Read and write` for fine-grained tokens). The daemon posts to:

`POST /repos/{owner}/{repo}/statuses/{sha}`

Context string: `continuous-integration/shitty-ci`.

**Option B: GitHub App (per-step check runs + statuses, recommended)**

A GitHub App lets the Checks API create **one check run per build step**, each with its own expandable entry in the Checks tab including log tail output. The same token also posts commit statuses alongside check runs.

No public server needed — GitHub Apps do **not** require webhooks for shitty-ci's polling-based workflow.

#### Creating the GitHub App

Use the interactive setup wizard:

```bash
shitty-ci github-app setup
```

It will guide you through:
1. Creating the app via a pre-filled manifest URL (open it in any browser)
2. Pasting the temporary code back into the terminal
3. Installing the app on your account or org

The wizard automatically saves the private key, writes the config, and verifies the token exchange.

To create the app manually instead:

1. Go to **GitHub Settings → Developer settings → GitHub Apps → New GitHub App**.
2. Fill in:
   - **GitHub App name**: `shitty-ci` (or whatever you like — this appears on PRs).
   - **Homepage URL**: `https://github.com` (required, never visited).
   - **Webhook**: **Uncheck "Active"** — shitty-ci polls git directly, no webhooks needed.
3. Under **Repository permissions**, set **Checks: Read & write**. The app will also inherit **Commit statuses: Read & write** automatically.
4. Click **Create GitHub App**.
5. **Generate a private key** (a `.pem` file downloads). Place it somewhere persistent, e.g. `~/.config/shitty-ci/github-app.pem`.
6. **Install the app**: on the app page, go to **Install App** → install on your user or org. After installing, the URL will contain the installation ID (the number at the end).

#### Configuring shitty-ci

```yaml
github_app:
  app_id: 123456
  installation_id: 789012
  private_key_path: /home/you/.config/shitty-ci/github-app.pem
```

When `github_app` is configured, it takes precedence over `github_token`. You can remove `github_token` entirely — the installation token handles both check runs and commit statuses.

### Status and check run mapping

| Internal outcome | Commit status | Check run conclusion |
|------------------|---------------|----------------------|
| Running | `pending` | (in progress) |
| Success | `success` | `success` |
| Step failure | `failure` | `failure` with log tail |
| Infrastructure error | `failure` | `failure` with detail |
| Timed out | `error` | `timed_out` |
| Cancelled | `error` | `cancelled` |
| Interrupted | `error` | `neutral` |

### Failure details

Commit status descriptions are capped at **140 characters** by GitHub. They include a one-line error snippet and a `shitty-ci logs <build-id>` hint.

Check run output supports up to **64 KB of Markdown** — the tail of the build log is included automatically, along with structured output (step name, error text, build ID).

## Remote access

The daemon can accept CLI connections over TCP in addition to the local Unix socket.

1. **Configure** `listen` in `config.yml` (requires daemon restart):

   ```yaml
   listen: "127.0.0.1:9876"   # listen only on loopback
   listen: "0.0.0.0:9876"      # listen on all interfaces
   ```

2. **Start (or restart) the daemon.** On first start with `listen` set, the daemon generates a 64-hex-char auth token and writes it to `<data-dir>/auth_token`.

3. **Retrieve the token** (on the daemon machine):

   ```bash
   shitty-ci token
   ```

4. **Use the CLI remotely:**

   ```bash
   shitty-ci --server daemon.local:9876 --token a1b2...c3d4 status
   ```

   Or via environment variables:

   ```bash
   export SHITTY_CI_SERVER=daemon.local:9876
   export SHITTY_CI_TOKEN=a1b2...c3d4
   shitty-ci status
   ```

**Auth:** Token is validated on TCP connections using constant-time comparison. Unix socket connections skip token validation (preserving local trust).

**TLS:** Not yet supported. TCP connections are unencrypted — use a VPN, WireGuard, SSH tunnel, or bind to loopback (default suggestion) for non-LAN paths.

## Security notes

- Treat `config.yml` as a **secret** if it contains a PAT.
- Treat `<data-dir>/auth_token` as a **secret** — it authorizes full access to the daemon over TCP.
- The daemon runs **shell commands from your repo’s YAML** on the host. Only track repositories you trust, same as any self-hosted runner.
