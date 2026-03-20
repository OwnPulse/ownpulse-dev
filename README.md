# ownpulse-dev

Workspace bootstrapper and Claude Code agent definitions for OwnPulse.

## What this does

`opdev` clones the OwnPulse repos and symlinks Claude Code agent definitions into each one. After that, you just `cd` into a repo and run `claude` — the right agents are already available.

Agents that modify code create their own git worktrees automatically, so you can run multiple Claude Code sessions in parallel against the same repo without conflicts.

## Quick start

```bash
# 1. Clone this repo
git clone git@github.com:ownpulse/ownpulse-dev.git
cd ownpulse-dev

# 2. Build and install
make build
sudo make install

# 3. Tell opdev where to find its config (add to ~/.zshrc)
export OPDEV_CONFIG=~/src/ownpulse/ownpulse-dev/config/workspace.toml

# 4. If your repos aren't at ~/dev/ownpulse, create an override:
cp config/workspace.override.toml.example config/workspace.override.toml
# Edit clone_root to point to your checkout directory

# 5. Set up the workspace
opdev setup
```

## Development workflow

```bash
# Start a session — cd into any repo, run claude
cd ~/src/ownpulse/ownpulse
claude

# Invoke an agent (e.g., rust-backend) — it creates a git worktree,
# copies .claude/ into it, and works there in isolation.

# Open another terminal for parallel work
cd ~/src/ownpulse/ownpulse
claude
# Invoke another agent — separate worktree, no conflicts.

# When done, the agent commits on its branch and you can PR it.
```

Each write agent creates a worktree like `ownpulse-1742531200/` next to the main repo. Multiple agents can work on the same repo simultaneously because each gets its own working tree and branch.

Read-only agents (code-review, security-review, principles-guardian) work directly in the repo — no worktree needed.

## Commands

```bash
opdev setup                   # clone all repos, link agents
opdev setup --repos ownpulse  # set up one repo
opdev setup --dry-run         # preview without making changes
opdev list                    # show repos and their agents
opdev teardown                # prune orphan worktrees
opdev teardown --remove-repos # delete repo directories too
```

## Repos

| Repo | Description | Agents |
|---|---|---|
| `ownpulse` | Backend, web frontend, iOS app | rust-backend, react-frontend, swift-ios, code-review, security-review, principles-guardian |
| `ownpulse-infra` | OpenTofu, Helm, Ansible | k8s-infra, security-review |
| `ownpulse-web` | Public marketing site (Astro) | react-frontend, code-review |
| `ownpulse-dev` | This tool and agent definitions | code-review |

## Agents

Agents live in `agents/`. Each `.md` file is a Claude Code agent definition — symlinked into `.claude/agents/` in each repo during setup.

**Write agents** — create worktrees for isolation:

| Agent | Scope |
|---|---|
| `rust-backend` | `backend/` — Axum, sqlx, tokio, migrations, Pact contracts |
| `react-frontend` | `web/` — React, Vite, TypeScript, Playwright, and the Astro public site |
| `swift-ios` | `ios/` — SwiftUI, HealthKit, Maestro flows |
| `k8s-infra` | Helm charts, OpenTofu, GitHub Actions, Ansible |

**Read-only agents** — review without modifying files:

| Agent | Purpose |
|---|---|
| `code-review` | Code quality, correctness, test coverage |
| `security-review` | Security audit — OWASP, auth, crypto, data handling |
| `principles-guardian` | Data cooperative principles compliance |

### Adding an agent

1. Create `agents/<name>.md` with frontmatter (`name`, `description`, `tools`, `model`)
2. Add it to the repo's `agents` list in `config/workspace.toml`
3. Run `opdev setup`

### How agent worktrees work

When a write agent starts, it runs:

```bash
WORKTREE="../$(basename $(pwd))-$(date +%s)"
git worktree add "$WORKTREE" -b work/$(date +%Y%m%d)-<description>
cp -r .claude "$WORKTREE/"
cd "$WORKTREE"
```

This gives it an isolated copy of the repo on its own branch, with all agent definitions available. The main working tree stays clean.

## Configuration

**Base config:** `config/workspace.toml` — committed, defines repos and agent assignments.

**Local override:** `config/workspace.override.toml` — gitignored, deep-merged over the base. Use for:
- Different `clone_root` path
- Private repos (added via `[[repo]]`)
- Repo overrides (e.g., different org or branch via `[[repo_override]]`)

```bash
cp config/workspace.override.toml.example config/workspace.override.toml
```

**Config lookup order:** `--config` flag → `OPDEV_CONFIG` env var → `~/.config/ownpulse/workspace.toml` → `./config/workspace.toml` → `./workspace.toml`

## Prerequisites

- Git with SSH access to the `ownpulse` org
- [Claude Code](https://docs.anthropic.com/en/docs/claude-code) (`npm install -g @anthropic-ai/claude-code`)
- Docker or Go 1.22+ (for building opdev)

## Building from source

```bash
make build      # Docker build (default if no Go on PATH)
make test       # run tests (requires Go)
make lint       # go vet (requires Go)
make release    # cross-compile all platforms
```
