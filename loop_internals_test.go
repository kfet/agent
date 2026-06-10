package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kfet/ai"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// runStream calls streamAssistantResponse with an event drainer and returns the
// final message. Lets white-box tests reach branches the agent-level loop only
// hits indirectly.
func runStream(ctx context.Context, agentCtx *AgentContext, cfg *AgentLoopConfig, fn StreamFn) *ai.AssistantMessage {
	events := make(chan AgentEvent, 256)
	done := make(chan struct{})
	go func() {
		for range events {
		}
		close(done)
	}()
	msg := streamAssistantResponse(ctx, agentCtx, cfg, fn, events)
	close(events)
	<-done
	return msg
}

func baseCtx() *AgentContext {
	return &AgentContext{
		Messages: []AgentMessage{NewAgentMessage(ai.NewUserMsg("hi", 0))},
		Tools:    NewToolSet(),
	}
}

// TestStreamAssistantResponse_TransformError covers the TransformContext error path.
func TestStreamAssistantResponse_TransformError(t *testing.T) {
	cfg := &AgentLoopConfig{
		Model:        testModel(),
		ConvertToLLM: testConvertToLLM,
		TransformContext: func(_ context.Context, _ []AgentMessage) ([]AgentMessage, error) {
			return nil, fmt.Errorf("xform boom")
		},
	}
	msg := runStream(context.Background(), baseCtx(), cfg, mockStreamFn(simpleResponse("x")))
	assert.Contains(t, msg.ErrorMessage, "xform boom")
}

// TestStreamAssistantResponse_TransformOK covers the TransformContext success path.
func TestStreamAssistantResponse_TransformOK(t *testing.T) {
	cfg := &AgentLoopConfig{
		Model:        testModel(),
		ConvertToLLM: testConvertToLLM,
		TransformContext: func(_ context.Context, m []AgentMessage) ([]AgentMessage, error) {
			return m, nil
		},
	}
	msg := runStream(context.Background(), baseCtx(), cfg, mockStreamFn(simpleResponse("ok")))
	assert.Equal(t, ai.StopReasonStop, msg.StopReason)
}

// TestStreamAssistantResponse_ConvertNil covers the nil-ConvertToLLM path.
func TestStreamAssistantResponse_ConvertNil(t *testing.T) {
	cfg := &AgentLoopConfig{Model: testModel()}
	msg := runStream(context.Background(), baseCtx(), cfg, mockStreamFn(simpleResponse("x")))
	assert.Contains(t, msg.ErrorMessage, "no ConvertToLLM")
}

// TestStreamAssistantResponse_ConvertError covers the ConvertToLLM error path.
func TestStreamAssistantResponse_ConvertError(t *testing.T) {
	cfg := &AgentLoopConfig{
		Model: testModel(),
		ConvertToLLM: func(_ []AgentMessage) ([]ai.Message, error) {
			return nil, fmt.Errorf("convert boom")
		},
	}
	msg := runStream(context.Background(), baseCtx(), cfg, mockStreamFn(simpleResponse("x")))
	assert.Contains(t, msg.ErrorMessage, "convert boom")
}

// TestStreamAssistantResponse_ApiKeyResolved covers GetAPIKey success (outer)
// and RefreshApiKey success (closure).
func TestStreamAssistantResponse_ApiKeyResolved(t *testing.T) {
	cfg := &AgentLoopConfig{
		Model:        testModel(),
		ConvertToLLM: testConvertToLLM,
		GetAPIKey:    func(_ string) (string, error) { return "secret", nil },
	}
	fn := func(_ *ai.Model, _ ai.Context, opts *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
		assert.Equal(t, "secret", opts.ApiKey)
		require.NotNil(t, opts.RefreshApiKey)
		assert.Equal(t, "secret", opts.RefreshApiKey("anthropic"))
		return mockStreamFn(simpleResponse("ok"))(nil, ai.Context{}, opts)
	}
	msg := runStream(context.Background(), baseCtx(), cfg, fn)
	assert.Equal(t, ai.StopReasonStop, msg.StopReason)
}

// TestStreamAssistantResponse_ApiKeyError covers GetAPIKey error (outer) and
// RefreshApiKey empty-return (closure).
func TestStreamAssistantResponse_ApiKeyError(t *testing.T) {
	cfg := &AgentLoopConfig{
		Model:        testModel(),
		ConvertToLLM: testConvertToLLM,
		GetAPIKey:    func(_ string) (string, error) { return "", fmt.Errorf("no key") },
	}
	fn := func(_ *ai.Model, _ ai.Context, opts *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
		assert.Equal(t, "no key", opts.ApiKeyError)
		assert.Equal(t, "", opts.RefreshApiKey("anthropic"))
		return mockStreamFn(simpleResponse("ok"))(nil, ai.Context{}, opts)
	}
	msg := runStream(context.Background(), baseCtx(), cfg, fn)
	assert.Equal(t, ai.StopReasonStop, msg.StopReason)
}

// doneOnlyStreamFn emits only an EventDone (no EventStart), so addedPartial is
// false — covering the non-partial append + MessageStart-emit branches.
func doneOnlyStreamFn(msg *ai.AssistantMessage) StreamFn {
	return func(_ *ai.Model, _ ai.Context, _ *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
		s := ai.NewAssistantMessageEventStream()
		go func() {
			s.Push(ai.AssistantMessageEvent{Type: ai.EventDone, Reason: msg.StopReason, Message: msg})
			s.End(nil)
		}()
		return s
	}
}

// TestStreamAssistantResponse_NoPartial covers the addedPartial==false branches
// in the EventDone handler.
func TestStreamAssistantResponse_NoPartial(t *testing.T) {
	cfg := &AgentLoopConfig{Model: testModel(), ConvertToLLM: testConvertToLLM}
	msg := runStream(context.Background(), baseCtx(), cfg, doneOnlyStreamFn(simpleResponse("done")))
	assert.Equal(t, "done", msg.Content[0].Text.Text)
}

// doneNilResultStreamFn emits an EventDone with no message, so stream.Result()
// is nil — covering the "stream ended without result" substitution.
func doneNilResultStreamFn() StreamFn {
	return func(_ *ai.Model, _ ai.Context, _ *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
		s := ai.NewAssistantMessageEventStream()
		go func() {
			s.Push(ai.AssistantMessageEvent{Type: ai.EventDone})
			s.End(nil)
		}()
		return s
	}
}

// TestStreamAssistantResponse_NilResult covers the nil-result substitution.
func TestStreamAssistantResponse_NilResult(t *testing.T) {
	cfg := &AgentLoopConfig{Model: testModel(), ConvertToLLM: testConvertToLLM}
	msg := runStream(context.Background(), baseCtx(), cfg, doneNilResultStreamFn())
	assert.Contains(t, msg.ErrorMessage, "stream ended without result")
}

// deltaOnlyStreamFn emits a delta then ends with no Done event, covering the
// post-loop fall-through (result == nil -> "stream ended unexpectedly").
func deltaOnlyStreamFn() StreamFn {
	return func(_ *ai.Model, _ ai.Context, _ *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
		s := ai.NewAssistantMessageEventStream()
		go func() {
			s.Push(ai.AssistantMessageEvent{Type: ai.EventTextDelta})
			s.End(nil)
		}()
		return s
	}
}

func TestStreamAssistantResponse_FallThrough(t *testing.T) {
	cfg := &AgentLoopConfig{Model: testModel(), ConvertToLLM: testConvertToLLM}
	msg := runStream(context.Background(), baseCtx(), cfg, deltaOnlyStreamFn())
	assert.Contains(t, msg.ErrorMessage, "stream ended unexpectedly")
}

// TestExecuteToolCalls_Branches covers the nil-Execute, onUpdate-callback, and
// Execute-error branches of executeToolCalls.
func TestExecuteToolCalls_Branches(t *testing.T) {
	assistantMsg := &ai.AssistantMessage{Content: []ai.AssistantContent{
		ai.NewToolCallContent("id1", "noexec", nil),
		ai.NewToolCallContent("id2", "erroring", nil),
	}}
	ts := NewToolSet()
	ts.Add(AgentTool{Tool: ai.Tool{Name: "noexec"}}) // Execute is nil
	ts.Add(AgentTool{
		Tool: ai.Tool{Name: "erroring"},
		Execute: func(_ context.Context, _ string, _ map[string]any, onUpdate AgentToolUpdateCallback) (AgentToolResult, error) {
			onUpdate(AgentToolResult{StatusMessage: "working"})
			return AgentToolResult{}, fmt.Errorf("tool boom")
		},
	})
	agentCtx := &AgentContext{Tools: ts}

	events := make(chan AgentEvent, 64)
	done := make(chan struct{})
	go func() {
		for range events {
		}
		close(done)
	}()
	batch := executeToolCalls(context.Background(), agentCtx, assistantMsg, events)
	close(events)
	<-done

	require.Len(t, batch.messages, 2)
}

// TestRetryMidToolCall_CtxCancelled covers the ctx.Done() arm of the
// mid-tool-call retry backoff select.
func TestRetryMidToolCall_CtxCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled

	broken := partialToolCallError("Bash", "")
	agentCtx := &AgentContext{
		Messages: []AgentMessage{NewAgentMessage(ai.NewAssistantMsg(*broken))},
		Tools:    NewToolSet(),
	}
	cfg := &AgentLoopConfig{Model: testModel(), ConvertToLLM: testConvertToLLM}

	events := make(chan AgentEvent, 64)
	done := make(chan struct{})
	go func() {
		for range events {
		}
		close(done)
	}()
	msg := retryMidToolCall(ctx, agentCtx, cfg, mockStreamFn(simpleResponse("x")), events, broken)
	close(events)
	<-done

	assert.True(t, hasIncompleteToolCall(msg), "cancelled retry returns the broken message unchanged")
}

// TestAgentLoop_ShouldStopAfterTurn covers the graceful-stop hook returning true
// after a tool-execution turn.
func TestAgentLoop_ShouldStopAfterTurn(t *testing.T) {
	ts := NewToolSet()
	ts.Add(AgentTool{
		Tool: ai.Tool{Name: "echo"},
		Execute: func(_ context.Context, _ string, _ map[string]any, _ AgentToolUpdateCallback) (AgentToolResult, error) {
			return AgentToolResult{Content: []ai.ToolResultContent{{Type: "text", Text: "ok"}}}, nil
		},
	})
	stopped := false
	cfg := &AgentLoopConfig{
		Model:        testModel(),
		ConvertToLLM: testConvertToLLM,
		ShouldStopAfterTurn: func(_ ShouldStopAfterTurnContext) bool {
			stopped = true
			return true
		},
	}
	streamFn := mockStreamFn(toolCallResponse("echo", "t1", nil), simpleResponse("done"))
	agentCtx := &AgentContext{Tools: ts}

	events := make(chan AgentEvent, 200)
	done := make(chan struct{})
	go func() {
		AgentLoop(context.Background(), []AgentMessage{NewAgentMessage(ai.NewUserMsg("go", 0))}, agentCtx, cfg, streamFn, events)
		close(events)
		close(done)
	}()
	_ = collectEvents(events)
	<-done
	assert.True(t, stopped)
}

// TestAgentLoop_MidToolCallExhaustedNonUserFollowUp covers the standalone-note
// path when all mid-tool-call retries fail and the queued follow-up is NOT
// user-role (so it can't be folded) — the note is injected standalone and the
// follow-up is queued.
func TestAgentLoop_MidToolCallExhaustedNonUserFollowUp(t *testing.T) {
	b1 := partialToolCallError("Bash", "")
	b2 := partialToolCallError("Bash", "")
	b3 := partialToolCallError("Bash", "")
	b4 := partialToolCallError("Bash", "")
	recovered := simpleResponse("ok")

	delivered := false
	// A non-user (assistant) follow-up — fold must fail.
	followUp := NewAgentMessage(ai.NewAssistantMsg(*simpleResponse("assistant follow-up")))
	cfg := &AgentLoopConfig{
		Model:        testModel(),
		ConvertToLLM: testConvertToLLM,
		GetFollowUpMessages: func() ([]AgentMessage, error) {
			if !delivered {
				delivered = true
				return []AgentMessage{followUp}, nil
			}
			return nil, nil
		},
	}
	streamFn := mockStreamFn(b1, b2, b3, b4, recovered)
	agentCtx := &AgentContext{}

	events := make(chan AgentEvent, 400)
	done := make(chan struct{})
	var returned []AgentMessage
	go func() {
		returned = AgentLoop(context.Background(), []AgentMessage{NewAgentMessage(ai.NewUserMsg("go", 0))}, agentCtx, cfg, streamFn, events)
		close(events)
		close(done)
	}()
	_ = collectEvents(events)
	<-done

	// A standalone cutoff note (user-role, no follow-up text) must exist.
	standalone := 0
	for _, m := range returned {
		if u := m.Message.AsUser(); u != nil {
			if text, ok := u.Content.(string); ok && strings.Contains(text, "cut off") {
				standalone++
			}
		}
	}
	assert.Equal(t, 1, standalone)
}

// fallbackOnlyStreamFn ends the stream with a non-nil fallback and no Done
// event, so the post-loop fall-through returns a non-nil result.
func fallbackOnlyStreamFn(msg *ai.AssistantMessage) StreamFn {
	return func(_ *ai.Model, _ ai.Context, _ *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
		s := ai.NewAssistantMessageEventStream()
		go func() { s.End(msg) }()
		return s
	}
}

// TestStreamAssistantResponse_FallbackResult covers the `return result` tail
// when stream.Result() is non-nil after an event-less close.
func TestStreamAssistantResponse_FallbackResult(t *testing.T) {
	cfg := &AgentLoopConfig{Model: testModel(), ConvertToLLM: testConvertToLLM}
	msg := runStream(context.Background(), baseCtx(), cfg, fallbackOnlyStreamFn(simpleResponse("fallback")))
	assert.Equal(t, "fallback", msg.Content[0].Text.Text)
}

// TestAgentLoop_AutoResumeCtxCancelled covers the ctx.Done() arm of the
// auto-resume backoff select. The backoff is widened so cancelling on the
// EventAutoResume signal deterministically wins the race against the timer.
func TestAgentLoop_AutoResumeCtxCancelled(t *testing.T) {
	saved := autoResumeBackoffs
	autoResumeBackoffs = []time.Duration{5 * time.Second}
	defer func() { autoResumeBackoffs = saved }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	streamFn := mockStreamFn(transportError("partial prefix", connResetErr), simpleResponse("recovered"))
	cfg := &AgentLoopConfig{Model: testModel(), ConvertToLLM: testConvertToLLM}
	agentCtx := &AgentContext{}

	events := make(chan AgentEvent, 400)
	done := make(chan struct{})
	go func() {
		AgentLoop(ctx, []AgentMessage{NewAgentMessage(ai.NewUserMsg("go", 0))}, agentCtx, cfg, streamFn, events)
		close(events)
		close(done)
	}()
	for ev := range events {
		if ev.Type == EventAutoResume {
			cancel() // cancel while the loop is in its (5s) backoff
		}
	}
	<-done
}
