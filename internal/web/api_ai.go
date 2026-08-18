package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"blockpanel/internal/ai"
	"blockpanel/internal/store"
)

// ---- Settings (admin only) ------------------------------------------------

type aiSettingsView struct {
	store.AISettings
	APIKey    string `json:"api_key"` // always masked
	APIKeySet bool   `json:"api_key_set"`
}

func (s *Server) handleAISettingsGet(w http.ResponseWriter, r *http.Request) {
	cfg := s.db.AISettings()
	view := aiSettingsView{AISettings: cfg, APIKey: "", APIKeySet: cfg.APIKey != ""}
	view.AISettings.APIKey = ""
	writeJSON(w, 200, view)
}

func (s *Server) handleAISettingsPut(w http.ResponseWriter, r *http.Request) {
	old := s.db.AISettings()
	var body struct {
		Enabled            *bool    `json:"enabled"`
		Provider           *string  `json:"provider"`
		BaseURL            *string  `json:"base_url"`
		APIKey             *string  `json:"api_key"` // empty string = keep, "-" = clear
		Model              *string  `json:"model"`
		Temperature        *float64 `json:"temperature"`
		MaxTokens          *int     `json:"max_tokens"`
		ReasoningEffort    *string  `json:"reasoning_effort"`
		ExtraBody          *string  `json:"extra_body"`
		WebSearchEnabled   *bool    `json:"web_search_enabled"`
		ContextLines       *int     `json:"context_lines"`
		AgentMaxIterations *int     `json:"agent_max_iterations"`
	}
	if !readBody(w, r, &body, 1<<20) {
		return
	}
	next := old
	if body.Enabled != nil {
		next.Enabled = *body.Enabled
	}
	if body.Provider != nil {
		switch *body.Provider {
		case "sglang", "vllm", "openrouter", "lmstudio", "llamacpp", "custom":
			next.Provider = *body.Provider
		default:
			writeErr(w, http.StatusBadRequest, "unknown provider")
			return
		}
	}
	if body.BaseURL != nil {
		next.BaseURL = *body.BaseURL
	}
	if next.BaseURL == "" {
		next.BaseURL = store.ProviderDefaultURL(next.Provider)
	}
	if body.APIKey != nil {
		switch *body.APIKey {
		case "":
			// keep existing
		case "-":
			next.APIKey = ""
		default:
			next.APIKey = *body.APIKey
		}
	}
	if body.Model != nil {
		next.Model = *body.Model
	}
	if body.Temperature != nil {
		next.Temperature = *body.Temperature
	}
	if body.MaxTokens != nil {
		next.MaxTokens = *body.MaxTokens
	}
	if body.ReasoningEffort != nil {
		switch *body.ReasoningEffort {
		case "", "low", "medium", "high":
			next.ReasoningEffort = *body.ReasoningEffort
		default:
			writeErr(w, http.StatusBadRequest, "reasoning_effort must be empty, low, medium or high")
			return
		}
	}
	if body.ExtraBody != nil {
		if *body.ExtraBody != "" && !json.Valid([]byte(*body.ExtraBody)) {
			writeErr(w, http.StatusBadRequest, "extra_body must be valid JSON")
			return
		}
		next.ExtraBody = *body.ExtraBody
	}
	if body.WebSearchEnabled != nil {
		next.WebSearchEnabled = *body.WebSearchEnabled
	}
	if body.ContextLines != nil {
		n := *body.ContextLines
		if n < 16 {
			n = 16
		}
		if n > 2000 {
			n = 2000
		}
		next.ContextLines = n
	}
	if body.AgentMaxIterations != nil {
		n := *body.AgentMaxIterations
		if n < 1 {
			n = 1
		}
		if n > 50 {
			n = 50
		}
		next.AgentMaxIterations = n
	}
	if err := s.db.SetAISettings(next); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.audit(r, "ai.settings_update", next.Provider+"/"+next.Model, "", "")
	s.handleAISettingsGet(w, r)
}

func (s *Server) handleAITest(w http.ResponseWriter, r *http.Request) {
	cfg := s.db.AISettings()
	client := ai.NewClient(cfg)
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	models, err := client.ListModels(ctx)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	if len(models) > 50 {
		models = models[:50]
	}
	writeJSON(w, 200, map[string]any{"status": "ok", "models": models})
}

// handleAIStatus tells any user whether AI features are on (not the config).
func (s *Server) handleAIStatus(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	cfg := s.db.AISettings()
	writeJSON(w, 200, map[string]any{
		"enabled":  cfg.Enabled && cfg.Model != "",
		"may_use":  s.db.HasGlobal(u, store.PermAIUse),
		"provider": cfg.Provider,
	})
}

// ---- SSE plumbing ---------------------------------------------------------

func sseStart(w http.ResponseWriter) (func(ev ai.Event), bool) {
	fl, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, 500, "streaming unsupported")
		return nil, false
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	// Bound each write so a client that stops reading cannot pin this
	// goroutine (and an in-flight agent run) indefinitely.
	rc := http.NewResponseController(w)
	return func(ev ai.Event) {
		b, err := json.Marshal(ev)
		if err != nil {
			return
		}
		rc.SetWriteDeadline(time.Now().Add(30 * time.Second))
		fmt.Fprintf(w, "data: %s\n\n", b)
		fl.Flush()
	}, true
}

// aiPrecheck returns the client + settings when AI is enabled and the user
// holds the global ai.use permission.
func (s *Server) aiPrecheck(w http.ResponseWriter, r *http.Request) (*ai.Client, store.AISettings, bool) {
	u := userFrom(r)
	cfg := s.db.AISettings()
	if !cfg.Enabled || cfg.Model == "" {
		writeErr(w, http.StatusConflict, "AI is not enabled — an admin must configure it in AI Settings")
		return nil, cfg, false
	}
	if !s.db.HasGlobal(u, store.PermAIUse) {
		writeErr(w, http.StatusForbidden, "missing permission: ai.use")
		return nil, cfg, false
	}
	return ai.NewClient(cfg), cfg, true
}

// ---- Ask (stateless log Q&A) ----------------------------------------------

func (s *Server) handleAIAsk(w http.ResponseWriter, r *http.Request) {
	client, cfg, ok := s.aiPrecheck(w, r)
	if !ok {
		return
	}
	in := s.mgr.Get(r.PathValue("id"))
	var body struct {
		Question string `json:"question"`
	}
	if !readBody(w, r, &body, 1<<20) {
		return
	}
	if body.Question == "" {
		writeErr(w, http.StatusBadRequest, "question required")
		return
	}
	emit, ok := sseStart(w)
	if !ok {
		return
	}
	s.audit(r, "ai.ask", body.Question, "", r.PathValue("id"))
	consoleText := in.Console().LastText(cfg.ContextLines)
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	ai.RunAsk(ctx, client, in.Config().Name, consoleText, body.Question, emit)
}

// ---- Agent ----------------------------------------------------------------

func (s *Server) handleAIAgent(w http.ResponseWriter, r *http.Request) {
	client, cfg, ok := s.aiPrecheck(w, r)
	if !ok {
		return
	}
	u := userFrom(r)
	id := r.PathValue("id")
	in := s.mgr.Get(id)
	var body struct {
		Message string `json:"message"`
	}
	if !readBody(w, r, &body, 1<<20) {
		return
	}
	if body.Message == "" {
		writeErr(w, http.StatusBadRequest, "message required")
		return
	}
	emit, ok := sseStart(w)
	if !ok {
		return
	}
	s.audit(r, "ai.agent", body.Message, "", id)

	srvCfg := in.Config()
	env := &ai.ToolEnv{
		Instance:  in,
		Server:    srvCfg,
		WebSearch: cfg.WebSearchEnabled,
		HasPerm: func(perm string) bool {
			return s.db.HasServer(u, id, perm)
		},
		Audit: func(action, detail string) {
			s.db.Audit(store.AuditEntry{
				User: u.Username, IP: s.clientIP(r),
				Action: action, Detail: detail, ServerID: id,
			})
		},
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Minute)
	defer cancel()
	if err := s.runner.RunAgent(ctx, client, cfg, env, ai.ConvKey(u.ID, id), body.Message, emit); err != nil {
		// Headers are already sent, so surface it on the stream rather than
		// closing an empty one (e.g. "a run is already in progress").
		emit(ai.Event{Type: "error", Error: err.Error()})
		emit(ai.Event{Type: "done"})
	}
}

func (s *Server) handleAIAgentReset(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	s.runner.ResetConv(ai.ConvKey(u.ID, r.PathValue("id")))
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

// handleAIApproval resolves a pending agent approval. Any authenticated user
// could hit this endpoint, but approval IDs are 128-bit random values known
// only to the SSE stream that created them.
func (s *Server) handleAIApproval(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Approve bool `json:"approve"`
	}
	if !readBody(w, r, &body, 1<<16) {
		return
	}
	if err := s.runner.Decide(r.PathValue("id"), body.Approve, userFrom(r).ID); err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	s.audit(r, "ai.approval", r.PathValue("id"), fmt.Sprintf("approve=%v", body.Approve), "")
	writeJSON(w, 200, map[string]string{"status": "ok"})
}
