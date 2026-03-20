# ownpulse-dev

Developer workspace tool for OwnPulse. Bootstraps repos, creates git worktrees, links Claude Code agent definitions, and manages parallel Claude Code sessions via tmux.

## Install

```bash
git clone git@github.com:ownpulse/ownpulse-dev.git
cd ownpulse-dev

# Build (requires Docker or Go 1.22+)
make build

# Install to /usr/local/bin
sudo make install
```

Prebuilt binaries are available on the [releases page](https://github.com/ownpulse/ownpulse-dev/releases).

### Prerequisites

- Git
- [tmux](https://github.com/tmux/tmux) — sessions run in tmux windows
- [Claude Code](https://docs.anthropic.com/en/docs/claude-code) (`npm install -g @anthropic-ai/claude-code`)
- Docker or Go 1.22+ (for building)

### Configuration

Add to your `~/.zshrc` so `opdev` finds its config from anywhere:

```bash
export OPDEV_CONFIG=~/src/ownpulse/ownpulse-dev/config/workspace.toml
```

Without this, `opdev` checks (in order): `--config` flag, `OPDEV_CONFIG` env var, `~/.config/ownpulse/workspace.toml`, `./config/workspace.toml`, `./workspace.toml`.

For private repos or local overrides, create `config/workspace.override.toml` (gitignored):

```bash
cp config/workspace.override.toml.example config/workspace.override.toml
```

## Getting started

```bash
# Set up the workspace — clones repos, creates worktrees, links agents
opdev setup

# Launch a cross-repo session with all agents (in tmux)
opdev session

# Launch a session scoped to one repo
opdev session ownpulse

# Launch a session from a named worktree branch
opdev session ownpulse --worktree backend

# Skip Claude Code permission prompts
opdev session --dangerously-skip-permissions

# See what's running
opdev status

# Clean up dead sessions and their worktrees
opdev cleanup
```

## How sessions work

Every `opdev session` runs Claude Code in a **tmux window** inside a shared tmux session called `opdev`. This means:

- **Multiple sessions run in parallel** — each in its own tmux window. Switch between them with `Ctrl-b` + window number.
- **Repo sessions get isolated worktrees** — `opdev session <repo>` creates a new git worktree (`session/<id>` branch) so agents can freely modify files without conflicts.
- **Workspace sessions** (`opdev session` with no repo) launch in the workspace root with all agents from all repos linked. Use this for cross-cutting features.
- **Agent definitions are symlinked** into each session automatically.
- **`opdev cleanup`** removes dead sessions and their worktrees/branches.

If you're already inside tmux, `opdev session` creates a new window and switches to it. If you're outside tmux, it creates the session and attaches.

## Commands

| Command | What it does |
|---|---|
| `opdev setup` | Clone repos, create worktrees, link agent definitions |
| `opdev session [repo]` | Launch Claude Code in a tmux window |
| `opdev status` | Show tracked sessions and their status |
| `opdev cleanup` | Remove stopped sessions and their worktrees |
| `opdev list` | List repos and agents from the merged config |
| `opdev teardown` | Remove worktrees (and optionally repos) |

### Flags

```bash
# Global
opdev --config /path/to/workspace.toml <cmd>
opdev --overlay /path/to/override.toml <cmd>
opdev --dry-run <cmd>

# setup
opdev setup --repos ownpulse          # one repo only
opdev setup --local                   # local toolchain, no Docker

# session
opdev session ownpulse --worktree backend           # branch from worktree/backend
opdev session --teams                               # enable agent teams mode
opdev session --dangerously-skip-permissions         # skip permission prompts

# teardown
opdev teardown --kill-sessions        # kill sessions before tearing down
opdev teardown --remove-repos         # also delete cloned repo dirs
```

## Agent definitions

Agent definitions live in `agents/`. Each `.md` file is a Claude Code agent — symlinked into `.claude/agents/` during setup and session creation.

| Agent | Purpose | Access |
|---|---|---|
| `rust-backend` | Axum/sqlx backend development | Read, Write, Edit, Bash |
| `react-frontend` | React/Vite frontend and Astro site | Read, Write, Edit, Bash |
| `swift-ios` | SwiftUI and HealthKit integration | Read, Write, Edit, Bash |
| `k8s-infra` | Helm, OpenTofu, GitHub Actions | Read, Write, Edit, Bash |
| `security-review` | Security audit (read-only) | Read, Glob, Grep |
| `code-review` | Code quality review (read-only) | Read, Glob, Grep |
| `principles-guardian` | Data cooperative principles audit (read-only) | Read, Glob, Grep |
| `session-launcher` | Launch additional Claude Code sessions via opdev | Read, Bash |
| `task-runner` | Autonomous task execution from the task queue | Read, Write, Edit, Bash, Glob, Grep |

To add an agent: create a `.md` file in `agents/`, add it to the `agents` list in the relevant `[[repo]]` block, and re-run `opdev setup`.

### Adding a repo

```toml
[[repo]]
name = "ownpulse-newservice"
description = "What this repo does"
visibility = "private"
agents = ["rust-backend", "security-review"]
worktrees = ["feature-a"]
```

### Environment variables

Variables in `[env]` are injected into every Claude Code session:

```toml
[env]
OWNPULSE_ENV = "development"
```

## Task system

An autonomous task queue for parallel Claude Code work. Reviews and plans generate tasks; sessions pick them up and execute them.

Task state lives in `<clone_root>/.ownpulse-dev/tasks/` (runtime, not committed). Task definitions — the agent and bootstrap prompt — live in this repo under `tasks/` and `agents/task-runner.md`.

See [tasks/README.md](tasks/README.md) for full documentation.

```bash
# In any session, the task-runner agent will automatically:
# 1. Read the task index
# 2. Claim the highest-priority unlocked task
# 3. Execute the plan
# 4. Commit and mark done

# Or bootstrap manually:
claude -p "$(cat ~/src/ownpulse/ownpulse-dev/tasks/PROMPT.md)"
```

## Development

```bash
make build      # build
make test       # run tests
make lint       # go vet + golangci-lint
make release    # cross-compile for all platforms
```
