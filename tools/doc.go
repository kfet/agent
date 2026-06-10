// Package tools provides the standard coding toolbox for the agent
// runtime: bash, read, write, edit, editdiff, find, grep, imageresize,
// and plan.
//
// Each constructor returns an [github.com/kfet/agent.AgentTool] that can
// be registered on a ToolSet. Tools that touch the filesystem resolve
// paths relative to a working directory supplied at construction time,
// and tool errors are returned as LLM-facing payloads rather than
// host-level failures.
package tools
