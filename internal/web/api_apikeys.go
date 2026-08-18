package web

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"blockpanel/internal/mc"
	"blockpanel/internal/store"
	"blockpanel/internal/util"
)

// API keys let scripts and monitoring call the panel without a browser
// session. A key is presented as `X-API-Key` and always resolves to a user,
// so it can never exceed that user's permissions.

type apiKeyView struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	UserID    string    `json:"user_id"`
	Username  string    `json:"username"`
	Prefix    string    `json:"prefix"`
	ReadOnly  bool      `json:"read_only"`
	Disabled  bool      `json:"disabled"`
	CreatedAt time.Time `json:"created_at"`
	LastUsed  time.Time `json:"last_used"`
}

func (s *Server) apiKeyViews(actor *store.User) []apiKeyView {
	out := []apiKeyView{}
	for _, k := range s.db.APIKeys() {
		// Non-admins only see their own keys.
		if !actor.IsAdmin && k.UserID != actor.ID {
			continue
		}
		name := "(deleted user)"
		if u := s.db.UserByID(k.UserID); u != nil {
			name = u.Username
		}
		out = append(out, apiKeyView{
			ID: k.ID, Name: k.Name, UserID: k.UserID, Username: name,
			Prefix: k.Prefix, ReadOnly: k.ReadOnly, Disabled: k.Disabled,
			CreatedAt: k.CreatedAt, LastUsed: k.LastUsed,
		})
	}
	return out
}

func (s *Server) handleAPIKeysList(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.apiKeyViews(userFrom(r)))
}

func (s *Server) handleAPIKeyCreate(w http.ResponseWriter, r *http.Request) {
	actor := userFrom(r)
	var body struct {
		Name     string `json:"name"`
		UserID   string `json:"user_id"`
		ReadOnly bool   `json:"read_only"`
	}
	if !readBody(w, r, &body, 1<<16) {
		return
	}
	owner := actor
	if body.UserID != "" && body.UserID != actor.ID {
		// Issuing a key for someone else means acting as them, so restrict
		// that to admins.
		if !actor.IsAdmin {
			writeErr(w, http.StatusForbidden, "only an admin can create a key for another user")
			return
		}
		owner = s.db.UserByID(body.UserID)
		if owner == nil {
			writeErr(w, http.StatusBadRequest, "unknown user")
			return
		}
	}
	if strings.TrimSpace(body.Name) == "" {
		writeErr(w, http.StatusBadRequest, "key name required")
		return
	}
	secret := "bp_" + util.NewToken()
	k := &store.APIKey{
		Name: body.Name, UserID: owner.ID, ReadOnly: body.ReadOnly,
	}
	if err := s.db.CreateAPIKey(k, secret); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s.audit(r, "apikey.create", body.Name, "owner="+owner.Username, "")
	// The plaintext secret is returned exactly once.
	writeJSON(w, 200, map[string]any{
		"status": "ok",
		"secret": secret,
		"key":    s.apiKeyViews(actor),
	})
}

func (s *Server) handleAPIKeyDelete(w http.ResponseWriter, r *http.Request) {
	actor := userFrom(r)
	id := r.PathValue("id")
	var owner string
	found := false
	for _, k := range s.db.APIKeys() {
		if k.ID == id {
			owner, found = k.UserID, true
			break
		}
	}
	if !found {
		writeErr(w, http.StatusNotFound, "no such key")
		return
	}
	if !actor.IsAdmin && owner != actor.ID {
		writeErr(w, http.StatusForbidden, "that key belongs to another user")
		return
	}
	if err := s.db.DeleteAPIKey(id); err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	s.audit(r, "apikey.delete", id, "", "")
	writeJSON(w, 200, s.apiKeyViews(actor))
}

// ---- shared jar download ---------------------------------------------------

// downloadJar streams a jar into the server root with a size cap. Shared by
// the URL downloader and the version installer.
func (s *Server) downloadJar(client *http.Client, root, rawURL, filename string) (int64, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" {
		return 0, fmt.Errorf("an https:// URL is required")
	}
	req, err := http.NewRequest("GET", parsed.String(), nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", "BlockPanel")
	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return 0, fmt.Errorf("download failed: %s", resp.Status)
	}
	const maxJar = 1 << 30
	n, err := mc.SaveUpload(root, "", filename, io.LimitReader(resp.Body, maxJar))
	if err != nil {
		return 0, err
	}
	if n >= maxJar {
		mc.Delete(root, filename)
		return 0, fmt.Errorf("file exceeds the 1 GiB limit")
	}
	return n, nil
}
