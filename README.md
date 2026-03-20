# ownpulse-dev

Developer workspace tool for OwnPulse. Bootstraps repos, creates git worktrees, links Claude Code agent definitions, and manages isolated Claude Code sessions.

## Install

```bash
git clone git@github.com:ownpulse/ownpulse-dev.git
cd ownpulse-dev
make install    # builds and copies to $GOPATH/bin
```

Or build locally:

```bash
make build      # produces ./opdev
```

Requires Go 1.22+, Git, and [Claude Code](https://docs.anthropic.com/en/docs/claude-code) (`npm install -g @anthropic-ai/claude-code`).

## Getting started

```bash
# Set up the full workspace — clones all repos, creates worktrees, links agents
opdev setup

# Spawn a Claude Code session (creates an isolated worktree)
opdev session ownpulse

# Spawn another session for the same repo — they don't conflict
opdev session ownpulse

# Session branching from a named worktree
opdev session ownpulse --worktree backend

# See what's running
opdev status

# Clean up dead sessions and their worktrees
opdev cleanup
```

## How sessions work

Every `opdev session` creates a **new git worktree** with a unique branch (`session/<id>`). This means:

- Multiple sessions can run against the same repo in parallel without conflicts.
- Each session gets its own copy of the working tree, so agents can freely modify files.
- Agent definitions are automatically symlinked into each session worktree.
- When a session's Claude Code process exits, `opdev cleanup` removes the worktree and branch.

The session worktree directories are named `<repo>-session-<id>` (or `<repo>-<worktree>-session-<id>` when using `--worktree`).

## Commands

| Command | What it does |
|---|---|
| `opdev setup` | Clone repos, create worktrees, link agent definitions |
| `opdev session <repo>` | Create an isolated worktree and launch Claude Code in it |
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
opdev session ownpulse --worktree backend   # branch from worktree/backend
opdev session ownpulse --teams              # enable agent teams mode

# teardown
opdev teardown --kill-sessions              # kill sessions before tearing down
opdev teardown --remove-repos               # also delete cloned repo dirs
```

## Configuration

Config lives in `config/workspace.toml`. For private repos or local overrides, create `config/workspace.override.toml` (gitignored):

```bash
cp config/workspace.override.toml.example config/workspace.override.toml
```

The override file is deep-merged over the base — it can add repos, override repo settings, and set environment variables.

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
MY_API_KEY = "..."
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
