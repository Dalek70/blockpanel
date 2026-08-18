package web

import (
	"context"
	"log"
	"net/http"
	"time"

	"blockpanel/internal/store"
)

// All update endpoints are admin-only (wired with requireAdmin).

func (s *Server) handleUpdateStatus(w http.ResponseWriter, r *http.Request) {
	st := s.upd.Status()
	writeJSON(w, 200, map[string]any{
		"current": st.Current, "latest": st.Latest,
		"update_available": st.UpdateAvailable,
		"checked_at":       st.CheckedAt,
		"check_error":      st.CheckError,
		"notes":            st.Notes,
		"release_url":      st.ReleaseURL,
		"applying":         st.Applying,
		"apply_error":      st.ApplyError,
		"auto_update":      s.db.UpdateSettings().AutoUpdate,
		"repo":             "Dalek70/blockpanel",
	})
}

func (s *Server) handleUpdateCheck(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if _, err := s.upd.Check(ctx); err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	s.handleUpdateStatus(w, r)
}

func (s *Server) handleUpdateApply(w http.ResponseWriter, r *http.Request) {
	st := s.upd.Status()
	if !st.UpdateAvailable {
		writeErr(w, http.StatusConflict, "no update available")
		return
	}
	s.audit(r, "panel.update_start", "v"+st.Current+" -> v"+st.Latest, "", "")
	// Reply first: on success the process is replaced and this connection dies.
	writeJSON(w, 200, map[string]string{"status": "updating", "to": st.Latest})
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		defer cancel()
		if err := s.upd.Apply(ctx); err != nil {
			log.Printf("update failed: %v", err)
		}
	}()
}

func (s *Server) handleUpdateSettingsPut(w http.ResponseWriter, r *http.Request) {
	var body struct {
		AutoUpdate *bool `json:"auto_update"`
	}
	if !readBody(w, r, &body, 4096) {
		return
	}
	cur := s.db.UpdateSettings()
	if body.AutoUpdate != nil {
		cur.AutoUpdate = *body.AutoUpdate
	}
	if err := s.db.SetUpdateSettings(cur); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	onoff := "off"
	if cur.AutoUpdate {
		onoff = "on"
	}
	s.audit(r, "panel.update_settings", "auto_update "+onoff, "", "")
	writeJSON(w, 200, store.UpdateSettings{AutoUpdate: cur.AutoUpdate})
}
