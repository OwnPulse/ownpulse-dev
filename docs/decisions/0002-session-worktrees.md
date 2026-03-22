# ADR-0002: Isolated Git Worktrees per Claude Code Session

**Date:** 2026-03-19
**Status:** Superseded by ADR-0003 (exec-based sessions)
**Deciders:** Tony Ferrell

---

## Context

When running multiple Claude Code agents against the same repo in parallel, they conflict if they share a working directory. Two agents editing the same files, running different builds, or checking out different branches will produce unpredictable results.

We need session isolation so that each Claude Code agent gets its own copy of the repo's working tree and can freely modify files without affecting other sessions.

---

## Decision

Each `opdev session <repo>` creates a **new git worktree** with a unique branch (`session/<8-char-hex-id>`). The worktree directory is named `<repo>-session-<id>` (or `<repo>-<worktree>-session-<id>` when branching from a named worktree).

Agent definitions are symlinked into each session worktree's `.claude/agents/` directory automatically.

Session worktrees are tracked in `sessions.json` with `managed: true`. `opdev cleanup` removes dead session worktrees and their branches. `opdev teardown --kill-sessions` kills sessions and cleans up their worktrees.

**Workspace sessions** (`opdev session` with no repo arg) are the exception — they run in the workspace root (`clone_root`) with all agents linked, and do not create a worktree.

---

## Alternatives Considered

### Shared working directory

All sessions for a repo share the same directory. Simple, but agents interfere with each other.

Rejected because parallel agents modifying the same files is the primary use case, and shared directories make it impossible.

### Docker containers per session

Each session runs in an isolated container with its own filesystem.

Rejected because Claude Code needs access to the host's SSH keys, git config, npm packages, and other tooling. Container isolation adds complexity without solving the core problem (git worktrees already provide file isolation).

### `git stash` / branch switching

Each session stashes its work before yielding. One working directory, shared sequentially.

Rejected because Claude Code sessions are long-running and interactive — stashing and switching would interrupt the user experience and doesn't support true parallelism.

---

## Consequences

**Positive:**
- True parallel isolation — agents can freely edit files, run builds, and commit without conflicts
- Lightweight — git worktrees share the object store, so they're fast to create and use minimal disk space
- Clean lifecycle — `opdev cleanup` handles everything, no manual worktree management needed
- Agents can commit and push from their session worktrees independently

**Negative / tradeoffs:**
- Disk space — each worktree is a full checkout of the working tree (but objects are shared)
- Branch proliferation — each session creates a `session/<id>` branch. `opdev cleanup` deletes them, but if sessions aren't cleaned up, branches accumulate.
- Worktree limits — git has a soft limit on worktrees per repo. Unlikely to hit in practice.

**Risks:**
- Forgetting to run `opdev cleanup` leaves orphaned worktree directories and branches. Mitigate: `opdev status` clearly shows stopped sessions; could add a periodic cleanup reminder.

---

## References

- git worktree documentation: https://git-scm.com/docs/git-worktree
