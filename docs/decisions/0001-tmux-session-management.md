# ADR-0001: tmux for Parallel Claude Code Session Management

**Date:** 2026-03-19
**Status:** Superseded by ADR-0003 (exec-based sessions)
**Deciders:** Tony Ferrell

---

## Context

`opdev` manages Claude Code sessions across multiple repos and worktrees. The core use case is running several Claude Code agents in parallel — for example, one working on the backend, one on the frontend, one on infrastructure — and being able to monitor all of them from one terminal.

The initial implementation launched `claude` as a foreground process, which meant:
- Only one session per terminal
- No way to monitor multiple sessions simultaneously
- Spawning sessions from within Claude Code (e.g., an orchestrator agent launching sub-agents) failed because `claude` needs its own TTY

We need a way to run multiple interactive Claude Code sessions in parallel, each with its own TTY, and switch between them easily.

---

## Decision

Use **tmux** as the session multiplexer. All Claude Code sessions run in tmux windows inside a single tmux session called `opdev`.

When `opdev session` is invoked:
- If the `opdev` tmux session doesn't exist, create it with the first window
- If it does exist, create a new window in it
- If the user is already inside tmux, switch to the new window
- If not inside tmux, attach to the `opdev` session

Session state (PIDs, worktree paths, tmux window names) is persisted to `<clone_root>/.ownpulse-dev/sessions.json`. `opdev status` shows all sessions with their tmux window names. `opdev cleanup` removes dead sessions and their worktrees.

---

## Alternatives Considered

### Direct foreground execution (`cmd.Run()`)

The simplest approach — `opdev session` replaces itself with `claude`. One session per terminal.

Rejected because the entire point is parallel sessions. Users would need to manually open new terminals, navigate to the right directory, set up the right environment, and launch claude by hand. This defeats the purpose of `opdev`.

### Background processes with log files

Launch `claude` in the background, redirect stdout/stderr to log files, provide `opdev logs <session>` to tail them.

Rejected because Claude Code is interactive — it needs a TTY for raw mode input. Redirecting to a file breaks the UI entirely. There is no headless mode (yet).

### GNU Screen

Screen could serve the same role as tmux.

Rejected because tmux is more widely installed on modern systems (default on macOS via Homebrew), has better scriptability (`tmux send-keys`, `tmux list-panes -F`), and the team already uses it.

### Custom terminal multiplexer (embedded)

Build a terminal multiplexer into `opdev` itself using a library like `tcell` or `pty`.

Rejected as massive over-engineering. tmux already solves this, is battle-tested, and users already know its keybindings.

---

## Consequences

**Positive:**
- Multiple Claude Code sessions run in parallel, each with a proper TTY
- Switch between sessions with `Ctrl-b` + window number — standard tmux workflow
- `opdev session` can be called from within a tmux window (including from within Claude Code) to spawn additional sessions
- `opdev status` gives a unified view of all sessions
- Session worktrees are automatically cleaned up when sessions die

**Negative / tradeoffs:**
- tmux is a hard dependency — users must have it installed
- Users unfamiliar with tmux will need to learn basic navigation (`Ctrl-b` + `n`/`p`/`w`/number)
- The tmux session name `opdev` is hardcoded — could conflict if someone already uses that name

**Risks:**
- tmux version differences across platforms could cause issues with the scripting API. Mitigate: use only basic tmux commands (`new-session`, `new-window`, `send-keys`, `list-panes`) that are stable across versions.

---

## References

- tmux documentation: https://github.com/tmux/tmux/wiki
- Claude Code: https://docs.anthropic.com/en/docs/claude-code
