# ownpulse-dev

Developer workspace tool for OwnPulse. Handles repo checkout, git worktrees, Claude Code agent definitions, and session management.

## Quickstart

```bash
# Clone this repo first — it's the entry point for all OwnPulse development.
git clone git@github.com:ownpulse/ownpulse-dev.git
cd ownpulse-dev

# Build the CLI
go build -o opdev ./src

# Set up the full workspace (clones all repos, creates worktrees, links agents)
./opdev setup

# Or pick specific repos
./opdev setup --repos ownpulse,ownpulse-infra

# See what would happen without making changes
./opdev setup --dry-run
```

## Hosted / private workspace

If you work on the hosted OwnPulse product, create a `config/workspace.override.toml` alongside the base config:

```bash
cp config/workspace.override.toml.example config/workspace.override.toml
# Edit it with your org name, private repos, and extra env vars
# Add it to .gitignore — it's not committed
```

The override file is deep-merged over the base config. It can add repos, override repo settings (org, branch), and add environment variables. See the example file for full documentation.

## Commands

| Command | What it does |
|---|---|
| `opdev setup` | Clone repos, create worktrees, link agent definitions |
| `opdev session <repo>` | Spawn a Claude Code session for a repo or worktree |
| `opdev status` | Show tracked Claude Code sessions and their status |
| `opdev list` | List repos and agents from the merged workspace config |
| `opdev teardown` | Remove worktrees (and optionally repos) |

### Useful flags

```bash
opdev setup --repos ownpulse          # set up one repo only
opdev setup --local                   # use local toolchain instead of Docker
opdev setup --dry-run                 # preview without making changes
opdev session ownpulse --worktree backend         # session in the backend worktree
opdev session ownpulse --teams                    # enable experimental agent teams mode
opdev teardown --kill-sessions --remove-repos     # full reset
opdev --config /path/to/workspace.toml <cmd>      # explicit config path
opdev --overlay /path/to/override.toml <cmd>      # explicit overlay path
```

## Agent definitions

Agent definitions live in `agents/`. Each `.md` file is a Claude Code subagent — it gets symlinked into `.claude/agents/` in the relevant repos during setup.

| Agent | Purpose | Access |
|---|---|---|
| `rust-backend` | Axum/sqlx backend development | Read, Write, Edit, Bash |
| `react-frontend` | React/Vite frontend and Astro public site | Read, Write, Edit, Bash |
| `swift-ios` | SwiftUI app and HealthKit integration | Read, Write, Edit, Bash |
| `k8s-infra` | Helm, OpenTofu, GitHub Actions | Read, Write, Edit, Bash |
| `security-review` | Security audit — read-only | Read, Glob, Grep |
| `code-review` | Code quality review — read-only | Read, Glob, Grep |
| `principles-guardian` | Data cooperative principles audit — read-only | Read, Glob, Grep |

To add an agent: create a new `.md` file in `agents/`, add it to the relevant `agents` list in `config/workspace.toml`, and re-run `opdev setup`.

## Config structure

```
config/
  workspace.toml               # base config (committed)
  workspace.override.toml      # local overrides (gitignored)
  workspace.override.toml.example  # template for override file
```

### Adding a new repo

In `workspace.toml`, add a `[[repo]]` block:

```toml
[[repo]]
name = "ownpulse-newservice"
description = "What this repo does"
visibility = "private"           # or "public"
agents = ["rust-backend", "security-review"]
worktrees = ["feature-a"]        # creates a worktree/<name> branch per entry
```

For hosted/private repos, add them to your `workspace.override.toml` instead so they don't appear in the public config.

## Prerequisites

- Go 1.22+
- Git
- Docker (for `--container` mode, the default)
- Claude Code (`npm install -g @anthropic-ai/claude-code`)
- GitHub SSH access configured for the `ownpulse` org

## Development

```bash
go build -o opdev ./src
go test ./...
go vet ./...
```
