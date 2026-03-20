# ownpulse-dev

Sets up the OwnPulse development workspace: clones repos, creates git worktrees, and symlinks Claude Code agent definitions.

## Install

```bash
git clone git@github.com:ownpulse/ownpulse-dev.git
cd ownpulse-dev
make build            # requires Docker or Go 1.22+
sudo make install     # installs to /usr/local/bin
```

Add to `~/.zshrc` so `opdev` finds its config from anywhere:

```bash
export OPDEV_CONFIG=~/src/ownpulse/ownpulse-dev/config/workspace.toml
```

## Usage

```bash
# Set up the workspace — clones all repos, creates worktrees, links agents
opdev setup

# Set up one repo
opdev setup --repos ownpulse

# See what's configured
opdev list

# Preview without making changes
opdev setup --dry-run

# Remove worktrees
opdev teardown

# Remove everything including repo dirs
opdev teardown --remove-repos
```

After setup, `cd` into any repo or worktree and run `claude` — the agents are already linked.

```bash
cd ~/src/ownpulse/ownpulse          # main repo
cd ~/src/ownpulse/ownpulse-backend  # backend worktree
claude                               # agents are ready
```

## What it does

1. **Clones repos** from GitHub into `clone_root` (configured in `workspace.toml`)
2. **Creates git worktrees** — e.g., `ownpulse-backend`, `ownpulse-web`, `ownpulse-ios`
3. **Symlinks agent definitions** from `agents/` into each repo's `.claude/agents/` directory

## Agent definitions

| Agent | Purpose | Access |
|---|---|---|
| `rust-backend` | Axum/sqlx backend development | Read, Write, Edit, Bash |
| `react-frontend` | React/Vite frontend and Astro site | Read, Write, Edit, Bash |
| `swift-ios` | SwiftUI and HealthKit integration | Read, Write, Edit, Bash |
| `k8s-infra` | Helm, OpenTofu, GitHub Actions | Read, Write, Edit, Bash |
| `security-review` | Security audit (read-only) | Read, Glob, Grep |
| `code-review` | Code quality review (read-only) | Read, Glob, Grep |
| `principles-guardian` | Data cooperative principles audit (read-only) | Read, Glob, Grep |

To add an agent: create a `.md` file in `agents/`, add it to the `agents` list in `workspace.toml`, run `opdev setup`.

## Configuration

Base config: `config/workspace.toml`. For local overrides (e.g., different `clone_root`):

```bash
cp config/workspace.override.toml.example config/workspace.override.toml
```

## Development

```bash
make build      # build
make test       # run tests
make lint       # go vet + golangci-lint
```
