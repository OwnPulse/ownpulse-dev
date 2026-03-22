# ownpulse-dev

Workspace bootstrapper and Claude Code agent definitions for OwnPulse.

## What this does

`opdev` clones the OwnPulse repos, symlinks Claude Code agent definitions into each one, and provides session isolation so you can run multiple Claude Code sessions in parallel without conflicts.

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
# Start an isolated session — creates a worktree and launches Claude Code
cd ~/src/ownpulse/ownpulse
opdev session backend-auth

# In another terminal — parallel session, separate worktree, no conflicts
cd ~/src/ownpulse/ownpulse
opdev session frontend-auth

# Infra work (different repo)
cd ~/src/ownpulse/ownpulse-infra
opdev session helm-auth

# When done, clean up stale worktrees
opdev clean --repo ownpulse
```

Each `opdev session` creates a git worktree at `worktrees/<repo>-<name>` with branch `work/<name>`, then launches `claude --dangerously-skip-permissions` in it. Multiple sessions can work on the same repo simultaneously because each gets its own working tree and branch.

Within each session, the lead Claude instance can spawn subagents with `isolation: "worktree"` for further isolation — for example, a backend agent and frontend agent working in parallel on a cross-cutting feature.

Read-only agents (code-review, security-review, principles-guardian) work directly in the repo — no worktree needed.

## Commands

```bash
# Workspace setup
opdev setup                          # clone all repos, link agents, generate CLAUDE.md
opdev setup --repos ownpulse         # set up one repo
opdev setup --dry-run                # preview without making changes
opdev list                           # show repos and their agents

# Sessions
opdev session backend-auth           # create worktree + launch claude
opdev session --repo ownpulse foo    # explicit repo (default: detect from cwd)
opdev session                        # random session name
opdev session --safe backend-auth    # launch without --dangerously-skip-permissions
opdev session --no-launch foo        # create worktree only, don't launch claude
opdev session --dry-run foo          # preview what would happen

# Cleanup
opdev clean                          # prune stale worktrees (detect repo from cwd)
opdev clean --repo ownpulse          # prune for a specific repo
opdev teardown                       # prune orphan worktrees across all repos
opdev teardown --remove-repos        # delete repo directories too
```

## Repos

| Repo | Description | Agents |
|---|---|---|
| `ownpulse` | Backend, web frontend, iOS app | rust-backend, react-frontend, swift-ios, code-review, security-review, principles-guardian |
| `ownpulse-infra` | OpenTofu, Helm, Ansible | k8s-infra, security-review |
| `ownpulse-web` | Public marketing site (Astro) | astro-web, code-review |
| `ownpulse-dev` | This tool and agent definitions | code-review |

## Agents

Agents live in `agents/`. Each `.md` file is a Claude Code agent definition — symlinked into `.claude/agents/` in each repo during setup.

**Write agents** — spawned with `isolation: "worktree"` by the lead session:

| Agent | Scope |
|---|---|
| `rust-backend` | `backend/` — Axum, sqlx, tokio, migrations, Pact contracts |
| `react-frontend` | `web/` — React, Vite, TypeScript, Playwright |
| `swift-ios` | `ios/` — SwiftUI, HealthKit, Maestro flows |
| `k8s-infra` | Helm charts, OpenTofu, GitHub Actions, Ansible |
| `astro-web` | Public marketing site (Astro) |

**Read-only agents** — review without modifying files:

| Agent | Purpose |
|---|---|
| `code-review` | Code quality, correctness, test coverage |
| `security-review` | Security audit — OWASP, auth, crypto, data handling |
| `principles-guardian` | Data cooperative principles compliance |

### Adding an agent

1. Create `agents/<name>.md` with frontmatter (`name`, `description`, `tools`)
2. Add it to the repo's `agents` list in `config/workspace.toml`
3. Run `opdev setup`

### How isolation works

**Session isolation:** `opdev session` creates a git worktree per top-level Claude Code session. This means you can run 3-5 sessions simultaneously against the same repo — each has its own working tree and branch.

**Subagent isolation:** Within a session, the lead Claude instance spawns write agents with `isolation: "worktree"`. Claude Code handles worktree creation and cleanup automatically — agents do not manage worktrees themselves.

```
repo (main checkout — stays clean)
├── worktrees/
│   ├── repo-backend-auth/      ← opdev session backend-auth
│   │   └── (claude spawns subagents with isolation: "worktree")
│   ├── repo-frontend-auth/     ← opdev session frontend-auth
│   └── repo-helm-fix/          ← opdev session helm-fix
```

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

## Generated files

`opdev setup` generates `.claude/CLAUDE.md` in each repo. This file contains:
- **Hard rules** (IaC only, no telemetry, secrets in SOPS, etc.) — loaded by every Claude session
- Repo list and CI/CD conventions
- Agent inventory (categorized as write/review)
- Agent team workflow instructions

The template lives in `src/workspace/setup.go` — edit there and re-run `opdev setup`.

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
