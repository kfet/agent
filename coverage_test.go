package agent

import (
	"testing"

	"github.com/kfet/ai"
	"github.com/stretchr/testify/assert"
)

// TestToolSet_NilReceivers exercises every nil-receiver guard so the
// zero-value-safe contract is covered.
func TestToolSet_NilReceivers(t *testing.T) {
	var ts *ToolSet
	assert.Equal(t, 0, ts.Len())
	assert.Nil(t, ts.Slice())
	assert.Nil(t, ts.Names())
	assert.False(t, ts.Has("x"))
	assert.Nil(t, ts.Clone())
	_, ok := ts.Get("x")
	assert.False(t, ok)
	ts.Remove("x") // no panic
}

// TestToolSet_RemoveAbsent covers the early return when the name is not present.
func TestToolSet_RemoveAbsent(t *testing.T) {
	ts := NewToolSet()
	ts.Add(AgentTool{Tool: ai.Tool{Name: "keep"}})
	ts.Remove("missing") // present-check fails -> early return
	assert.True(t, ts.Has("keep"))
	assert.Equal(t, []string{"keep"}, ts.Names())
}

// TestToolSet_HasAndNames covers the populated (non-nil) paths.
func TestToolSet_HasAndNames(t *testing.T) {
	ts := NewToolSet()
	ts.Add(AgentTool{Tool: ai.Tool{Name: "a"}})
	ts.Add(AgentTool{Tool: ai.Tool{Name: "b"}})
	assert.True(t, ts.Has("a"))
	assert.False(t, ts.Has("z"))
	assert.Equal(t, []string{"a", "b"}, ts.Names())
}

// TestAgent_TransportAccessors covers Get/SetTransport.
func TestAgent_TransportAccessors(t *testing.T) {
	a := NewAgent(AgentOptions{})
	a.SetTransport(ai.TransportWebSocket)
	assert.Equal(t, ai.TransportWebSocket, a.GetTransport())
}

// TestAgent_SetStreamFn covers the per-instance stream function override.
func TestAgent_SetStreamFn(t *testing.T) {
	a := NewAgent(AgentOptions{})
	a.SetStreamFn(mockStreamFn(simpleResponse("x")))
	a.mu.Lock()
	got := a.streamFn != nil
	a.mu.Unlock()
	assert.True(t, got)
}

// TestAgent_SetServerToolsAndCompaction covers the two Anthropic-specific setters.
func TestAgent_SetServerToolsAndCompaction(t *testing.T) {
	a := NewAgent(AgentOptions{})
	a.SetServerTools([]ai.AnthropicServerTool{{}})
	a.SetCompaction(&ai.AnthropicCompaction{})
	a.mu.Lock()
	defer a.mu.Unlock()
	assert.Len(t, a.serverTools, 1)
	assert.NotNil(t, a.compaction)
}

// TestAgent_FollowUpQueueClearAndDrain covers ClearFollowUpQueue and
// GetAndClearFollowUpQueue.
func TestAgent_FollowUpQueueClearAndDrain(t *testing.T) {
	a := NewAgent(AgentOptions{})
	a.FollowUp(NewAgentMessage(ai.NewUserMsg("one", 0)))
	a.FollowUp(NewAgentMessage(ai.NewUserMsg("two", 0)))

	drained := a.GetAndClearFollowUpQueue()
	assert.Len(t, drained, 2)
	assert.Equal(t, 0, a.FollowUpQueueLen())

	// Clear on an already-empty queue is a no-op.
	a.FollowUp(NewAgentMessage(ai.NewUserMsg("three", 0)))
	a.ClearFollowUpQueue()
	assert.Equal(t, 0, a.FollowUpQueueLen())
}
