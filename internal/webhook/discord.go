// Package webhook posts Discord webhook notifications for server lifecycle
// events.
package webhook

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"blockpanel/internal/store"
)

var client = &http.Client{Timeout: 10 * time.Second}

var eventStyle = map[string]struct {
	title string
	color int
}{
	"start":         {"Server starting", 0x43b581},
	"stop":          {"Server stopped", 0x747f8d},
	"crash":         {"Server crashed", 0xf04747},
	"backup":        {"Backup created", 0x4a9eff},
	"player_join":   {"Player joined", 0x43b581},
	"player_leave":  {"Player left", 0x747f8d},
	"schedule":      {"Scheduled task", 0x4a9eff},
	"test":          {"Test notification", 0x4a9eff},
}

// ValidURL restricts webhook targets to Discord's endpoints so the panel
// cannot be turned into a generic SSRF proxy by a webhooks.manage user.
func ValidURL(raw string) bool {
	return strings.HasPrefix(raw, "https://discord.com/api/webhooks/") ||
		strings.HasPrefix(raw, "https://discordapp.com/api/webhooks/") ||
		strings.HasPrefix(raw, "https://ptb.discord.com/api/webhooks/") ||
		strings.HasPrefix(raw, "https://canary.discord.com/api/webhooks/")
}

// Send posts one event embed to a webhook URL.
func Send(url, event, serverName, detail string) error {
	style, ok := eventStyle[event]
	if !ok {
		style.title = event
		style.color = 0x747f8d
	}
	desc := fmt.Sprintf("**%s**", serverName)
	if detail != "" {
		desc += "\n" + detail
	}
	payload := map[string]any{
		"embeds": []map[string]any{{
			"title":       style.title,
			"description": desc,
			"color":       style.color,
			"timestamp":   time.Now().UTC().Format(time.RFC3339),
			"footer":      map[string]any{"text": "BlockPanel"},
		}},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	resp, err := client.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("discord returned %s", resp.Status)
	}
	return nil
}

// Notify fans an event out to every enabled webhook subscribed to it.
func Notify(srv *store.Server, event, detail string) {
	for _, wh := range srv.Webhooks {
		if !wh.Enabled || !ValidURL(wh.URL) {
			continue
		}
		subscribed := false
		for _, e := range wh.Events {
			if e == event {
				subscribed = true
				break
			}
		}
		if !subscribed {
			continue
		}
		go Send(wh.URL, event, srv.Name, detail)
	}
}
