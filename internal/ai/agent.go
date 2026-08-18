package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"blockpanel/internal/store"
)

const (
	approvalTimeout = 5 * time.Minute
	maxHistory      = 60 // stored messages per conversation
	// maxHistoryBytes bounds a conversation by size as well as by message
	// count: a single message can be ~1 MiB, so a count-only cap still let
	// one user pin tens of MiB per server for the process lifetime.
	maxHistoryBytes = 256 * 1024
	// maxConversations bounds total retained conversations across all users;
	// nothing else evicts them (not user deletion, not server deletion).
	maxConversations = 200
)

// Event is one item on the agent's SSE stream.
type Event struct {
	Type       string          `json:"type"` // reasoning|content|tool_call|tool_result|approval_required|approval_resolved|done|error
	Text       string          `json:"text,omitempty"`
	Tool       string          `json:"tool,omitempty"`
	Args       json.RawMessage `json:"args,omitempty"`
	Result     string          `json:"result,omitempty"`
	Approval   *Approval       `json:"approval,omitempty"`
	ApprovalID string          `json:"approval_id,omitempty"`
	Approved   bool            `json:"approved,omitempty"`
	Error      string          `json:"error,omitempty"`
}

type EmitFunc func(Event)

// Runner holds per-(user,server) agent conversations and pending approvals.
type Runner struct {
	mu        sync.Mutex
	convs     map[string][]Message
	running   map[string]bool
	approvals map[string]*pendingApproval
}

// pendingApproval binds a waiting approval to the user who started the agent
// run, so only that user can resolve it (defense in depth on top of the
// unguessable 128-bit approval ID).
type pendingApproval struct {
	ch     chan bool
	userID string
}

func NewRunner() *Runner {
	return &Runner{
		convs:     map[string][]Message{},
		running:   map[string]bool{},
		approvals: map[string]*pendingApproval{},
	}
}

func ConvKey(userID, serverID string) string { return userID + "|" + serverID }

func userIDFromKey(key string) string {
	if i := strings.IndexByte(key, '|'); i >= 0 {
		return key[:i]
	}
	return key
}

func (r *Runner) ResetConv(key string) {
	r.mu.Lock()
	delete(r.convs, key)
	r.mu.Unlock()
}

// Decide resolves a pending approval from the HTTP layer. userID must match
// the user who started the run that created the approval.
func (r *Runner) Decide(approvalID string, approve bool, userID string) error {
	r.mu.Lock()
	p, ok := r.approvals[approvalID]
	if ok && p.userID == userID {
		delete(r.approvals, approvalID)
	}
	r.mu.Unlock()
	if !ok {
		return errors.New("no such pending approval (it may have timed out)")
	}
	if p.userID != userID {
		return errors.New("this approval belongs to another user's session")
	}
	p.ch <- approve
	return nil
}

// requestApproval emits approval_required and blocks until the user decides,
// the context dies, or the timeout passes (= deny).
func (r *Runner) requestApproval(ctx context.Context, emit EmitFunc, userID string, ap *Approval) (bool, error) {
	ch := make(chan bool, 1)
	r.mu.Lock()
	r.approvals[ap.ID] = &pendingApproval{ch: ch, userID: userID}
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		delete(r.approvals, ap.ID)
		r.mu.Unlock()
	}()

	emit(Event{Type: "approval_required", Approval: ap})
	select {
	case ok := <-ch:
		emit(Event{Type: "approval_resolved", ApprovalID: ap.ID, Approved: ok})
		return ok, nil
	case <-ctx.Done():
		return false, ctx.Err()
	case <-time.After(approvalTimeout):
		emit(Event{Type: "approval_resolved", ApprovalID: ap.ID, Approved: false})
		return false, nil
	}
}

// RunAgent executes one agent turn: user message in, streamed events out.
// The conversation persists in memory per (user, server) until reset.
func (r *Runner) RunAgent(ctx context.Context, client *Client, cfg store.AISettings, env *ToolEnv, key, userMsg string, emit EmitFunc) error {
	r.mu.Lock()
	if r.running[key] {
		r.mu.Unlock()
		return errors.New("an agent run is already in progress for this server")
	}
	r.running[key] = true
	history := append([]Message{}, r.convs[key]...)
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		delete(r.running, key)
		r.mu.Unlock()
	}()

	runUserID := userIDFromKey(key)
	env.RequestApproval = func(ctx context.Context, ap *Approval) (bool, error) {
		return r.requestApproval(ctx, emit, runUserID, ap)
	}

	sys := Message{Role: "system", Content: agentSystemPrompt(env.Server.Name, env.WebSearch)}
	messages := append([]Message{sys}, history...)
	messages = append(messages, Message{Role: "user", Content: userMsg})

	tools := AgentTools(env.WebSearch)
	onDelta := func(kind, text string) {
		emit(Event{Type: kind, Text: text})
	}

	maxIter := cfg.AgentMaxIterations
	if maxIter <= 0 {
		maxIter = 10
	}

	for iter := 0; iter < maxIter; iter++ {
		msg, finish, err := client.Stream(ctx, messages, tools, onDelta)
		if err != nil {
			emit(Event{Type: "error", Error: err.Error()})
			return err
		}
		messages = append(messages, msg)
		if len(msg.ToolCalls) == 0 {
			break
		}
		_ = finish
		for _, tc := range msg.ToolCalls {
			emit(Event{Type: "tool_call", Tool: tc.Function.Name, Args: json.RawMessage(safeJSON(tc.Function.Arguments))})
			result := ExecuteTool(ctx, env, tc)
			if ctx.Err() != nil {
				return ctx.Err()
			}
			emit(Event{Type: "tool_result", Tool: tc.Function.Name, Result: truncate(result, 4000)})
			messages = append(messages, Message{Role: "tool", ToolCallID: tc.ID, Content: result})
		}
		if iter == maxIter-1 {
			note := fmt.Sprintf("[panel] agent stopped: reached the %d-iteration limit", maxIter)
			emit(Event{Type: "content", Text: "\n" + note})
			messages = append(messages, Message{Role: "assistant", Content: note})
		}
	}

	r.mu.Lock()
	if _, exists := r.convs[key]; !exists && len(r.convs) >= maxConversations {
		// Evict an arbitrary other conversation rather than growing without
		// bound; conversations are a convenience, not durable state.
		for k := range r.convs {
			if k != key {
				delete(r.convs, k)
				break
			}
		}
	}
	r.convs[key] = trimHistory(messages[1:]) // drop system prompt
	r.mu.Unlock()
	emit(Event{Type: "done"})
	return nil
}

// RunAsk executes the stateless "ask about the logs" flow: the question plus
// the last N console lines, no tools, no history.
func RunAsk(ctx context.Context, client *Client, serverName, consoleText, question string, emit EmitFunc) error {
	if consoleText == "" {
		consoleText = "(the console buffer is empty — the server may not have been started since the panel booted)"
	}
	user := fmt.Sprintf("Recent console output:\n```\n%s```\n\nQuestion: %s", consoleText, question)
	messages := []Message{
		{Role: "system", Content: askSystemPrompt(serverName)},
		{Role: "user", Content: user},
	}
	_, _, err := client.Stream(ctx, messages, nil, func(kind, text string) {
		emit(Event{Type: kind, Text: text})
	})
	if err != nil {
		emit(Event{Type: "error", Error: err.Error()})
		return err
	}
	emit(Event{Type: "done"})
	return nil
}

// trimHistory bounds stored history by count AND bytes, and never lets it
// start with an orphaned tool message (which upsets strict OpenAI-compatible
// servers).
func trimHistory(msgs []Message) []Message {
	if len(msgs) > maxHistory {
		msgs = msgs[len(msgs)-maxHistory:]
	}
	total := 0
	for _, m := range msgs {
		total += len(m.Content)
	}
	for len(msgs) > 1 && total > maxHistoryBytes {
		total -= len(msgs[0].Content)
		msgs = msgs[1:]
	}
	for len(msgs) > 0 && msgs[0].Role == "tool" {
		msgs = msgs[1:]
	}
	return msgs
}

func safeJSON(s string) string {
	if json.Valid([]byte(s)) {
		return s
	}
	b, _ := json.Marshal(s)
	return string(b)
}
