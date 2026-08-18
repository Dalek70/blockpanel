// Package ai implements the panel's AI features against any OpenAI-compatible
// chat-completions endpoint: SGLang, vLLM, OpenRouter, LM Studio and
// llama.cpp all speak this protocol.
package ai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"blockpanel/internal/store"
)

type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

type ToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type Tool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		Parameters  map[string]any `json:"parameters"`
	} `json:"function"`
}

// DeltaFunc receives streamed output. kind is "content" or "reasoning".
type DeltaFunc func(kind, text string)

type Client struct {
	cfg  store.AISettings
	http *http.Client
}

func NewClient(cfg store.AISettings) *Client {
	return &Client{
		cfg: cfg,
		http: &http.Client{
			Timeout: 0, // streams can be long; callers bound via context
			Transport: &http.Transport{
				ResponseHeaderTimeout: 60 * time.Second,
			},
		},
	}
}

func (c *Client) endpoint(path string) string {
	return strings.TrimRight(c.cfg.BaseURL, "/") + path
}

func (c *Client) setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	if c.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	}
	if c.cfg.Provider == "openrouter" {
		req.Header.Set("X-Title", "BlockPanel")
	}
}

// ListModels hits GET /models for the settings "test connection" button.
func (c *Client) ListModels(ctx context.Context) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", c.endpoint("/models"), nil)
	if err != nil {
		return nil, err
	}
	c.setHeaders(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("%s: %s", resp.Status, truncate(string(body), 300))
	}
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("bad /models response: %w", err)
	}
	ids := make([]string, 0, len(out.Data))
	for _, m := range out.Data {
		ids = append(ids, m.ID)
	}
	return ids, nil
}

// buildBody assembles the chat-completions payload, merging admin-supplied
// extra_body JSON (e.g. chat_template_kwargs for Qwen thinking control).
func (c *Client) buildBody(messages []Message, tools []Tool) ([]byte, error) {
	body := map[string]any{
		"model":    c.cfg.Model,
		"messages": messages,
		"stream":   true,
	}
	if c.cfg.Temperature > 0 {
		body["temperature"] = c.cfg.Temperature
	}
	if c.cfg.MaxTokens > 0 {
		body["max_tokens"] = c.cfg.MaxTokens
	}
	if len(tools) > 0 {
		body["tools"] = tools
	}
	if c.cfg.ReasoningEffort != "" && c.cfg.Provider == "openrouter" {
		body["reasoning"] = map[string]any{"effort": c.cfg.ReasoningEffort}
	}
	if strings.TrimSpace(c.cfg.ExtraBody) != "" {
		var extra map[string]any
		if err := json.Unmarshal([]byte(c.cfg.ExtraBody), &extra); err != nil {
			return nil, fmt.Errorf("ai settings extra_body is not valid JSON: %w", err)
		}
		for k, v := range extra {
			body[k] = v
		}
	}
	return json.Marshal(body)
}

type streamDelta struct {
	Choices []struct {
		Delta struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"` // vLLM / SGLang / DeepSeek style
			Reasoning        string `json:"reasoning"`         // OpenRouter style
			ToolCalls        []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// Stream runs one streaming chat-completion. Deltas flow to onDelta; the
// final assistant message (content + tool calls, reasoning stripped) and
// finish reason are returned.
func (c *Client) Stream(ctx context.Context, messages []Message, tools []Tool, onDelta DeltaFunc) (Message, string, error) {
	payload, err := c.buildBody(messages, tools)
	if err != nil {
		return Message{}, "", err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", c.endpoint("/chat/completions"), bytes.NewReader(payload))
	if err != nil {
		return Message{}, "", err
	}
	c.setHeaders(req)
	req.Header.Set("Accept", "text/event-stream")

	resp, err := c.http.Do(req)
	if err != nil {
		return Message{}, "", fmt.Errorf("AI endpoint unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		return Message{}, "", fmt.Errorf("AI endpoint %s: %s", resp.Status, truncate(string(body), 400))
	}

	var (
		content    strings.Builder
		finish     string
		toolCalls  = map[int]*ToolCall{}
		thinkState = newThinkParser(onDelta)
	)

	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		data, ok := strings.CutPrefix(line, "data:")
		if !ok {
			continue
		}
		data = strings.TrimSpace(data)
		if data == "[DONE]" {
			break
		}
		var d streamDelta
		if err := json.Unmarshal([]byte(data), &d); err != nil {
			continue
		}
		if d.Error != nil {
			return Message{}, "", errors.New("AI endpoint error: " + d.Error.Message)
		}
		if len(d.Choices) == 0 {
			continue
		}
		ch := d.Choices[0]
		if ch.FinishReason != "" {
			finish = ch.FinishReason
		}
		if r := ch.Delta.ReasoningContent; r != "" {
			onDelta("reasoning", r)
		}
		if r := ch.Delta.Reasoning; r != "" {
			onDelta("reasoning", r)
		}
		if ch.Delta.Content != "" {
			clean := thinkState.feed(ch.Delta.Content)
			if clean != "" {
				content.WriteString(clean)
				onDelta("content", clean)
			}
		}
		for _, tc := range ch.Delta.ToolCalls {
			cur, ok := toolCalls[tc.Index]
			if !ok {
				cur = &ToolCall{Type: "function"}
				toolCalls[tc.Index] = cur
			}
			if tc.ID != "" {
				cur.ID = tc.ID
			}
			if tc.Function.Name != "" {
				cur.Function.Name += tc.Function.Name
			}
			cur.Function.Arguments += tc.Function.Arguments
		}
	}
	if err := sc.Err(); err != nil {
		return Message{}, "", fmt.Errorf("stream read: %w", err)
	}
	if tail := thinkState.flush(); tail != "" {
		content.WriteString(tail)
		onDelta("content", tail)
	}

	msg := Message{Role: "assistant", Content: content.String()}
	for i := 0; i < len(toolCalls); i++ {
		if tc, ok := toolCalls[i]; ok {
			if tc.ID == "" {
				tc.ID = fmt.Sprintf("call_%d", i)
			}
			msg.ToolCalls = append(msg.ToolCalls, *tc)
		}
	}
	if finish == "" && len(msg.ToolCalls) > 0 {
		finish = "tool_calls"
	}
	return msg, finish, nil
}

// thinkParser strips <think>...</think> blocks that some reasoning models
// (DeepSeek-R1, QwQ, Qwen3) emit inline, routing them to the reasoning
// channel instead. Handles tags split across stream chunks.
type thinkParser struct {
	onDelta DeltaFunc
	buf     string
	state   int // 0 = detecting leading tag, 1 = inside think, 2 = normal
}

const (
	thinkOpen  = "<think>"
	thinkClose = "</think>"
)

func newThinkParser(onDelta DeltaFunc) *thinkParser {
	return &thinkParser{onDelta: onDelta}
}

// feed consumes a chunk and returns the portion that is real content.
func (p *thinkParser) feed(chunk string) string {
	p.buf += chunk
	var out strings.Builder
	for {
		switch p.state {
		case 0: // start of message: is it a <think> block?
			trimmed := strings.TrimLeft(p.buf, " \n\t")
			if trimmed == "" {
				return out.String()
			}
			if strings.HasPrefix(trimmed, thinkOpen) {
				p.buf = trimmed[len(thinkOpen):]
				p.state = 1
				continue
			}
			if len(trimmed) < len(thinkOpen) && strings.HasPrefix(thinkOpen, trimmed) {
				return out.String() // could still become <think>; wait for more
			}
			p.state = 2
			continue
		case 1: // inside <think>
			if idx := strings.Index(p.buf, thinkClose); idx >= 0 {
				p.emitReasoning(p.buf[:idx])
				p.buf = p.buf[idx+len(thinkClose):]
				p.state = 2
				continue
			}
			// keep a tail that might be a split closing tag
			keep := len(thinkClose) - 1
			if len(p.buf) > keep {
				p.emitReasoning(p.buf[:len(p.buf)-keep])
				p.buf = p.buf[len(p.buf)-keep:]
			}
			return out.String()
		default: // normal content
			out.WriteString(p.buf)
			p.buf = ""
			return out.String()
		}
	}
}

func (p *thinkParser) emitReasoning(s string) {
	if s != "" && p.onDelta != nil {
		p.onDelta("reasoning", s)
	}
}

func (p *thinkParser) flush() string {
	if p.state == 1 {
		p.emitReasoning(p.buf)
		p.buf = ""
		return ""
	}
	out := p.buf
	p.buf = ""
	return out
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
