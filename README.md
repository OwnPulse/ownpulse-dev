# ownpulse-dev

Developer workspace tool for OwnPulse. Bootstraps repos, creates git worktrees, links Claude Code agent definitions, and manages isolated Claude Code sessions.

## Install

```bash
git clone git@github.com:ownpulse/ownpulse-dev.git
cd ownpulse-dev

# Option 1: install to $GOPATH/bin (recommended)
make install

# Option 2: build locally
make build    # produces ./opdev
```

Requires Go 1.22+, Git, and [Claude Code](https://docs.anthropic.com/en/docs/claude-code) (`npm install -g @anthropic-ai/claude-code`).

### Install from GitHub releases

Download a prebuilt binary from the [releases page](https://github.com/ownpulse/ownpulse-dev/releases):

```bash
# macOS Apple Silicon
curl -L https://github.com/ownpulse/ownpulse-dev/releases/latest/download/opdev-darwin-arm64 -o opdev
chmod +x opdev
mv opdev /usr/local/bin/
```

## Configuration

Add this to your `~/.zshrc` (or `~/.bashrc`) so `opdev` finds its config from anywhere:

```bash
export OPDEV_CONFIG=~/src/ownpulse/ownpulse-dev/config/workspace.toml
```

Without this, `opdev` looks for the config in these locations (in order):

1. `--config` flag
2. `OPDEV_CONFIG` environment variable
3. `~/.config/ownpulse/workspace.toml`
4. `./config/workspace.toml` (relative to cwd)
5. `./workspace.toml` (relative to cwd)

### Local overrides

For private repos or local settings, create a `workspace.override.toml` next to the base config:

```bash
cp config/workspace.override.toml.example config/workspace.override.toml
```

The override file is deep-merged over the base — it can add repos, override repo settings, and set environment variables. Common use: override `clone_root` to match your local checkout path.

### Adding a repo

Add a `[[repo]]` block to `workspace.toml` (or to your override file for private repos):

```toml
[[repo]]
name = "ownpulse-newservice"
description = "What this repo does"
visibility = "private"
agents = ["rust-backend", "security-review"]
worktrees = ["feature-a"]
```

Then run `opdev setup --repos ownpulse-newservice`.

### Environment variables

Variables defined in `[env]` are injected into every Claude Code session:

```toml
[env]
OWNPULSE_ENV = "development"
```

## Getting started

```bash
# Set up the full workspace — clones all repos, creates worktrees, links agents
opdev setup

# Launch a cross-repo Claude Code session with all agents
opdev session

# Launch a session scoped to one repo (creates an isolated worktree)
opdev session ownpulse

# Session branching from a named worktree
opdev session ownpulse --worktree backend

# Skip Claude Code permission prompts
opdev session --dangerously-skip-permissions

# See what's running
opdev status

# Clean up dead sessions and their worktrees
opdev cleanup
```

## How sessions work

**Workspace sessions** (`opdev session` with no repo): launches Claude Code in the workspace root directory (`clone_root`) with all agents from all repos linked. Use this for cross-cutting features that span multiple repos.

**Repo sessions** (`opdev session <repo>`): creates a new git worktree with a unique branch (`session/<id>`) and launches Claude Code in it. This means:

- Multiple sessions can run against the same repo in parallel without conflicts.
- Each session gets its own copy of the working tree, so agents can freely modify files.
- Agent definitions are automatically symlinked into each session worktree.
- When a session's Claude Code process exits, `opdev cleanup` removes the worktree and branch.

## Commands

| Command | What it does |
|---|---|
| `opdev setup` | Clone repos, create worktrees, link agent definitions |
| `opdev session [repo]` | Launch Claude Code — workspace-wide or in an isolated repo worktree |
| `opdev status` | Show tracked sessions and whether they're running |
| `opdev cleanup` | Remove stopped sessions and their worktrees |
| `opdev list` | List repos and agents from the merged workspace config |
| `opdev teardown` | Remove worktrees (and optionally repos) |

### Flags

```bash
# Global
opdev --config /path/to/workspace.toml <cmd>   # explicit config path
opdev --overlay /path/to/override.toml <cmd>    # explicit overlay path
opdev --dry-run <cmd>                           # preview without changes

# setup
opdev setup --repos ownpulse          # set up one repo only
opdev setup --local                   # skip Docker, use local toolchain

# session
opdev session ownpulse --worktree backend           # branch from worktree/backend
opdev session ownpulse --teams                      # enable agent teams mode
opdev session --dangerously-skip-permissions         # skip permission prompts

# teardown
opdev teardown --kill-sessions              # kill sessions before tearing down
opdev teardown --remove-repos               # also delete cloned repo dirs
```

## Agent definitions

Agent definitions live in `agents/`. Each `.md` file is a Claude Code agent — symlinked into `.claude/agents/` in repos during setup and session creation.

| Agent | Purpose | Access |
|---|---|---|
| `rust-backend` | Axum/sqlx backend development | Read, Write, Edit, Bash |
| `react-frontend` | React/Vite frontend and Astro site | Read, Write, Edit, Bash |
| `swift-ios` | SwiftUI and HealthKit integration | Read, Write, Edit, Bash |
| `k8s-infra` | Helm, OpenTofu, GitHub Actions | Read, Write, Edit, Bash |
| `security-review` | Security audit (read-only) | Read, Glob, Grep |
| `code-review` | Code quality review (read-only) | Read, Glob, Grep |
| `principles-guardian` | Data cooperative principles audit (read-only) | Read, Glob, Grep |

To add an agent: create a `.md` file in `agents/`, add it to the `agents` list in the relevant `[[repo]]` block, and re-run `opdev setup`.

## Development

```bash
make build      # build
make test       # run tests
make lint       # go vet + golangci-lint
make release    # cross-compile for all platforms
```
