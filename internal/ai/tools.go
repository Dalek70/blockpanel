package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"blockpanel/internal/mc"
	"blockpanel/internal/store"
	"blockpanel/internal/util"
)

const (
	maxToolFileRead = 64 * 1024 // bytes of file content fed back to the model
	maxDirEntries   = 300
)

// Approval describes a pending state-changing tool call awaiting the user's
// yes/no in the UI.
type Approval struct {
	ID         string `json:"id"`
	Tool       string `json:"tool"`
	Summary    string `json:"summary"`
	Path       string `json:"path,omitempty"`
	Command    string `json:"command,omitempty"`
	OldContent string `json:"old_content,omitempty"`
	NewContent string `json:"new_content,omitempty"`
	Exists     bool   `json:"exists"`
}

// ToolEnv is everything a tool execution needs. Permission checks run against
// the requesting user's per-server permissions, so the agent can never do
// more than the human driving it.
type ToolEnv struct {
	Instance        *mc.Instance
	Server          *store.Server
	HasPerm         func(perm string) bool
	WebSearch       bool
	RequestApproval func(ctx context.Context, ap *Approval) (bool, error)
	Audit           func(action, detail string)
}

func fn(name, desc string, params map[string]any) Tool {
	var t Tool
	t.Type = "function"
	t.Function.Name = name
	t.Function.Description = desc
	if params == nil {
		params = map[string]any{"type": "object", "properties": map[string]any{}}
	}
	t.Function.Parameters = params
	return t
}

func obj(required []string, props map[string]any) map[string]any {
	m := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		m["required"] = required
	}
	return m
}

// AgentTools returns the tool schema advertised to the model.
func AgentTools(webSearch bool) []Tool {
	tools := []Tool{
		fn("get_console", "Read the most recent lines from the server console.",
			obj(nil, map[string]any{
				"lines": map[string]any{"type": "integer", "description": "How many lines (1-512, default 100)."},
			})),
		fn("search_console", "Search the in-memory console history for lines containing a string (case-insensitive).",
			obj([]string{"query"}, map[string]any{
				"query": map[string]any{"type": "string"},
				"max":   map[string]any{"type": "integer", "description": "Max matches (default 50)."},
			})),
		fn("server_status", "Get server state: running/stopped, uptime, CPU, memory, configured jar and port.", nil),
		fn("list_dir", "List a directory inside the server folder. Path is relative to the server root; use \"\" or \".\" for the root.",
			obj([]string{"path"}, map[string]any{
				"path": map[string]any{"type": "string"},
			})),
		fn("read_file", "Read a text file inside the server folder (configs, logs, properties). Not for binary files.",
			obj([]string{"path"}, map[string]any{
				"path": map[string]any{"type": "string"},
			})),
		fn("write_file", "Write a text file inside the server folder. The panel ASKS THE USER FOR APPROVAL before anything is written. Always read the current file first and write the complete new content.",
			obj([]string{"path", "content"}, map[string]any{
				"path":    map[string]any{"type": "string"},
				"content": map[string]any{"type": "string", "description": "Complete new file content."},
			})),
		fn("send_command", "Send a command to the running server console (without leading slash). The panel ASKS THE USER FOR APPROVAL before sending.",
			obj([]string{"command"}, map[string]any{
				"command": map[string]any{"type": "string"},
			})),
	}
	if webSearch {
		tools = append(tools, fn("web_search", "Search the web (DuckDuckGo) for Minecraft server errors, plugin/mod docs and compatibility info. Returns titles, URLs and snippets.",
			obj([]string{"query"}, map[string]any{
				"query": map[string]any{"type": "string"},
			})))
	}
	return tools
}

// ExecuteTool runs one tool call and returns the text fed back to the model.
// Errors are returned as text so the model can react; they never abort the
// agent loop.
func ExecuteTool(ctx context.Context, env *ToolEnv, call ToolCall) string {
	args := map[string]any{}
	if s := strings.TrimSpace(call.Function.Arguments); s != "" {
		if err := json.Unmarshal([]byte(s), &args); err != nil {
			return "ERROR: tool arguments were not valid JSON: " + err.Error()
		}
	}
	str := func(k string) string {
		v, _ := args[k].(string)
		return v
	}
	num := func(k string, def int) int {
		if v, ok := args[k].(float64); ok {
			return int(v)
		}
		return def
	}

	switch call.Function.Name {
	case "get_console":
		if !env.HasPerm(store.SPermConsoleView) {
			return "permission denied: console.view"
		}
		n := num("lines", 100)
		if n < 1 {
			n = 1
		}
		if n > 512 {
			n = 512
		}
		out := env.Instance.Console().LastText(n)
		if out == "" {
			return "(console is empty)"
		}
		return out

	case "search_console":
		if !env.HasPerm(store.SPermConsoleView) {
			return "permission denied: console.view"
		}
		q := str("query")
		if q == "" {
			return "ERROR: query is required"
		}
		lines := env.Instance.Console().Search(q, num("max", 50))
		if len(lines) == 0 {
			return "no matching lines"
		}
		var b strings.Builder
		for _, l := range lines {
			fmt.Fprintf(&b, "%s %s\n", l.T.Format("15:04:05"), l.Text)
		}
		return b.String()

	case "server_status":
		if !env.HasPerm(store.SPermView) {
			return "permission denied: view"
		}
		st := env.Instance.Stats()
		cfg := env.Instance.Config()
		return fmt.Sprintf("state: %s\nuptime: %s\ncpu: %.1f%%\nmemory: %.0f MB (max configured %d MB)\njar: %s\nport: %d\nauto-restart: %v",
			env.Instance.State(),
			env.Instance.Uptime().Round(time.Second),
			st.CPUPercent, st.RSSMB, cfg.MaxMemMB, cfg.Jar, mc.ReadServerPort(cfg.Root), cfg.AutoRestart)

	case "list_dir":
		if !env.HasPerm(store.SPermFilesView) {
			return "permission denied: files.view"
		}
		entries, err := mc.ListDir(env.Server.Root, str("path"))
		if err != nil {
			return "ERROR: " + err.Error()
		}
		if len(entries) == 0 {
			return "(empty directory)"
		}
		var b strings.Builder
		for i, e := range entries {
			if i >= maxDirEntries {
				fmt.Fprintf(&b, "… %d more entries truncated\n", len(entries)-maxDirEntries)
				break
			}
			if e.IsDir {
				fmt.Fprintf(&b, "%s/\n", e.Name)
			} else {
				fmt.Fprintf(&b, "%s  (%s)\n", e.Name, util.HumanBytes(e.Size))
			}
		}
		return b.String()

	case "read_file":
		if !env.HasPerm(store.SPermFilesView) {
			return "permission denied: files.view"
		}
		content, err := mc.ReadTextFile(env.Server.Root, str("path"))
		if err != nil {
			return "ERROR: " + err.Error()
		}
		if len(content) > maxToolFileRead {
			return content[:maxToolFileRead] + "\n…[truncated: file is larger than the 64KB tool limit]"
		}
		if content == "" {
			return "(file is empty)"
		}
		return content

	case "write_file":
		if !env.HasPerm(store.SPermFilesEdit) {
			return "permission denied: files.edit"
		}
		path, content := str("path"), str("content")
		if path == "" {
			return "ERROR: path is required"
		}
		old, readErr := mc.ReadTextFile(env.Server.Root, path)
		exists := readErr == nil
		ap := &Approval{
			ID:         util.NewID(),
			Tool:       "write_file",
			Summary:    fmt.Sprintf("Write %d bytes to %s", len(content), path),
			Path:       path,
			OldContent: truncate(old, 16*1024),
			NewContent: truncate(content, 16*1024),
			Exists:     exists,
		}
		ok, err := env.RequestApproval(ctx, ap)
		if err != nil {
			return "ERROR: approval failed: " + err.Error()
		}
		if !ok {
			return "The user DENIED this write. Do not retry it; ask the user how to proceed."
		}
		if err := mc.WriteTextFile(env.Server.Root, path, content); err != nil {
			return "ERROR: " + err.Error()
		}
		env.Audit("ai.agent.write_file", path)
		return "written: " + path

	case "send_command":
		if !env.HasPerm(store.SPermConsoleSend) {
			return "permission denied: console.send"
		}
		command := strings.TrimSpace(str("command"))
		if command == "" {
			return "ERROR: command is required"
		}
		ap := &Approval{
			ID:      util.NewID(),
			Tool:    "send_command",
			Summary: "Send console command: " + command,
			Command: command,
		}
		ok, err := env.RequestApproval(ctx, ap)
		if err != nil {
			return "ERROR: approval failed: " + err.Error()
		}
		if !ok {
			return "The user DENIED this command. Do not retry it; ask the user how to proceed."
		}
		if err := env.Instance.SendCommand(command); err != nil {
			return "ERROR: " + err.Error()
		}
		env.Audit("ai.agent.send_command", command)
		return "command sent: " + command

	case "web_search":
		if !env.WebSearch {
			return "web search is disabled in the panel's AI settings"
		}
		q := str("query")
		if q == "" {
			return "ERROR: query is required"
		}
		cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		results, err := WebSearch(cctx, q)
		if err != nil {
			return "ERROR: " + err.Error()
		}
		env.Audit("ai.agent.web_search", q)
		return FormatSearchResults(results)
	}
	return "ERROR: unknown tool " + call.Function.Name
}
