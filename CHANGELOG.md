# Changelog

All notable changes to this project are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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
