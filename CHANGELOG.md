# Changelog

All notable changes to this project are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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
  tool-error paths) is tracked follow-up work.
