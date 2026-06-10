# OSS readiness review — `github.com/kfet/agent`

Independent review of this repo as a **standalone open-source Go library**
(not as the in-tree fir package it was extracted from). Build is green:
`make all` passes and the module is at a 100% coverage gate.

**Verdict: hold until the two P1 fixes land, then ship v0.1.0.** The runtime
logic (auto-resume / mid-tool-call / role-alternation in `loop.go`) is careful
and well-tested. But two issues gate an OSS release because an evaluator hits
them immediately, plus a cluster of doc defects that reference symbols and
behaviour that don't exist.

Address everything below and keep `make all` green (race + shuffle + 100%
covgate gate). Where a fix changes behaviour, update/extend tests.

---

## P1 (blockers) — must fix before tagging

### P1.1 — Dead / aspirational public API: `ExecutionMode`
`types.go` declares `ToolExecutionMode`, the constants
`ToolExecutionSequential` / `ToolExecutionParallel`, and the field
`AgentTool.ExecutionMode`, with godoc promising *"parallel: tools can execute
concurrently with other tool calls."* But `loop.go:executeToolCalls` **always
iterates tool calls sequentially** and never reads `ExecutionMode` anywhere.
A consumer who sets `ToolExecutionParallel` gets silent sequential execution.

Fix — pick one:
- **(preferred) Delete it**: remove `ToolExecutionMode`, both constants, and the
  `AgentTool.ExecutionMode` field. Re-add when parallel execution is actually
  implemented. Update any tests that reference them.
- **Or document truthfully**: keep the surface but change the godoc to
  "reserved; not yet honored — all tools currently execute sequentially."

### P1.2 — Stale / contradictory README coverage claim
`README.md` has three paragraphs rationalising an **85%** coverage floor as a
documented deviation — but `CHANGELOG.md` (0.0.2) and the `Makefile`
(`-min=100`) show the floor is **100%**. Delete the deviation paragraph and
state 100%. (The "Coverage" section near the top of the README.)

---

## P2 — should fix (do in the same pass)

### P2.1 — Doc/error strings referencing nonexistent symbols
Same class of defect as P1.1 (docs promising things that don't exist):
- `agent.go:85` — `AgentOptions.StreamFn` doc says *"Default uses
  `ai.StreamSimple`."* — no such symbol exists. Reword.
- `agent.go` `SimplePromptStream` returns the error string *"…or pass
  `SimplePromptOptions.StreamFn`"* — `SimplePromptOptions` has only `Model` and
  `Reasoning`, no `StreamFn` field. Fix the message (it should point at
  `AgentOptions.StreamFn` / `DefaultStreamFn`).
- `clamp.go` — `IsCanonicalThinkingLevel`'s godoc comment begins
  *"ClampThinkingLevel reports whether l is…"* — wrong function name
  (copy-paste). Fix to `IsCanonicalThinkingLevel`.

### P2.2 — `tools/` has no package doc
`tools/` ships no `doc.go` / package comment, so half the public surface gets an
empty pkg.go.dev landing page. Add a `tools/doc.go` with a short package comment
describing the standard coding toolbox. Then re-enable `ST1000` in
`staticcheck.conf` (root already has `doc.go`).

### P2.3 — `core` import alias → `ai`
`import core "github.com/kfet/ai"` everywhere is a porting fossil (in-tree the
package was `pkg/ai/core`). External readers see package `ai`; `ai.Message`
reads better than `core.Message`, and critically `doc.go`'s
`[core.AssistantMessageEventStream]` doc-link won't resolve on pkg.go.dev.
Rename the alias to `ai` across the module and fix the `doc.go` cross-reference
links. This is internal-only (no cross-module break).

### P2.4 — `GetApiKey` → `GetAPIKey`
Re-enable `ST1003` after fixing. `GetApiKey` is this module's own symbol, so the
rename is self-contained — rename the field/param everywhere it's defined here.
**Note:** do NOT rename `core.Api` / `core.StreamOptions.ApiKey` references —
those are `kfet/ai`'s symbols, still spelled `Api`/`ApiKey` in the pinned
`v0.0.1`. They get renamed in a separate coordinated dependency bump (see
below). Keep `make all` green against the current `kfet/ai v0.0.1`.

Keep `ST1005` (capitalized tool error strings, e.g. `edit.go` `fmt.Errorf("Could
not find the exact text…")`) **disabled** — those are LLM-facing tool payloads,
asserted byte-for-byte, and capitalization aids model legibility. Legitimate
documented exception.

### P2.5 — Scrub fir-isms from comments
`plan.go:19-20` ("fir-style observable card … see
docs/design/observable-cards.md"), `loop.go:18` ("fir resumed the turn"),
`agent.go:323` ("fir's pkg/session"), `agent.go:42`/`doc.go` ("hosts (such as
fir)"). Reword generically for a standalone lib.

## P3 — nice to have

- `agent.go` exposes a broad flat surface of `Get*`/`Set*` accessors for a
  "deliberately small" runtime. Consider folding some into option structs.
  Taste — only if it doesn't balloon the diff.

---

## Cross-repo coordination — the `Api`/`ApiKey` rename

The sibling `kfet/ai` repo is renaming `Api`→`API`, `ApiKey`→`APIKey` in its own
worktree. This repo consumes those symbols (`core.Api`, `StreamOptions.ApiKey`,
…). **Do not** chase that rename here — it would break the build against the
pinned `kfet/ai v0.0.1`. Keep this module building against `v0.0.1`, finish all
repo-local fixes above, and in your final summary note that a follow-up
coordinated dependency bump (to the released, renamed `kfet/ai`) is required to
complete the `Api`→`API` migration on the consumer side.
