// Ported from: packages/coding-agent/src/core/tools/bash.ts
// Upstream hash: 1caadb2e

package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"syscall"
	"time"

	"github.com/kfet/agent"
	"github.com/kfet/ai"
	"log/slog"
)

// BashToolParams are the parameters for the bash tool.
type BashToolParams struct {
	Command string   `json:"command"`
	Timeout *float64 `json:"timeout,omitempty"` // seconds
	Cwd     string   `json:"cwd,omitempty"`     // optional per-call working directory override
}

// DefaultBashTimeout is applied when the agent does not pass an explicit
// timeout. Agents can override (up or down) via the "timeout" parameter.
// It is a var (not const) so tests can shorten it.
var DefaultBashTimeout = 10 * time.Second

// createOutputFile isolates output-file creation so the (disk-full / EMFILE)
// failure path can be exercised in tests.
var createOutputFile = newLogFile

// readOutputFile isolates os.ReadFile for the same reason.
var readOutputFile = os.ReadFile

// processWait isolates (*os.Process).Wait so its non-ExitError failure path
// (the wait4 syscall can fail with EINTR/ECHILD) can be exercised in tests.
var processWait = func(p *os.Process) (*os.ProcessState, error) { return p.Wait() }

// NewBashTool creates the bash tool for the given working directory.
func NewBashTool(cwd string) agent.AgentTool {
	return agent.AgentTool{
		Tool: ai.Tool{
			Name: "bash",
			Description: fmt.Sprintf(
				"Execute a bash command in the current working directory (%s/%s). Returns stdout and stderr. Output is truncated to last %d lines or %dKB (whichever is hit first). If truncated, full output is saved to a temp file. Optionally provide a timeout in seconds (default 10s if omitted; pass an explicit value to override up or down). Optionally provide `cwd` to run in a specific directory instead of the session's working directory (absolute path, or relative to the session directory); use this to recover if the session's working directory was deleted. Background jobs (`&`, `nohup`, `(sleep N; cmd) &`) keep running after the call returns; the result lists their pids and log file — poll it with the `wait` tool. They are killed when the session ends. For a long foreground command, pass a larger `timeout` instead. For viewing file contents, prefer the `read` tool over `cat`/`sed -n`/`head`/`tail` — `read` output is tracked and de-duplicated. For modifying files, prefer `edit`/`write` over `sed -i` / heredoc rewrites. Each result carries a `hash` of the output; the command always executes, but if you pass `if_hash` and the fresh output matches it you get back a tiny `unchanged` stub instead of the full output.",
				runtime.GOOS, runtime.GOARCH, DefaultMaxLines, DefaultMaxBytes/1024,
			),
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"command": map[string]any{
						"type":        "string",
						"description": "Bash command to execute",
					},
					"timeout": map[string]any{
						"type":        "number",
						"description": "Timeout in seconds. Defaults to 10s if omitted; pass an explicit value to override (e.g. 60 for slower commands).",
					},
					"cwd": map[string]any{
						"type":        "string",
						"description": "Optional working directory to run the command in, overriding the session's default. Absolute, or relative to the session directory. Useful to recover when the session's working directory has been deleted.",
					},
					"if_hash": map[string]any{
						"type":        "string",
						"description": "Optional. If you already hold this command's output, pass the `hash` from your previous run. The command always re-executes; if the fresh output still matches, you get back a tiny `unchanged` stub instead of the full output.",
					},
				},
				"required": []string{"command"},
			},
		},
		Label: "bash",
		Execute: func(ctx context.Context, toolCallID string, params map[string]any, onUpdate agent.AgentToolUpdateCallback) (agent.AgentToolResult, error) {
			command, _ := params["command"].(string)
			if command == "" {
				return agent.AgentToolResult{}, fmt.Errorf("command is required")
			}

			var timeout time.Duration
			if t, ok := params["timeout"].(float64); ok && t > 0 {
				timeout = time.Duration(t * float64(time.Second))
			} else {
				timeout = DefaultBashTimeout
			}

			// Allow a per-call working-directory override. Resolved against the
			// session cwd (handles ~, absolute paths, and relative paths). This
			// is the clean recovery path when the session cwd was deleted: the
			// model can point bash at a directory that still exists instead of
			// prefixing the command with `cd`.
			runDir := cwd
			if override, _ := params["cwd"].(string); override != "" {
				runDir = ResolveToCwd(override, cwd)
			}

			ifHash, _ := params["if_hash"].(string)
			return executeBash(ctx, command, runDir, timeout, ifHash)
		},
	}
}

// NewBashToolWithPrefix creates a bash tool that prepends a shell command prefix
// (e.g., "source /etc/profile") before each command.
func NewBashToolWithPrefix(cwd, commandPrefix string) agent.AgentTool {
	t := NewBashTool(cwd)
	if commandPrefix == "" {
		return t
	}
	orig := t.Execute
	t.Execute = func(ctx context.Context, toolCallID string, params map[string]any, onUpdate agent.AgentToolUpdateCallback) (agent.AgentToolResult, error) {
		if cmd, ok := params["command"].(string); ok {
			p := make(map[string]any, len(params))
			for k, v := range params {
				p[k] = v
			}
			p["command"] = commandPrefix + "\n" + cmd
			return orig(ctx, toolCallID, p, onUpdate)
		}
		return orig(ctx, toolCallID, params, onUpdate)
	}
	return t
}

// truncateForLog truncates a string for log output.
func truncateForLog(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

// executeBash runs a bash command and returns the result.
func executeBash(ctx context.Context, command, cwd string, timeout time.Duration, ifHash string) (agent.AgentToolResult, error) {
	slog.Debug("bash exec", "command", truncateForLog(command, 200), "cwd", cwd, "timeout", timeout)
	start := time.Now()
	// If the working directory no longer exists, refuse to run rather than
	// silently executing somewhere else. The cwd can be deleted out from under
	// a live session (git worktree removed, temp dir cleaned up, branch switch
	// deleting the dir). Falling back to a surviving directory — the nearest
	// ancestor, $HOME, or a temp dir — is dangerous: a relative-path command
	// (rm -rf *, git clean -fdx, mv * ...) would hit real files the agent was
	// never pointed at. Instead, surface the situation so the model can decide
	// (recreate the dir, or target an existing path with an explicit `cd`).
	if !isDir(cwd) {
		slog.Warn("bash cwd missing", "cwd", cwd)
		msg := fmt.Sprintf(
			"working directory no longer exists: %s\n\n"+
				"It was likely removed after this session started (a git worktree was "+
				"deleted, a temp dir was cleaned up, or a branch switch removed it). "+
				"The command was NOT run, to avoid executing in an unintended "+
				"directory. To proceed, run in a directory that still exists — pass "+
				"the `cwd` parameter with an existing absolute path (or recreate the "+
				"missing directory).",
			cwd,
		)
		return agent.AgentToolResult{
			Content: []ai.ToolResultContent{{Type: "text", Text: msg}},
			IsError: true,
		}, nil
	}

	// Apply timeout if specified
	var cancel context.CancelFunc
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	// Create command. We deliberately do NOT use exec.CommandContext: we
	// manage the process group ourselves so timeout/abort kill every member,
	// while a normal exit leaves backgrounded jobs (`cmd &`) running.
	cmd := exec.Command("bash", "-c", command)
	cmd.Dir = cwd
	cmd.Env = AppendColorEnv(os.Environ())
	cmd.Env = append(cmd.Env, "GIT_EDITOR=true")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	// Output goes to a temp file, not a pipe: background jobs may keep the fd
	// open after bash exits, and a file never blocks us waiting for EOF. We
	// read a snapshot when bash exits; later writes land in the file only.
	outFile, err := createOutputFile()
	if err != nil {
		return agent.AgentToolResult{}, fmt.Errorf("output file: %w", err)
	}
	logPath := outFile.Name()
	keepLog := false
	defer func() {
		if !keepLog {
			_ = os.Remove(logPath)
		}
	}()
	cmd.Stdout = outFile
	cmd.Stderr = outFile

	if err := cmd.Start(); err != nil {
		outFile.Close()
		return agent.AgentToolResult{}, err
	}
	// Children hold their own copies of the fd now.
	outFile.Close()
	pgid := cmd.Process.Pid

	// Watch ctx for cancellation/timeout; kill the process group if it fires
	// before bash exits naturally.
	mainExited := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
		case <-mainExited:
		}
	}()

	// Wait for bash itself to exit (does not wait for backgrounded children).
	state, waitErr := processWait(cmd.Process)
	close(mainExited)

	var bgNote string
	if ctx.Err() != nil {
		// Timeout/abort: the whole group goes, including background jobs.
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
	} else if alive, pids := groupAlive(pgid); alive {
		if limitErr := registerBackgroundJob(pgid, logPath); limitErr != nil {
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
			return agent.AgentToolResult{}, limitErr
		}
		keepLog = true
		pidText := ""
		if len(pids) > 0 {
			pidText = fmt.Sprintf(" (pids %s)", joinInts(pids))
		}
		bgNote = fmt.Sprintf("[background: process group %d still running%s. Output continues in %s. Capped at %dMB. It is killed when the session ends.]",
			pgid, pidText, logPath, MaxBackgroundLogBytes>>20)
	}

	data, readErr := readOutputFile(logPath)
	if readErr != nil {
		return agent.AgentToolResult{}, fmt.Errorf("read output: %w", readErr)
	}

	// Synthesise an exec.ExitError on non-zero exit so downstream code keeps
	// working (cmd.Wait normally does this for us).
	err = waitErr
	if err == nil && state != nil && !state.Success() {
		err = &exec.ExitError{ProcessState: state}
	}

	output := string(data)

	// Keep raw output (with ANSI) for display, strip for LLM context
	rawOutput := output
	output = StripAnsi(output)

	// Apply tail truncation
	truncResult := TruncateTail(output, TruncationOptions{})
	outputText := truncResult.Content
	if outputText == "" {
		outputText = "(no output)"
	}

	// Handle truncation notice
	var fullOutputPath string
	if truncResult.Truncated {
		// The output file already holds the full output; keep it.
		keepLog = true
		fullOutputPath = logPath

		startLine := truncResult.TotalLines - truncResult.OutputLines + 1
		endLine := truncResult.TotalLines

		if truncResult.TruncatedBy == "lines" {
			outputText += fmt.Sprintf("\n\n[Showing lines %d-%d of %d. Full output: %s]",
				startLine, endLine, truncResult.TotalLines, fullOutputPath)
		} else {
			outputText += fmt.Sprintf("\n\n[Showing lines %d-%d of %d (%dKB limit). Full output: %s]",
				startLine, endLine, truncResult.TotalLines, DefaultMaxBytes/1024, fullOutputPath)
		}
	}

	if bgNote != "" {
		outputText += "\n\n" + bgNote
	}

	if err != nil {
		// Check for specific error types
		if ctx.Err() == context.DeadlineExceeded {
			if output != "" {
				outputText += "\n\n"
			}
			outputText += fmt.Sprintf("Command timed out after %.0f seconds", timeout.Seconds())
			return agent.AgentToolResult{}, errors.New(outputText)
		}
		if ctx.Err() == context.Canceled {
			if output != "" {
				outputText += "\n\n"
			}
			outputText += "Command aborted"
			return agent.AgentToolResult{}, errors.New(outputText)
		}

		// Process exited with non-zero code
		if exitErr, ok := err.(*exec.ExitError); ok {
			slog.Debug("bash done", "exitCode", exitErr.ExitCode(), "outputLen", len(output), "elapsed", time.Since(start))
			outputText += fmt.Sprintf("\n\nCommand exited with code %d", exitErr.ExitCode())
			return agent.AgentToolResult{}, errors.New(outputText)
		}

		slog.Warn("bash error", "err", err, "elapsed", time.Since(start))
		return agent.AgentToolResult{}, err
	}

	slog.Debug("bash done", "exitCode", 0, "outputLen", len(output), "truncated", truncResult.Truncated, "elapsed", time.Since(start))

	// Hash the canonical (stripped, untruncated) output so the model can opt
	// into the confirm-unchanged path. The command always executed above;
	// hashing only affects what we put in the transcript.
	hash := readContentHash([]byte(output))
	if ifHash != "" && ifHash == hash {
		return agent.AgentToolResult{
			Content: []ai.ToolResultContent{{
				Type: "text",
				Text: fmt.Sprintf("[unchanged] command output matches if_hash (hash: %s)", hash),
			}},
			Details: map[string]any{"unchanged": true, "hash": hash},
		}, nil
	}

	details := map[string]any{"hash": hash}
	if fullOutputPath != "" {
		details["fullOutputPath"] = fullOutputPath
	}

	// Store raw output (with ANSI colors) for TUI display.
	// Truncate to same line budget as the LLM output.
	rawTrunc := TruncateTail(rawOutput, TruncationOptions{})
	details["rawOutput"] = rawTrunc.Content

	return agent.AgentToolResult{
		Content: []ai.ToolResultContent{
			{Type: "text", Text: outputText},
		},
		Details: details,
		Meta:    map[string]string{"hash": hash},
	}, nil
}
