package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	core "github.com/kfet/ai"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestResolveStreamFn_AllNil covers the both-nil fall-through.
func TestResolveStreamFn_AllNil(t *testing.T) {
	prev := DefaultStreamFn
	DefaultStreamFn = nil
	defer func() { DefaultStreamFn = prev }()
	assert.Nil(t, resolveStreamFn(context.Background(), nil))
}

// TestUpdateTools_NilToolsInitializes covers the nil-ToolSet lazy-init branch.
func TestUpdateTools_NilToolsInitializes(t *testing.T) {
	a := NewAgent(AgentOptions{})
	a.mu.Lock()
	a.state.Tools = nil
	a.mu.Unlock()
	a.UpdateTools(func(ts *ToolSet) { ts.Add(AgentTool{Tool: core.Tool{Name: "x"}}) })
	assert.True(t, a.State().Tools.Has("x"))
}

// TestAbort_WithAndWithoutCancel covers both the nil and non-nil abortCancel paths.
func TestAbort_WithAndWithoutCancel(t *testing.T) {
	a := NewAgent(AgentOptions{})
	a.Abort() // abortCancel nil -> no-op

	called := false
	a.mu.Lock()
	a.abortCancel = func() { called = true }
	a.mu.Unlock()
	a.Abort()
	assert.True(t, called)
}

// TestBlockHelpers covers SummarizeBlocks (all four block kinds),
// formatBlockSummary (thinking + default), and renderSimplePromptContent
// (text + thinking + toolCall).
func TestBlockHelpers(t *testing.T) {
	content := []core.AssistantContent{
		core.NewTextContent("hello"),
		core.NewThinkingContent("pondering"),
		core.NewToolCallContent("id1", "mytool", map[string]any{"k": "v"}),
		core.NewServerContent("web_search", json.RawMessage(`{"q":"x"}`), "display"),
	}

	blocks := SummarizeBlocks(content)
	require.Len(t, blocks, 4)

	s := formatBlockSummary(blocks)
	assert.Contains(t, s, "text(len=")
	assert.Contains(t, s, "thinking(th=")
	assert.Contains(t, s, "toolCall(len=")
	assert.Contains(t, s, "server(len=")

	rendered, summary, err := renderSimplePromptContent(content)
	require.NoError(t, err)
	require.Len(t, summary, 4)
	assert.Contains(t, rendered, "hello")
	assert.Contains(t, rendered, "[think] pondering")
	assert.Contains(t, rendered, "[tool mytool")

	// Empty content -> error with block summary.
	_, _, err = renderSimplePromptContent(nil)
	assert.Error(t, err)
}

// TestContinue_AlreadyStreaming covers the IsStreaming guard.
func TestContinue_AlreadyStreaming(t *testing.T) {
	a := NewAgent(AgentOptions{Model: testModel(), StreamFn: mockStreamFn(simpleResponse("x"))})
	a.mu.Lock()
	a.state.IsStreaming = true
	a.mu.Unlock()
	err := a.Continue()
	assert.ErrorContains(t, err, "already processing")
}

// TestContinue_NoMessages covers the empty-history guard.
func TestContinue_NoMessages(t *testing.T) {
	a := NewAgent(AgentOptions{Model: testModel(), StreamFn: mockStreamFn(simpleResponse("x"))})
	err := a.Continue()
	assert.ErrorContains(t, err, "no messages")
}

// TestContinue_LastAssistantWithSteering covers the steering-dequeue branch and
// the skipInitialSteeringPoll path in runLoop.
func TestContinue_LastAssistantWithSteering(t *testing.T) {
	a := NewAgent(AgentOptions{Model: testModel(), StreamFn: mockStreamFn(simpleResponse("resp"))})
	a.ReplaceMessages([]AgentMessage{
		NewAgentMessage(core.NewUserMsg("hi", 0)),
		NewAgentMessage(core.NewAssistantMsg(*simpleResponse("prev"))),
	})
	a.Steer(NewAgentMessage(core.NewUserMsg("steer", 0)))
	require.NoError(t, a.Continue())
	a.WaitForIdle()
	assert.False(t, a.State().IsStreaming)
}

// TestContinue_LastAssistantWithFollowUp covers the follow-up-dequeue branch.
func TestContinue_LastAssistantWithFollowUp(t *testing.T) {
	a := NewAgent(AgentOptions{Model: testModel(), StreamFn: mockStreamFn(simpleResponse("resp"))})
	a.ReplaceMessages([]AgentMessage{
		NewAgentMessage(core.NewUserMsg("hi", 0)),
		NewAgentMessage(core.NewAssistantMsg(*simpleResponse("prev"))),
	})
	a.FollowUp(NewAgentMessage(core.NewUserMsg("follow", 0)))
	require.NoError(t, a.Continue())
	a.WaitForIdle()
	assert.False(t, a.State().IsStreaming)
}

// TestContinue_LastNotAssistant covers the final runLoop(nil) path.
func TestContinue_LastNotAssistant(t *testing.T) {
	a := NewAgent(AgentOptions{Model: testModel(), StreamFn: mockStreamFn(simpleResponse("resp"))})
	a.ReplaceMessages([]AgentMessage{NewAgentMessage(core.NewUserMsg("hi", 0))})
	require.NoError(t, a.Continue())
	a.WaitForIdle()
	assert.False(t, a.State().IsStreaming)
}

// TestSimplePrompt_NoModel covers the no-model error branch.
func TestSimplePrompt_NoModel(t *testing.T) {
	a := NewAgent(AgentOptions{StreamFn: mockStreamFn(simpleResponse("x"))})
	_, err := a.SimplePrompt(context.Background(), nil, nil)
	assert.ErrorContains(t, err, "no model selected")
}

// TestSimplePrompt_NoStreamFn covers the no-stream-function error branch.
func TestSimplePrompt_NoStreamFn(t *testing.T) {
	prev := DefaultStreamFn
	DefaultStreamFn = nil
	defer func() { DefaultStreamFn = prev }()
	a := NewAgent(AgentOptions{Model: testModel()})
	_, err := a.SimplePrompt(context.Background(), nil, nil)
	assert.ErrorContains(t, err, "no stream function configured")
}

// TestSimplePrompt_NoConvertToLLM covers the nil-ConvertToLLM error branch.
func TestSimplePrompt_NoConvertToLLM(t *testing.T) {
	a := NewAgent(AgentOptions{Model: testModel(), StreamFn: mockStreamFn(simpleResponse("x"))})
	a.mu.Lock()
	a.convertToLLM = nil
	a.mu.Unlock()
	_, err := a.SimplePrompt(context.Background(), nil, nil)
	assert.ErrorContains(t, err, "no ConvertToLLM")
}

// TestSimplePrompt_CtxCancelledDuringRetry covers the ctx.Done() path in the
// inter-attempt backoff: a degenerate (empty) response forces a retry, and the
// context is already cancelled so the backoff select takes the cancel arm.
func TestSimplePrompt_CtxCancelledDuringRetry(t *testing.T) {
	// Stream returns an assistant message with no usable content -> render
	// error -> retry path.
	empty := &core.AssistantMessage{
		Role:       "assistant",
		Content:    []core.AssistantContent{},
		Api:        core.ApiAnthropicMessages,
		Provider:   core.ProviderAnthropic,
		Model:      "test-model",
		StopReason: core.StopReasonStop,
		Timestamp:  time.Now().UnixMilli(),
	}
	a := NewAgent(AgentOptions{Model: testModel(), StreamFn: mockStreamFn(empty)})
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled
	_, err := a.SimplePrompt(ctx, nil, nil)
	assert.ErrorIs(t, err, context.Canceled)
}

// TestRunLoop_NoStreamFnSurfacesError covers the runLoop streamFn==nil branch
// that surfaces an error through agent state without starting the goroutine.
func TestRunLoop_NoStreamFnSurfacesError(t *testing.T) {
	prev := DefaultStreamFn
	DefaultStreamFn = nil
	defer func() { DefaultStreamFn = prev }()
	a := NewAgent(AgentOptions{Model: testModel()})
	require.NoError(t, a.Prompt("hi"))
	a.WaitForIdle()
	assert.Contains(t, a.State().Error, "no stream function configured")
}

// TestRunLoop_ThinkingLevelAndToolEvents drives a full agent prompt with a
// thinking level set and a tool call, covering the reasoning branch and the
// EventToolExecutionStart/End handling in runLoop.
func TestRunLoop_ThinkingLevelAndToolEvents(t *testing.T) {
	executed := make(chan struct{}, 1)
	a := NewAgent(AgentOptions{
		Model:         testModel(),
		ThinkingLevel: ThinkingMedium,
		StreamFn: mockStreamFn(
			toolCallResponse("echo", "t1", map[string]any{"v": "1"}),
			simpleResponse("done"),
		),
	})
	a.UpdateTools(func(ts *ToolSet) {
		ts.Add(AgentTool{
			Tool: core.Tool{Name: "echo"},
			Execute: func(_ context.Context, _ string, params map[string]any, _ AgentToolUpdateCallback) (AgentToolResult, error) {
				select {
				case executed <- struct{}{}:
				default:
				}
				return AgentToolResult{Content: []core.ToolResultContent{{Type: core.ContentTypeText, Text: "ok"}}}, nil
			},
		})
	})
	require.NoError(t, a.Prompt("use echo"))
	a.WaitForIdle()
	select {
	case <-executed:
	default:
		t.Fatal("tool was not executed")
	}
	assert.False(t, a.State().IsStreaming)
}

// TestRunLoop_TurnEndErrorRecorded covers the EventTurnEnd error capture: an
// assistant turn that ends with a non-retryable error message records it on
// agent state.
func TestRunLoop_TurnEndErrorRecorded(t *testing.T) {
	errMsg := "400 invalid request"
	a := NewAgent(AgentOptions{
		Model:    testModel(),
		StreamFn: mockStreamFn(transportError("", errMsg)),
	})
	require.NoError(t, a.Prompt("go"))
	a.WaitForIdle()
	assert.Contains(t, a.State().Error, "invalid request")
}

// TestAssistantMessageHasContent covers every content-kind branch of the helper.
func TestAssistantMessageHasContent(t *testing.T) {
	assert.False(t, assistantMessageHasContent(nil))
	assert.False(t, assistantMessageHasContent(&core.AssistantMessage{}))
	// Empty text block -> not content.
	assert.False(t, assistantMessageHasContent(&core.AssistantMessage{
		Content: []core.AssistantContent{core.NewTextContent("")},
	}))
	// Non-empty text.
	assert.True(t, assistantMessageHasContent(&core.AssistantMessage{
		Content: []core.AssistantContent{core.NewTextContent("hi")},
	}))
	// Thinking with text.
	assert.True(t, assistantMessageHasContent(&core.AssistantMessage{
		Content: []core.AssistantContent{core.NewThinkingContent("t")},
	}))
	// Thinking with signature only.
	assert.True(t, assistantMessageHasContent(&core.AssistantMessage{
		Content: []core.AssistantContent{{Thinking: &core.ThinkingContent{ThinkingSignature: "sig"}}},
	}))
	// Thinking redacted only.
	assert.True(t, assistantMessageHasContent(&core.AssistantMessage{
		Content: []core.AssistantContent{{Thinking: &core.ThinkingContent{Redacted: true}}},
	}))
	// Tool call with a name.
	assert.True(t, assistantMessageHasContent(&core.AssistantMessage{
		Content: []core.AssistantContent{core.NewToolCallContent("id", "tool", nil)},
	}))
}

// partialOnlyStreamFn emits a message start + a delta but no Done event, so the
// agent loop ends the turn (unclean stream end) without a MessageEnd — leaving
// a partial assistant message in runLoop for the partial-tail handler.
func partialOnlyStreamFn(partial *core.AssistantMessage) StreamFn {
	return func(_ *core.Model, _ core.Context, _ *core.SimpleStreamOptions) *core.AssistantMessageEventStream {
		s := core.NewAssistantMessageEventStream()
		go func() {
			s.Push(core.AssistantMessageEvent{Type: core.EventStart, Partial: partial})
			s.Push(core.AssistantMessageEvent{Type: core.EventTextDelta, Partial: partial})
			s.End(nil)
		}()
		return s
	}
}

// TestRunLoop_LeftoverPartialAppended covers EventMessageStart/Update tracking
// and the partial-tail append in runLoop.
func TestRunLoop_LeftoverPartialAppended(t *testing.T) {
	a := NewAgent(AgentOptions{Model: testModel(), StreamFn: partialOnlyStreamFn(simpleResponse("partial text"))})
	require.NoError(t, a.Prompt("go"))
	a.WaitForIdle()
	msgs := a.State().Messages
	require.NotEmpty(t, msgs)
	assert.Equal(t, "partial text", msgs[len(msgs)-1].Text())
}

// TestRenderSimplePromptContent_MarshalError covers the json.Marshal error
// fallback for tool-call arguments (a channel is not JSON-serialisable).
func TestRenderSimplePromptContent_MarshalError(t *testing.T) {
	content := []core.AssistantContent{
		core.NewToolCallContent("id", "tool", map[string]any{"bad": make(chan int)}),
	}
	rendered, _, err := renderSimplePromptContent(content)
	require.NoError(t, err)
	assert.Contains(t, rendered, "[tool tool {}]")
}

// TestClampThinkingLevel_NoneAtOrBelow covers the final ThinkingOff return when
// the only available level sits above the requested one in the ladder.
func TestClampThinkingLevel_NoneAtOrBelow(t *testing.T) {
	assert.Equal(t, ThinkingOff, ClampThinkingLevel(ThinkingHigh, []ThinkingLevel{ThinkingMax}))
}
