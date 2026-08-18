package web

import (
	"errors"
	"fmt"
	"net/http"

	"blockpanel/internal/store"
	"blockpanel/internal/util"
	"blockpanel/internal/webhook"
)

var errNoSuchWebhook = errors.New("no such webhook")

var validEvents = map[string]bool{
	"start": true, "stop": true, "crash": true, "backup": true,
	"player_join": true, "player_leave": true,
}

func cleanEvents(events []string) []string {
	out := []string{}
	for _, e := range events {
		if validEvents[e] {
			out = append(out, e)
		}
	}
	return out
}

// Webhook URLs are secrets (anyone holding one can post to the channel), so
// list responses mask them.
type webhookView struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	URLMasked string   `json:"url_masked"`
	Events    []string `json:"events"`
	Enabled   bool     `json:"enabled"`
}

func maskURL(u string) string {
	if len(u) <= 45 {
		return u
	}
	return u[:45] + "…"
}

func toWebhookViews(cfg *store.Server) []webhookView {
	out := []webhookView{}
	for _, wh := range cfg.Webhooks {
		out = append(out, webhookView{
			ID: wh.ID, Name: wh.Name, URLMasked: maskURL(wh.URL),
			Events: wh.Events, Enabled: wh.Enabled,
		})
	}
	return out
}

func (s *Server) handleWebhooksList(w http.ResponseWriter, r *http.Request) {
	in := s.mgr.Get(r.PathValue("id"))
	writeJSON(w, 200, toWebhookViews(in.Config()))
}

const maxWebhooksPerServer = 25

func (s *Server) handleWebhookCreate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Name    string   `json:"name"`
		URL     string   `json:"url"`
		Events  []string `json:"events"`
		Enabled bool     `json:"enabled"`
	}
	if !readBody(w, r, &body, 1<<16) {
		return
	}
	if !webhook.ValidURL(body.URL) {
		writeErr(w, http.StatusBadRequest, "URL must be a Discord webhook (https://discord.com/api/webhooks/…)")
		return
	}
	if body.Name == "" {
		body.Name = "webhook"
	}
	wh := &store.Webhook{
		ID: util.NewID(), Name: body.Name, URL: body.URL,
		Events: cleanEvents(body.Events), Enabled: body.Enabled,
	}
	cfg, err := s.mgr.MutateConfig(id, func(c *store.Server) error {
		if len(c.Webhooks) >= maxWebhooksPerServer {
			return fmt.Errorf("this server already has the maximum of %d webhooks", maxWebhooksPerServer)
		}
		c.Webhooks = append(c.Webhooks, wh)
		return nil
	})
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s.audit(r, "webhook.create", wh.Name, "", cfg.ID)
	writeJSON(w, 200, toWebhookViews(cfg))
}

func (s *Server) handleWebhookUpdate(w http.ResponseWriter, r *http.Request) {
	id, whid := r.PathValue("id"), r.PathValue("whid")
	var body struct {
		Name    *string   `json:"name"`
		URL     *string   `json:"url"`
		Events  *[]string `json:"events"`
		Enabled *bool     `json:"enabled"`
	}
	if !readBody(w, r, &body, 1<<16) {
		return
	}
	if body.URL != nil && !webhook.ValidURL(*body.URL) {
		writeErr(w, http.StatusBadRequest, "URL must be a Discord webhook")
		return
	}
	cfg, err := s.mgr.MutateConfig(id, func(c *store.Server) error {
		for _, wh := range c.Webhooks {
			if wh.ID != whid {
				continue
			}
			if body.Name != nil && *body.Name != "" {
				wh.Name = *body.Name
			}
			if body.URL != nil {
				wh.URL = *body.URL
			}
			if body.Events != nil {
				wh.Events = cleanEvents(*body.Events)
			}
			if body.Enabled != nil {
				wh.Enabled = *body.Enabled
			}
			return nil
		}
		return errNoSuchWebhook
	})
	if err != nil {
		if errors.Is(err, errNoSuchWebhook) {
			writeErr(w, http.StatusNotFound, err.Error())
			return
		}
		writeErr(w, 500, err.Error())
		return
	}
	s.audit(r, "webhook.update", whid, "", cfg.ID)
	writeJSON(w, 200, toWebhookViews(cfg))
}

func (s *Server) handleWebhookDelete(w http.ResponseWriter, r *http.Request) {
	id, whid := r.PathValue("id"), r.PathValue("whid")
	var name string
	cfg, err := s.mgr.MutateConfig(id, func(c *store.Server) error {
		for i, wh := range c.Webhooks {
			if wh.ID == whid {
				name = wh.Name
				c.Webhooks = append(c.Webhooks[:i], c.Webhooks[i+1:]...)
				return nil
			}
		}
		return errNoSuchWebhook
	})
	if err != nil {
		if errors.Is(err, errNoSuchWebhook) {
			writeErr(w, http.StatusNotFound, err.Error())
			return
		}
		writeErr(w, 500, err.Error())
		return
	}
	s.audit(r, "webhook.delete", name, "", cfg.ID)
	writeJSON(w, 200, toWebhookViews(cfg))
}

func (s *Server) handleWebhookTest(w http.ResponseWriter, r *http.Request) {
	in := s.mgr.Get(r.PathValue("id"))
	cfg := in.Config()
	for _, wh := range cfg.Webhooks {
		if wh.ID == r.PathValue("whid") {
			if err := webhook.Send(wh.URL, "test", cfg.Name, "Sent from the panel webhook settings."); err != nil {
				writeErr(w, http.StatusBadGateway, err.Error())
				return
			}
			s.audit(r, "webhook.test", wh.Name, "", cfg.ID)
			writeJSON(w, 200, map[string]string{"status": "ok"})
			return
		}
	}
	writeErr(w, http.StatusNotFound, "no such webhook")
}
