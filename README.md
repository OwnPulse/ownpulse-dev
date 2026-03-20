# ownpulse-dev

Sets up the OwnPulse development workspace and provides Claude Code agent definitions.

## Install

```bash
git clone git@github.com:ownpulse/ownpulse-dev.git
cd ownpulse-dev
make build            # requires Docker or Go 1.22+
sudo make install     # installs to /usr/local/bin
```

Add to `~/.zshrc`:

```bash
export OPDEV_CONFIG=~/src/ownpulse/ownpulse-dev/config/workspace.toml
```

## Usage

```bash
opdev setup               # clone all repos, link agents
opdev setup --repos ownpulse  # one repo
opdev list                # show config
opdev teardown            # clean up
```

After setup, `cd` into any repo and run `claude`:

```bash
cd ~/src/ownpulse/ownpulse
claude
```

Agents are symlinked and ready. Write agents automatically create git worktrees for isolation — multiple sessions can run in parallel without conflicts.

## How it works

1. **`opdev setup`** clones repos from GitHub and symlinks agent `.md` files into each repo's `.claude/agents/`
2. You run `claude` in any repo
3. When you invoke a write agent (e.g., `rust-backend`), it creates a git worktree, copies `.claude/` into it, and works there in isolation
4. Read-only agents (`code-review`, `security-review`) work directly — no worktree needed

## Agents

| Agent | Purpose | Isolation |
|---|---|---|
| `rust-backend` | Axum/sqlx backend | creates worktree |
| `react-frontend` | React/Vite frontend, Astro site | creates worktree |
| `swift-ios` | SwiftUI, HealthKit | creates worktree |
| `k8s-infra` | Helm, OpenTofu, GitHub Actions | creates worktree |
| `security-review` | Security audit | read-only |
| `code-review` | Code quality review | read-only |
| `principles-guardian` | Data cooperative principles | read-only |

## Configuration

Base config: `config/workspace.toml`. For local overrides (e.g., different `clone_root`):

```bash
cp config/workspace.override.toml.example config/workspace.override.toml
```

### Adding a repo

```toml
[[repo]]
name = "my-repo"
description = "What it does"
visibility = "private"
agents = ["rust-backend", "security-review"]
```

### Adding an agent

Create `agents/<name>.md`, add it to the repo's `agents` list, run `opdev setup`.
