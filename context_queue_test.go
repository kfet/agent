package agent

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kfet/ai"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// requestRecorder records, per LLM request, the (role, text) of every message.
type requestRecorder struct {
	mu       sync.Mutex
	requests [][]string
}

func (r *requestRecorder) transform(_ context.Context, msgs []AgentMessage) ([]AgentMessage, error) {
	var req []string
	for i := range msgs {
		req = append(req, msgs[i].Role()+":"+msgText(msgs[i]))
	}
	r.mu.Lock()
	r.requests = append(r.requests, req)
	r.mu.Unlock()
	return msgs, nil
}

func newCtxAgent(rec *requestRecorder, exec func(), responses ...*ai.AssistantMessage) *Agent {
	a := NewAgent(AgentOptions{
		Model:            testModel(),
		StreamFn:         mockStreamFn(responses...),
		TransformContext: rec.transform,
	})
	a.UpdateTools(func(ts *ToolSet) {
		ts.Add(AgentTool{
			Tool: ai.Tool{Name: "inject"},
			Execute: func(context.Context, string, map[string]any, AgentToolUpdateCallback) (AgentToolResult, error) {
				exec()
				return AgentToolResult{Content: []ai.ToolResultContent{{Type: "text", Text: "tool out"}}}, nil
			},
		})
	})
	return a
}

func ctxMsg(text string) AgentMessage {
	return NewAgentMessage(ai.NewUserMsg(text, time.Now().UnixMilli()))
}

func TestAppendContext_IdleAppendsImmediately(t *testing.T) {
	a := NewAgent(AgentOptions{})
	assert.False(t, a.AppendContext(ctxMsg("c")))
	require.Len(t, a.State().Messages, 1)
	assert.Empty(t, a.PeekContextQueue())
}

// Mid-run context arrives as its own user message after the tool results and
// before the next request; the tool result itself is untouched.
func TestAppendContext_MidRunDeliveredBeforeNextRequest(t *testing.T) {
	rec := &requestRecorder{}
	var a *Agent
	var queued bool
	a = newCtxAgent(rec, func() { queued = a.AppendContext(ctxMsg("CTX")) },
		toolCallResponse("inject", "t1", map[string]any{}), simpleResponse("done"))
	var ends []string
	a.Subscribe(func(e AgentEvent) {
		if e.Type == EventMessageEnd && e.Message.Role() == "user" {
			ends = append(ends, msgText(*e.Message))
		}
	})
	require.NoError(t, a.Prompt("go"))
	a.WaitForIdle()

	assert.True(t, queued)
	require.Len(t, rec.requests, 2)
	assert.Equal(t, []string{"user:go", "assistant:", "toolResult:tool out", "user:CTX"}, rec.requests[1])
	assert.Equal(t, []string{"go", "CTX"}, ends, "context announced exactly once")
	assert.Empty(t, a.PeekContextQueue())
}

// Context that arrives during the final answer must not cause another request.
func TestAppendContext_NoExtraTurnAfterFinalAnswer(t *testing.T) {
	rec := &requestRecorder{}
	a := newCtxAgent(rec, func() {}, simpleResponse("answer"))
	var once sync.Once
	a.Subscribe(func(e AgentEvent) {
		if e.Type == EventTurnEnd {
			once.Do(func() { assert.True(t, a.AppendContext(ctxMsg("LATE"))) })
		}
	})
	require.NoError(t, a.Prompt("go"))
	a.WaitForIdle()

	assert.Len(t, rec.requests, 1, "no extra turn")
	msgs := a.State().Messages
	require.Len(t, msgs, 3)
	assert.Equal(t, "LATE", msgText(msgs[2]))
	assert.Empty(t, a.PeekContextQueue())
}

// AppendContext racing the end of the run: every message ends up in history
// exactly once and is announced exactly once.
func TestAppendContext_RaceWithAgentEnd(t *testing.T) {
	for i := 0; i < 50; i++ {
		a := NewAgent(AgentOptions{Model: testModel(), StreamFn: mockStreamFn(simpleResponse("x"))})
		var mu sync.Mutex
		announced := 0
		a.Subscribe(func(e AgentEvent) {
			if e.Type == EventMessageEnd && msgText(*e.Message) == "R" {
				mu.Lock()
				announced++
				mu.Unlock()
			}
		})
		var queued bool
		done := make(chan struct{})
		require.NoError(t, a.Prompt("go"))
		go func() { queued = a.AppendContext(ctxMsg("R")); close(done) }()
		<-done
		a.WaitForIdle()
		n := 0
		for _, m := range a.State().Messages {
			if msgText(m) == "R" {
				n++
			}
		}
		assert.Equal(t, 1, n)
		mu.Lock()
		if queued {
			assert.Equal(t, 1, announced)
		} else {
			assert.Equal(t, 0, announced)
		}
		mu.Unlock()
		assert.Empty(t, a.PeekContextQueue())
	}
}

func TestReset_ClearsContextQueue(t *testing.T) {
	a := NewAgent(AgentOptions{})
	a.mu.Lock()
	a.contextQueue = []AgentMessage{ctxMsg("x")}
	a.mu.Unlock()
	a.Reset()
	assert.Empty(t, a.PeekContextQueue())
}

// msgText returns the plain text of a user, assistant or tool-result message.
func msgText(m AgentMessage) string {
	if u := m.AsUser(); u != nil {
		if s, ok := u.Content.(string); ok {
			return s
		}
		return ""
	}
	if r := m.AsToolResult(); r != nil {
		var b strings.Builder
		for _, c := range r.Content {
			b.WriteString(c.Text)
		}
		return b.String()
	}
	return m.Text()
}
