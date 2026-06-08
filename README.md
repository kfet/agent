# agent

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

A small, model-agnostic coding-agent runtime in Go. It turns a stream of
LLM events into a stream of tool calls — with retries, steering,
follow-ups, abort, and thinking-budget clamping already handled — and
ships the standard coding toolbox (bash, read, write, edit, editdiff,
find, grep, imageresize, plan).

## Why

Wiring an LLM into a working coding agent means re-solving the same
control-flow problems every time: stream assistant turns, parse tool
calls out of partial JSON, execute tools, feed results back, retry on
transient/rate-limit errors, fold steering and follow-up messages into
the loop, and clamp thinking budgets to what the model supports. `agent`
packages that loop behind a compact API so the host only supplies a
model, a tool set, a prompt, and a streaming function.

## Dependencies

The runtime is deliberately lean. It depends only on:

- [`github.com/kfet/ai`](https://github.com/kfet/ai) — portable AI
  primitives (messages, tools, models, streaming events).
- [`github.com/kfet/pinexec`](https://github.com/kfet/pinexec) — the
  bash tool's cancellable shell runner.
- `golang.org/x/image` and `golang.org/x/text` — image resizing and
  Unicode-aware path handling for the tools subpackage.

No session store, MCP runtime, TUI, extension host, provider catalog, or
HTTP client. A `forbidden_imports_test.go` guard fails the build if the
transitive import graph ever grows beyond that sanctioned set.

## Layout

| Package | Purpose |
| --- | --- |
| `agent` (root) | `Agent`, the agent loop, `ToolSet`, `AgentTool`, thinking-level clamping, side-query sanitisation. |
| `agent/tools` | The standard coding toolbox: bash, read, write, edit, editdiff, find, grep, imageresize, plan. |

## Coverage

The repo ships the sibling-convention `make all` (gofmt + vet +
staticcheck + race + a covgate gate). The gate floor is **85%**, not the
100% used by the smaller sibling repos: this runtime was extracted from
fir, which had no coverage gate, so the parts of it exercised only by
fir-side session/mode/e2e tests did not arrive with a unit test. Pulling
the floor toward 100% with runtime-level unit tests is tracked follow-up
work.

## Attribution

Portions are ported from [pi-mono](https://github.com/badlogic/pi-mono)
(MIT, Copyright (c) 2025 Mario Zechner). Files derived from that project
carry a `// Ported from:` header. See [LICENSE](LICENSE).

## License

MIT — see [LICENSE](LICENSE).
