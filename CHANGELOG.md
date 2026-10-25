# Changelog

All notable changes to this project are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.6.0] - 2026-10-26

### Added

- `Agent.AppendContext(msg)`: appends a context message when idle; during a run it queues it under the run lock and the loop delivers it after the next tool results (no extra turn), or at agent_end. Queued messages emit message_start/message_end exactly once. `Agent.PeekContextQueue()` and `AgentLoopConfig.GetContextMessages` back it.

## [0.5.2] - 2026-10-22

### Changed

- bash tool: the print-mode background-job note now also says "Do not detach it (no setsid, nohup, tmux, at)." In an A/B test, gpt-5.5 went from 0/2 (with one setsid escape) to 2/3 (no escapes) on "in 2 minutes create x".

## [0.5.1] - 2026-10-22

### Changed

- bash tool: shorter description and parameter texts.
- bash tool: `tools.PrintMode` (set by one-shot hosts such as `fir -p`). When it is set, the background-job note warns that the session ends after this turn and suggests running in the foreground with a larger timeout. In an A/B test, opus went from 0/2 to 2/2 on "in 2 minutes create x".

## [0.5.0] - 2026-10-22

### Added

- `Agent.SteeringQueueLen()` reports pending steering messages without draining them.

## [0.4.0] - 2026-10-22

### Changed

- bash tool: output goes to an in-memory pipe again, not temp log files. When
  bash exits, the result is what the pipe holds at that point (non-blocking
  read, with no wait for EOF and no killpg). The per-process log dir, stale-dir
  cleanup and log cap are gone (`tools.BashLogDir` and
  `tools.MaxBackgroundLogBytes` were removed).
- bash tool: when group members outlive bash, the result names a job id. A
  reader keeps the last `tools.MaxJobOutputBytes` (1MB) of the job's output.
  If the host crashes, the kernel closes the read ends, and jobs that write
  afterwards die of SIGPIPE.
- Truncated foreground output is written to a temp file only when it is
  truncated.

### Added

- bash tool params `job` (int), `kill` (bool) and `jobs` (bool). `job=<id>`
  returns the job's status and the output since the last read. `job=<id>,
  kill=true` kills its group. `jobs=true` or `job=0` lists all jobs. `command`
  is required only when none of these is set. An exited job is dropped once
  it has been read.
- On Linux the output pipe is grown to 1MB (`F_SETPIPE_SZ`, best effort).

## [0.3.0] - 2026-10-22

### Added

- bash tool: output files live in one per-process log dir
  (`$TMPDIR/agent-bash-<pid>-*`, see `tools.BashLogDir`); dirs left by dead
  processes are removed when it is first created.
- bash tool: a background job's log is capped at `tools.MaxBackgroundLogBytes`
  (50MB); past the cap it is emptied and restarted with a notice line.
- bash tool: at most `tools.MaxBackgroundJobs` (10) live background jobs; a
  call that would exceed it fails with an error naming the running process
  groups, and its new group is killed.

## [0.2.0] - 2026-10-22

### Changed

- bash tool: background jobs (`cmd &`, `nohup cmd &`, `(sleep N; cmd) &`) now
  keep running after the call returns instead of being killed with the
  process group. Output goes to a temp file instead of a pipe; when group
  members survive bash, the result lists their pids and the log file (kept,
  still receiving output). Timeout and abort still kill the whole group.

### Added

- `tools.KillBackgroundJobs()` terminates (SIGTERM, grace, SIGKILL) every
  background process group left by the bash tool; hosts call it at session
  end. `tools.BackgroundJobs()` lists the live ones.

## [0.1.4] - 2026-10-08

### Added

- `Agent.TakeQueues()` atomically returns and clears both the steering and
  follow-up queues (enqueue order preserved). Call it before `Abort` when the
  history is about to be discarded (session restart) so queued messages are
  not drained into the aborted run and can be re-queued on the fresh session.

### Fixed

- `TestGrepTool_FallbackWhenNoRipgrep` no longer assumes `rg` is absent from
  `/usr/bin`; it builds a PATH containing only `grep`, so the 100% coverage
  gate holds on Linux too.

## [0.1.3] - 2026-08-22

### Fixed

- A provider error is no longer flattened into "response had no usable
  content". `StreamFn` is an injected dependency, so provider output is
  untrusted input at the agent boundary — and at least one provider path
  reports `StopReason == StopReasonError` while leaving `ErrorMessage`
  empty. `streamAssistantResponse` now enforces the invariant that an
  assistant message with an error stop reason always carries a non-empty
  `ErrorMessage`, synthesising an honest, diagnosable substitute (provider,
  model, stop reason, content-block summary) when the provider says
  nothing. Nothing is invented: no status code or error class is
  fabricated, and the synthesised text deliberately matches none of the
  `ai.IsRetryableError` patterns, so it stays terminal. The invariant now
  holds for the returned message, the message stored in agent history, and
  the emitted `EventMessageStart` / `EventMessageEnd` events.
- `SimplePrompt` / `SideQuery` now classify a turn as errored by its stop
  reason (`StopReason == StopReasonError`) as well as by non-empty
  `ErrorMessage`. Previously a blank-error message fell through to the
  degenerate-generation arm, was re-rolled three times with thinking forced
  off, and surfaced as `response had no usable content (blocks: [])
  (stop_reason=error)` — destroying the provider's diagnosis and burning
  three round trips. Such a call now fails on the FIRST attempt with a
  diagnosable message. Retry semantics are otherwise unchanged: only
  `ai.IsRetryableError` transport/rate-limit classes are re-rolled.

## [0.1.2] - 2026-07-27

### Fixed

- **bash tool description: corrected the process-cleanup claim.** It said daemons
  detaching via `setsid` **or double-fork** escape the post-command cleanup. The
  double-fork half is false: cleanup is a process-**group** kill, and `fork()`
  preserves the process group, so a double-forked child is reparented to init and
  killed anyway. Only `setsid` allocates a new group. The cited examples (tmux
  server, sshd, dockerd) escape because they call `setsid()`, not because they
  fork twice. Also noted that `setsid` does not leave the **cgroup**, so a systemd
  unit restart still tears down "detached" children. Rewritten to state the
  underlying rule rather than enumerate consequences. Verified empirically; the
  in-code comment at bash.go:183 was already correct.

## [0.1.1] - 2026-06-11

### Added

- Tool-result `Meta` channel: `AgentToolResult` now carries an optional
  `Meta map[string]string` of small, structured metadata the LLM should
  see alongside the content. The agent loop copies it onto
  `ai.ToolResultMessage.Meta`, where the provider transform renders it
  for the provider-bound message only (internal consumers that join
  content blocks never see it). Forward-ported from fir to keep the
  extracted runtime in parity.
- `if_hash` confirm-unchanged opt-in on the `bash` and `read` tools:
  every full read / command result carries a content `hash` (surfaced
  via `Meta{hash}`); passing `if_hash` returns a tiny `unchanged` stub
  when the content still matches, else the full body/output. Partial
  (offset/limit) reads carry no hash and ignore `if_hash`. Tool
  descriptions nudge `read` over `cat`/`sed`/`head`/`tail` and
  `edit`/`write` over `sed -i`/heredoc rewrites.

### Changed

- Bumped the `github.com/kfet/ai` dependency to v0.1.2 (provides
  `ToolResultMessage.Meta` and `RenderToolResultMeta`).

## [0.1.0] - 2026-06-10

### Breaking

- Consumers must rename any `GetApiKey` field/usage to `GetAPIKey` and
  drop any reference to the removed `ToolExecutionMode` API (see below).

### Removed

- The dead `ToolExecutionMode` API (`ToolExecutionSequential`,
  `ToolExecutionParallel`, and `AgentTool.ExecutionMode`): tool calls
  have always executed sequentially and the field was never read. It
  will be reintroduced if/when parallel tool execution is implemented.

### Changed

- Renamed `GetApiKey` to `GetAPIKey` on `AgentOptions` and
  `AgentLoopConfig` (Go initialism convention; staticcheck ST1003
  re-enabled). Bumped the `github.com/kfet/ai` dependency to v0.1.0 and
  adopted its `API`-initialism rename (`ai.API*` constants, `Model.API`,
  `StreamOptions.APIKey`/`APIKeyError`/`RefreshAPIKey`) at all call sites.
- The `core` import alias for `github.com/kfet/ai` is gone; the package
  is imported under its real name `ai` (internal-only change).

### Fixed

- README "Coverage" section now states the actual 100% gate floor
  (was stale 85% rationale).
- Doc/error strings no longer reference nonexistent symbols
  (`ai.StreamSimple`, `SimplePromptOptions.StreamFn`); the
  `IsCanonicalThinkingLevel` godoc names the right function.
- Added a `tools` package comment (`tools/doc.go`); staticcheck ST1000
  re-enabled.
- Comments scrubbed of references to the originating host application.

## [0.0.2]

### Changed

- Test coverage raised to 100% for both the root `agent` package and the
  `tools` package; the covgate floor is bumped from 85% to 100%. Added
  runtime unit tests for the streaming/retry/tool-error paths and the
  full tools toolbox.

### Removed

- Two provably-dead defensive branches in `agent.go` (the `msg == nil`
  arm of `SimplePromptStream`, guaranteed non-nil by `streamSinglePrompt`'s
  contract, and the `PendingToolCalls == nil` guard in `runLoop`, a
  constructor invariant).

### Notes

- A handful of defensive guards that are unreachable for valid inputs are
  now exercised via small injectable function-var seams instead of being
  deleted: `tools.fileExists` (macOS APFS NFC↔NFD folding makes the
  filename-variant fallbacks otherwise unreachable), `os.Pipe` /
  process-`Wait` in the bash runner, `Cmd.StdoutPipe` in grep, and the
  PNG/JPEG encoders in imageresize. Each seam isolates a real dependency
  boundary and its test asserts the error propagates.

## [0.0.1]

### Added


- Initial extraction from fir (`github.com/kfet/fir`) Phase 5. The
  model-agnostic coding-agent runtime — `Agent`, the agent loop,
  `ToolSet`, `AgentTool`, thinking-level clamping — in the root `agent`
  package, plus the standard coding toolbox (bash, read, write, edit,
  editdiff, find, grep, imageresize, plan) in `tools/`. Depends only on
  `github.com/kfet/ai`, `github.com/kfet/pinexec`, and golang.org/x.
  Portions ported from pi-mono (MIT, Copyright (c) 2025 Mario Zechner).

### Known limitations

- The covgate gate floor is 85%, not the 100% used by sibling repos.
  fir had no coverage gate, so runtime paths exercised only by fir-side
  session/mode/e2e tests arrived without unit coverage. Raising the
  floor toward 100% with runtime-level unit tests (streaming, retry,
  tool-error paths) is tracked follow-up work. (Done in 0.0.2.)
