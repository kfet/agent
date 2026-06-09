package agent

import (
	"testing"

	core "github.com/kfet/ai"
	"github.com/stretchr/testify/assert"
)

func thinkingResponse(thinking, sig string) *core.AssistantMessage {
	return &core.AssistantMessage{
		Role: "assistant",
		Content: []core.AssistantContent{
			{Thinking: &core.ThinkingContent{Type: "thinking", Thinking: thinking, ThinkingSignature: sig}},
		},
		StopReason: core.StopReasonError,
	}
}

// TestHasReplayableContent_Thinking covers the thinking branch (signature and
// trimmed text) plus the all-empty thinking case.
func TestHasReplayableContent_Thinking(t *testing.T) {
	assert.True(t, hasReplayableContent(thinkingResponse("pondering", "")))
	assert.True(t, hasReplayableContent(thinkingResponse("", "sig123")))
	assert.False(t, hasReplayableContent(thinkingResponse("   ", "")))
}

// TestSanitizeTrailingError covers all three paths: empty, trailing error
// assistant (rewritten), and trailing non-error (untouched).
func TestSanitizeTrailingError(t *testing.T) {
	sanitizeTrailingError(nil) // n == 0, no panic

	withErr := []AgentMessage{NewAgentMessage(core.NewAssistantMsg(*transportError("hi", "boom")))}
	sanitizeTrailingError(withErr)
	a := withErr[0].Message.AsAssistant()
	assert.Equal(t, core.StopReasonStop, a.StopReason)
	assert.Equal(t, "", a.ErrorMessage)

	clean := []AgentMessage{NewAgentMessage(core.NewAssistantMsg(*simpleResponse("ok")))}
	sanitizeTrailingError(clean)
	assert.Equal(t, core.StopReasonStop, clean[0].Message.AsAssistant().StopReason)

	// Trailing non-assistant message is a no-op.
	user := []AgentMessage{NewAgentMessage(core.NewUserMsg("hi", 0))}
	sanitizeTrailingError(user)
	assert.NotNil(t, user[0].Message.AsUser())
}

// TestDropTrailingErrorMessage covers empty, drop, and keep paths.
func TestDropTrailingErrorMessage(t *testing.T) {
	assert.Empty(t, dropTrailingErrorMessage(nil))

	withErr := []AgentMessage{
		NewAgentMessage(core.NewUserMsg("hi", 0)),
		NewAgentMessage(core.NewAssistantMsg(*transportError("", "boom"))),
	}
	assert.Len(t, dropTrailingErrorMessage(withErr), 1)

	clean := []AgentMessage{NewAgentMessage(core.NewAssistantMsg(*simpleResponse("ok")))}
	assert.Len(t, dropTrailingErrorMessage(clean), 1)
}

// TestDropTrailingPartial covers empty, removal of an incomplete tool_use
// partial, and the keep path for a complete message.
func TestDropTrailingPartial(t *testing.T) {
	empty := &AgentContext{}
	dropTrailingPartial(empty) // n == 0

	partial := &AgentContext{Messages: []AgentMessage{
		NewAgentMessage(core.NewAssistantMsg(*partialToolCallError("mytool", "thinking..."))),
	}}
	dropTrailingPartial(partial)
	assert.Empty(t, partial.Messages)

	complete := &AgentContext{Messages: []AgentMessage{
		NewAgentMessage(core.NewAssistantMsg(*simpleResponse("done"))),
	}}
	dropTrailingPartial(complete)
	assert.Len(t, complete.Messages, 1)
}

// TestStreamErrorNote covers the empty-message default branch and the
// passthrough branch.
func TestStreamErrorNote(t *testing.T) {
	assert.Contains(t, streamErrorNote(""), "unknown stream error")
	assert.Contains(t, streamErrorNote("broken pipe"), "broken pipe")
}

// TestFoldStreamErrorNoteIntoFirstUser covers success and all three false
// branches.
func TestFoldStreamErrorNoteIntoFirstUser(t *testing.T) {
	// Empty slice.
	assert.False(t, foldStreamErrorNoteIntoFirstUser(nil, "note"))

	// First message not user-role.
	notUser := []AgentMessage{NewAgentMessage(core.NewAssistantMsg(*simpleResponse("x")))}
	assert.False(t, foldStreamErrorNoteIntoFirstUser(notUser, "note"))

	// User content is not a plain string.
	blocks := []AgentMessage{NewAgentMessage(core.NewUserMsg([]any{"block"}, 0))}
	assert.False(t, foldStreamErrorNoteIntoFirstUser(blocks, "note"))

	// Success.
	ok := []AgentMessage{NewAgentMessage(core.NewUserMsg("original", 7))}
	assert.True(t, foldStreamErrorNoteIntoFirstUser(ok, "NOTE"))
	u := ok[0].Message.AsUser()
	assert.Equal(t, "NOTE\n\noriginal", u.Content)
	assert.Equal(t, int64(7), u.Timestamp)
}

// TestErrorAssistantMessage covers the constructor.
func TestErrorAssistantMessage(t *testing.T) {
	m := errorAssistantMessage(testModel(), "boom")
	assert.Equal(t, core.StopReasonError, m.StopReason)
	assert.Equal(t, "boom", m.ErrorMessage)
	assert.Equal(t, testModel().ID, m.Model)
}
