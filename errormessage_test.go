package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kfet/ai"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// blankProviderError builds the exact defect shape observed in the wild: a
// provider that reports an error stop reason but carries neither an error
// message nor any content blocks. Downstream this is indistinguishable from a
// degenerate ("model chose silence") generation unless the invariant is
// enforced.
// testLoopConfig is the minimal loop config the boundary tests need.
func testLoopConfig() *AgentLoopConfig {
	return &AgentLoopConfig{Model: testModel(), ConvertToLLM: testConvertToLLM}
}

func blankProviderError() *ai.AssistantMessage {
	return &ai.AssistantMessage{
		Role:         "assistant",
		Content:      []ai.AssistantContent{},
		API:          ai.APIAnthropicMessages,
		Provider:     ai.ProviderAnthropic,
		Model:        "test-model",
		StopReason:   ai.StopReasonError,
		ErrorMessage: "",
		Timestamp:    time.Now().UnixMilli(),
	}
}

// TestEnsureErrorMessage_SynthesizesForBlankError covers the invariant fix: an
// error stop reason with no error text gets an honest, diagnosable substitute
// naming the provider, model and content blocks — and the substitute must not
// be classified as retryable.
func TestEnsureErrorMessage_SynthesizesForBlankError(t *testing.T) {
	orig := blankProviderError()
	got := ensureErrorMessage(orig)

	require.NotNil(t, got)
	assert.NotSame(t, orig, got, "must not mutate the provider-owned message in place")
	assert.Empty(t, orig.ErrorMessage, "original message left untouched")

	assert.NotEmpty(t, got.ErrorMessage)
	assert.Contains(t, got.ErrorMessage, "stop_reason=error")
	assert.Contains(t, got.ErrorMessage, string(ai.ProviderAnthropic))
	assert.Contains(t, got.ErrorMessage, "test-model")
	assert.Contains(t, got.ErrorMessage, "blocks: []")
	assert.False(t, ai.IsRetryableError(got.ErrorMessage),
		"a synthesised error must be terminal, never re-rolled as a transport blip")
	assert.Equal(t, ai.StopReasonError, got.StopReason)
}

// TestEnsureErrorMessage_SummarizesBlocks covers the branch-summary path when
// the errored message did carry (unusable) content.
func TestEnsureErrorMessage_SummarizesBlocks(t *testing.T) {
	m := blankProviderError()
	tc := ai.NewThinkingContent("")
	tc.Thinking.ThinkingSignature = "sig"
	m.Content = []ai.AssistantContent{tc}
	got := ensureErrorMessage(m)
	assert.Contains(t, got.ErrorMessage, "thinking(th=0,sig=3)")
}

// TestEnsureErrorMessage_PassThrough covers every case the guard must leave
// exactly as-is: nil, a non-error stop reason, and an error that already
// carries text.
func TestEnsureErrorMessage_PassThrough(t *testing.T) {
	assert.Nil(t, ensureErrorMessage(nil))

	ok := simpleResponse("hello")
	assert.Same(t, ok, ensureErrorMessage(ok), "non-error stop reason untouched")

	withText := transportError("", connResetErr)
	assert.Same(t, withText, ensureErrorMessage(withText), "existing error text preserved")
	assert.Equal(t, connResetErr, ensureErrorMessage(withText).ErrorMessage)
}

// TestEnsureErrorMessage_WhitespaceOnly covers a provider that sets an error
// message consisting only of whitespace — as useless as an empty one.
func TestEnsureErrorMessage_WhitespaceOnly(t *testing.T) {
	m := blankProviderError()
	m.ErrorMessage = "  \n\t "
	got := ensureErrorMessage(m)
	assert.Contains(t, got.ErrorMessage, "no error message")
}

// TestStreamAssistantResponse_BlankProviderErrorGetsMessage verifies the
// invariant is enforced at the agent boundary: the message returned AND the
// one appended to the agent context both carry diagnosable error text.
func TestStreamAssistantResponse_BlankProviderErrorGetsMessage(t *testing.T) {
	agentCtx := baseCtx()
	msg := runStream(context.Background(), agentCtx, testLoopConfig(), mockStreamFn(blankProviderError()))

	require.NotNil(t, msg)
	assert.Equal(t, ai.StopReasonError, msg.StopReason)
	assert.NotEmpty(t, msg.ErrorMessage, "boundary must never emit a blank error message")
	assert.Contains(t, msg.ErrorMessage, "stop_reason=error")

	last := agentCtx.Messages[len(agentCtx.Messages)-1].Message.AsAssistant()
	require.NotNil(t, last)
	assert.Equal(t, msg.ErrorMessage, last.ErrorMessage,
		"the message stored in agent history must satisfy the invariant too")
}

// TestStreamAssistantResponse_BlankErrorViaErrorEvent covers the same shape
// arriving as an EventError (rather than EventDone) with no EventStart before
// it — the "provider rejected the request outright" path.
func TestStreamAssistantResponse_BlankErrorViaErrorEvent(t *testing.T) {
	streamFn := func(model *ai.Model, c ai.Context, o *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
		s := ai.NewAssistantMessageEventStream()
		go func() {
			s.Push(ai.AssistantMessageEvent{
				Type:   ai.EventError,
				Reason: ai.StopReasonError,
				Error:  blankProviderError(),
			})
			s.End(nil)
		}()
		return s
	}
	msg := runStream(context.Background(), baseCtx(), testLoopConfig(), streamFn)
	require.NotNil(t, msg)
	assert.NotEmpty(t, msg.ErrorMessage)
}

// TestSimplePrompt_BlankErrorFailsOnFirstAttempt is the regression test for the
// reported bug: a provider error with StopReasonError + empty ErrorMessage +
// zero content blocks must surface a diagnosable error after exactly ONE
// attempt, not three, and must not be reported as "no usable content".
func TestSimplePrompt_BlankErrorFailsOnFirstAttempt(t *testing.T) {
	calls := 0
	streamFn := func(model *ai.Model, c ai.Context, o *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
		calls++
		s := ai.NewAssistantMessageEventStream()
		msg := blankProviderError()
		go func() {
			s.Push(ai.AssistantMessageEvent{Type: ai.EventStart, Partial: msg})
			s.Push(ai.AssistantMessageEvent{Type: ai.EventError, Reason: msg.StopReason, Error: msg})
			s.End(nil)
		}()
		return s
	}
	a := NewAgent(AgentOptions{
		InitialState: &AgentState{Model: testModel(), ThinkingLevel: ThinkingMedium},
		StreamFn:     streamFn,
	})

	text, err := a.SimplePrompt(context.Background(), []AgentMessage{
		NewAgentMessage(ai.NewUserMsg("advise me", time.Now().UnixMilli())),
	}, nil)

	require.Error(t, err)
	assert.Empty(t, text)
	assert.Equal(t, 1, calls, "a provider error must not be re-rolled as a degenerate generation")
	assert.Contains(t, err.Error(), "stop_reason=error")
	assert.Contains(t, err.Error(), "blocks: []")
	assert.NotContains(t, err.Error(), "no usable content",
		"a real provider error must not be flattened into the degenerate-content message")
}

// TestSideQuery_BlankErrorSurfacesDiagnosis checks the same guarantee through
// the exported side-query entry point that downstream callers (advisor chains)
// actually use to classify failures.
func TestSideQuery_BlankErrorSurfacesDiagnosis(t *testing.T) {
	a := NewAgent(AgentOptions{
		InitialState: &AgentState{Model: testModel()},
		StreamFn:     mockStreamFn(blankProviderError()),
	})

	_, err := a.SimplePrompt(context.Background(), []AgentMessage{
		NewAgentMessage(ai.NewUserMsg("advise me", time.Now().UnixMilli())),
	}, nil)
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "provider="),
		"caller must be able to tell which provider failed, got: %v", err)
}
