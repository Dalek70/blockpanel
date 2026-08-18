package web

import (
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"blockpanel/internal/store"
	"blockpanel/internal/util"
)

// Panel settings live in <data>/config.json. Changes touch listeners and TLS,
// so they apply on restart; the response says so.

func (s *Server) configPath() string {
	return filepath.Join(s.dataDir, "config.json")
}

func (s *Server) handleSettingsGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.cfg)
}

func (s *Server) handleSettingsPut(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Bind            *string  `json:"bind"`
		Port            *int     `json:"port"`
		TLSMode         *string  `json:"tls_mode"`
		CertFile        *string  `json:"cert_file"`
		KeyFile         *string  `json:"key_file"`
		ExtraHosts      *[]string `json:"extra_hosts"`
		SessionTTLHours *int     `json:"session_ttl_hours"`
		MaxUploadMB     *int64   `json:"max_upload_mb"`
		TrustProxy      *bool    `json:"trust_proxy"`
		BehindTLSProxy  *bool    `json:"behind_tls_proxy"`
	}
	if !readBody(w, r, &body, 1<<20) {
		return
	}
	// Rebase on what is CURRENTLY on disk, not the boot-time snapshot in
	// s.cfg. s.cfg is never refreshed after startup, so patching it would
	// resurrect superseded values — silently re-enabling trust_proxy or
	// downgrading tls_mode back to http on a later unrelated save.
	next := s.cfg
	var onDisk store.Config
	if err := util.ReadJSON(s.configPath(), &onDisk); err == nil {
		next = onDisk
	}
	if body.Bind != nil {
		next.Bind = *body.Bind
	}
	if body.Port != nil {
		if *body.Port < 1 || *body.Port > 65535 {
			writeErr(w, http.StatusBadRequest, "port out of range")
			return
		}
		next.Port = *body.Port
	}
	if body.TLSMode != nil {
		switch *body.TLSMode {
		case "http", "self-signed", "custom":
			next.TLS.Mode = *body.TLSMode
		default:
			writeErr(w, http.StatusBadRequest, "tls_mode must be http, self-signed or custom")
			return
		}
	}
	if body.CertFile != nil {
		next.TLS.CertFile = *body.CertFile
	}
	if body.KeyFile != nil {
		next.TLS.KeyFile = *body.KeyFile
	}
	if body.ExtraHosts != nil {
		next.TLS.ExtraHosts = *body.ExtraHosts
	}
	if next.TLS.Mode == "custom" && (next.TLS.CertFile == "" || next.TLS.KeyFile == "") {
		writeErr(w, http.StatusBadRequest, "custom TLS needs cert_file and key_file paths")
		return
	}
	if body.SessionTTLHours != nil && *body.SessionTTLHours > 0 {
		next.SessionTTLHours = *body.SessionTTLHours
	}
	if body.MaxUploadMB != nil && *body.MaxUploadMB > 0 {
		next.MaxUploadMB = *body.MaxUploadMB
	}
	if body.TrustProxy != nil {
		next.TrustProxy = *body.TrustProxy
	}
	if body.BehindTLSProxy != nil {
		next.BehindTLSProxy = *body.BehindTLSProxy
	}
	if err := util.WriteJSONAtomic(s.configPath(), next); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.audit(r, "settings.update", "", "", "")
	writeJSON(w, 200, map[string]any{
		"status":           "ok",
		"restart_required": true,
		"config":           next,
	})
}

func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	writeJSON(w, 200, s.db.AuditEntries(limit, q.Get("user"), q.Get("action")))
}

// handleRestart exits the process; systemd/launchd bring it back up. When run
// by hand the panel just stops — the UI warns about that.
func (s *Server) handleRestart(w http.ResponseWriter, r *http.Request) {
	s.audit(r, "panel.restart", "", "", "")
	writeJSON(w, 200, map[string]string{"status": "restarting"})
	go func() {
		time.Sleep(500 * time.Millisecond)
		s.mgr.StopAll()
		os.Exit(0)
	}()
}
