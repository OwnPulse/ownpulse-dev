# ADR-0003: Exec-Based Session Isolation with Claude Code Agent Teams

**Date:** 2026-03-21
**Status:** Accepted
**Deciders:** Tony Ferrell
**Supersedes:** ADR-0001 (tmux session management), ADR-0002 (session worktrees)

---

## Context

ADR-0001 introduced tmux as a session multiplexer, and ADR-0002 introduced per-session git worktrees managed by opdev with JSON-based session tracking. This worked but had problems in practice:

- The tmux-based session management was too complex to use day-to-day as a solo developer
- Session state tracking (`sessions.json`) added bookkeeping with little benefit
- Agent definitions contained manual worktree creation scripts that conflicted with Claude Code's built-in `isolation: "worktree"` feature
- Agents weren't consistently following project guidance (e.g., running infra manually instead of using IaC) because hard rules were buried in agent definitions rather than in `.claude/CLAUDE.md`

Meanwhile, Claude Code added native agent teams: the ability to spawn specialized subagents with `isolation: "worktree"`, where Claude Code handles worktree creation and cleanup automatically.

---

## Decision

Replace the tmux/session-tracking approach with a simpler two-layer isolation model:

### Layer 1: `opdev session` (top-level isolation)

`opdev session <name>` creates a git worktree and `exec`s into `claude --dangerously-skip-permissions` in that worktree. No tmux, no session tracking, no PID management — the process replaces opdev. Each terminal window is one session.

```
worktrees/
├── ownpulse-backend-auth/     ← terminal 1: opdev session backend-auth
├── ownpulse-frontend-auth/    ← terminal 2: opdev session frontend-auth
└── ownpulse-helm-fix/         ← terminal 3: opdev session helm-fix
```

### Layer 2: Claude Code `isolation: "worktree"` (subagent isolation)

Within each session, the lead Claude instance is the orchestrator. It spawns specialized agents (rust-backend, react-frontend, etc.) using Claude Code's `Agent` tool with `isolation: "worktree"`. Claude Code creates and cleans up subagent worktrees automatically.

Agent definitions no longer contain worktree management scripts. They define only: what you own, what you don't own, non-negotiables, code patterns, and build/test commands.

### Hard rules in `.claude/CLAUDE.md`

`opdev setup` generates `.claude/CLAUDE.md` with a "Hard Rules" section at the top — IaC only, no telemetry, SOPS secrets, self-hosting required, etc. This is loaded by every Claude session before any agent definition, ensuring consistent behavior.

---

## Alternatives Considered

### Keep tmux but simplify

Remove session tracking but keep tmux for window management.

Rejected because tmux adds a dependency and UX layer that isn't needed. Users already have terminal windows/tabs. The core problem was isolation, not multiplexing.

### Let Claude Code handle everything (no opdev session)

Users just `cd repo && claude`, and subagents use `isolation: "worktree"` for all parallelism.

Rejected because this doesn't solve the multi-session problem. If you run 3 `claude` instances in the same directory, the lead sessions still share the working tree and stomp on each other. The lead session needs its own worktree.

---

## Consequences

**Positive:**
- Dramatically simpler — no tmux, no session.json, no PID tracking
- Agent definitions are clean — just expertise and rules, no worktree scripts
- Hard rules are enforced consistently via generated `.claude/CLAUDE.md`
- Two-layer isolation cleanly separates "sessions don't conflict" from "subagents don't conflict"
- `opdev clean` is just `git worktree prune` — no custom cleanup logic

**Negative / tradeoffs:**
- No unified view of all sessions (`opdev status` is gone) — you manage terminals yourself
- `--dangerously-skip-permissions` is the default, which skips permission prompts. Use `--safe` flag to opt back in.
- Worktree cleanup is manual (`opdev clean`) rather than automatic on session exit

---

## References

- Claude Code agent teams: https://docs.anthropic.com/en/docs/claude-code
- git worktree: https://git-scm.com/docs/git-worktree
