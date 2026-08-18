package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"blockpanel/internal/mc"
	"blockpanel/internal/store"
	"blockpanel/internal/util"
)

type serverPerms map[string]bool

type serverView struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	State     string    `json:"state"`
	UptimeSec int64     `json:"uptime_sec"`
	CPU       float64   `json:"cpu_percent"`
	RSSMB     float64   `json:"rss_mb"`
	Port      int       `json:"port"`
	CreatedAt time.Time `json:"created_at"`
	Imported  bool      `json:"imported"`

	// Config (present when the caller can view it)
	JavaPath          string      `json:"java_path,omitempty"`
	Jar               string      `json:"jar,omitempty"`
	MinMemMB          int         `json:"min_mem_mb,omitempty"`
	MaxMemMB          int         `json:"max_mem_mb,omitempty"`
	JVMArgs           string      `json:"jvm_args,omitempty"`
	ServerArgs        string      `json:"server_args,omitempty"`
	LaunchOverride    string      `json:"launch_override,omitempty"`
	StopCommand       string      `json:"stop_command,omitempty"`
	StopGraceSecs     int         `json:"stop_grace_secs,omitempty"`
	AutoRestart       bool        `json:"auto_restart"`
	AutoStart         bool        `json:"auto_start"`
	AcceptEula        bool        `json:"accept_eula"`
	DownloadsEnabled  bool        `json:"downloads_enabled"`
	BlockedExtensions []string    `json:"blocked_extensions"`
	BackupKeep        int         `json:"backup_keep"`
	Perms             serverPerms `json:"perms"`

	// Live status from the most recent server-list ping.
	PlayersNow int    `json:"players_now"`
	PlayersMax int    `json:"players_max"`
	MOTD       string `json:"motd,omitempty"`
	MCVersion  string `json:"mc_version,omitempty"`
}

func (s *Server) serverPermsFor(u *store.User, id string) serverPerms {
	p := serverPerms{}
	for _, k := range store.ServerPermKeys {
		p[k] = s.db.HasServer(u, id, k)
	}
	return p
}

func (s *Server) toServerView(u *store.User, in *mc.Instance, full bool) serverView {
	cfg := in.Config()
	st := in.Stats()
	v := serverView{
		ID: cfg.ID, Name: cfg.Name, State: string(in.State()),
		UptimeSec: int64(in.Uptime().Seconds()),
		CPU:       st.CPUPercent, RSSMB: st.RSSMB,
		Port:      mc.ReadServerPort(cfg.Root),
		CreatedAt: cfg.CreatedAt, Imported: cfg.Imported,
		Perms:     s.serverPermsFor(u, cfg.ID),
	}
	if ping := in.LastPing(); ping.Online {
		v.PlayersNow, v.PlayersMax = ping.PlayersNow, ping.PlayersMax
		v.MOTD, v.MCVersion = ping.MOTD, ping.Version
	}
	if full {
		v.JavaPath, v.Jar = cfg.JavaPath, cfg.Jar
		v.MinMemMB, v.MaxMemMB = cfg.MinMemMB, cfg.MaxMemMB
		v.JVMArgs, v.ServerArgs = cfg.JVMArgs, cfg.ServerArgs
		v.StopCommand, v.StopGraceSecs = cfg.StopCommand, cfg.StopGraceSecs
		v.AutoRestart, v.AutoStart, v.AcceptEula = cfg.AutoRestart, cfg.AutoStart, cfg.AcceptEula
		v.DownloadsEnabled = cfg.DownloadsEnabled
		v.BlockedExtensions = cfg.BlockedExtensions
		v.BackupKeep = cfg.BackupKeep
		if v.BlockedExtensions == nil {
			v.BlockedExtensions = []string{}
		}
		if u.IsAdmin {
			v.LaunchOverride = cfg.LaunchOverride
		}
	}
	return v
}

func (s *Server) handleServersList(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	out := []serverView{}
	for _, in := range s.mgr.All() {
		if s.db.HasServer(u, in.Config().ID, store.SPermView) {
			out = append(out, s.toServerView(u, in, false))
		}
	}
	writeJSON(w, 200, out)
}

func (s *Server) handleServerGet(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	in := s.mgr.Get(r.PathValue("id"))
	writeJSON(w, 200, s.toServerView(u, in, true))
}

func (s *Server) handleServerCreate(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	var body struct {
		Name       string `json:"name"`
		ImportPath string `json:"import_path"`
		MaxMemMB   int    `json:"max_mem_mb"`
		Jar        string `json:"jar"`
	}
	if !readBody(w, r, &body, 1<<16) {
		return
	}
	if body.Name == "" {
		writeErr(w, http.StatusBadRequest, "name required")
		return
	}
	if body.ImportPath != "" && !u.IsAdmin {
		// Importing grants panel access to an arbitrary host path.
		writeErr(w, http.StatusForbidden, "only an admin can import an existing directory")
		return
	}
	cfg := &store.Server{
		ID:               util.Slugify(body.Name) + "-" + util.NewID()[:6],
		Name:             body.Name,
		CreatedAt:        time.Now(),
		JavaPath:         "java",
		Jar:              body.Jar,
		MinMemMB:         1024,
		MaxMemMB:         body.MaxMemMB,
		ServerArgs:       "nogui",
		StopCommand:      "stop",
		StopGraceSecs:    30,
		DownloadsEnabled: true,
	}
	if cfg.MaxMemMB <= 0 {
		cfg.MaxMemMB = 2048
	}
	if cfg.MinMemMB > cfg.MaxMemMB {
		cfg.MinMemMB = cfg.MaxMemMB
	}
	var err error
	if body.ImportPath != "" {
		_, err = s.mgr.Import(cfg, body.ImportPath)
	} else {
		_, err = s.mgr.Create(cfg)
	}
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s.audit(r, "server.create", cfg.Name, "", cfg.ID)
	writeJSON(w, 200, s.toServerView(u, s.mgr.Get(cfg.ID), true))
}

func (s *Server) handleServerUpdate(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	id := r.PathValue("id")
	in := s.mgr.Get(id)

	var body struct {
		Name          *string `json:"name"`
		Jar           *string `json:"jar"`
		MinMemMB      *int    `json:"min_mem_mb"`
		MaxMemMB      *int    `json:"max_mem_mb"`
		StopCommand   *string `json:"stop_command"`
		StopGraceSecs *int    `json:"stop_grace_secs"`
		AutoRestart   *bool   `json:"auto_restart"`
		AutoStart     *bool   `json:"auto_start"`
		AcceptEula    *bool   `json:"accept_eula"`
		BackupKeep    *int    `json:"backup_keep"`

		// Admin-only. These all amount to controlling what command the panel
		// executes, which host paths it touches, or whether files may leave
		// the box. JVM/server args are in this set because the JVM treats
		// several flags as code execution: -javaagent:, -agentpath:,
		// -XX:OnOutOfMemoryError=, @argfiles. A config.edit delegate must not
		// be able to reach them.
		JavaPath          *string   `json:"java_path"`
		JVMArgs           *string   `json:"jvm_args"`
		ServerArgs        *string   `json:"server_args"`
		LaunchOverride    *string   `json:"launch_override"`
		DownloadsEnabled  *bool     `json:"downloads_enabled"`
		BlockedExtensions *[]string `json:"blocked_extensions"`
	}
	if !readBody(w, r, &body, 1<<20) {
		return
	}

	adminOnly := body.LaunchOverride != nil || body.DownloadsEnabled != nil ||
		body.BlockedExtensions != nil || body.JavaPath != nil ||
		body.JVMArgs != nil || body.ServerArgs != nil
	if adminOnly && !u.IsAdmin {
		writeErr(w, http.StatusForbidden,
			"those fields are admin-only (java path, JVM/server args, launch override, download policy)")
		return
	}
	if body.StopGraceSecs != nil && (*body.StopGraceSecs < 0 || *body.StopGraceSecs > 3600) {
		// An unbounded grace period would park Stop() (and therefore panel
		// shutdown and restart, which wait on it) effectively forever.
		writeErr(w, http.StatusBadRequest, "stop_grace_secs must be between 0 and 3600")
		return
	}
	if body.MinMemMB != nil && (*body.MinMemMB < 0 || *body.MinMemMB > 1<<20) {
		writeErr(w, http.StatusBadRequest, "min_mem_mb out of range")
		return
	}
	if body.MaxMemMB != nil && (*body.MaxMemMB < 0 || *body.MaxMemMB > 1<<20) {
		writeErr(w, http.StatusBadRequest, "max_mem_mb out of range")
		return
	}

	cfg, err := s.mgr.MutateConfig(id, func(cfg *store.Server) error {
		if body.Name != nil && *body.Name != "" {
			cfg.Name = *body.Name
		}
		if body.JavaPath != nil {
			cfg.JavaPath = *body.JavaPath
		}
		if body.Jar != nil {
			cfg.Jar = *body.Jar
		}
		if body.MinMemMB != nil {
			cfg.MinMemMB = *body.MinMemMB
		}
		if body.MaxMemMB != nil {
			cfg.MaxMemMB = *body.MaxMemMB
		}
		if body.JVMArgs != nil {
			cfg.JVMArgs = *body.JVMArgs
		}
		if body.ServerArgs != nil {
			cfg.ServerArgs = *body.ServerArgs
		}
		if body.StopCommand != nil {
			cfg.StopCommand = *body.StopCommand
		}
		if body.StopGraceSecs != nil {
			cfg.StopGraceSecs = *body.StopGraceSecs
		}
		if body.AutoRestart != nil {
			cfg.AutoRestart = *body.AutoRestart
		}
		if body.AutoStart != nil {
			cfg.AutoStart = *body.AutoStart
		}
		if body.AcceptEula != nil {
			cfg.AcceptEula = *body.AcceptEula
		}
		if body.BackupKeep != nil {
			cfg.BackupKeep = *body.BackupKeep
		}
		if body.LaunchOverride != nil {
			cfg.LaunchOverride = *body.LaunchOverride
		}
		if body.DownloadsEnabled != nil {
			cfg.DownloadsEnabled = *body.DownloadsEnabled
		}
		if body.BlockedExtensions != nil {
			cfg.BlockedExtensions = *body.BlockedExtensions
		}
		return nil
	})
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.audit(r, "server.update", cfg.Name, "", id)
	writeJSON(w, 200, s.toServerView(u, in, true))
}

func (s *Server) handleServerDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	in := s.mgr.Get(id)
	if in == nil {
		writeErr(w, http.StatusNotFound, "no such server")
		return
	}
	name := in.Config().Name
	if err := s.mgr.Delete(id); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.audit(r, "server.delete", name, "", id)
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

// ---- Lifecycle ------------------------------------------------------------

func (s *Server) handleServerStart(w http.ResponseWriter, r *http.Request) {
	in := s.mgr.Get(r.PathValue("id"))
	if err := in.Start(); err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	s.audit(r, "server.start", in.Config().Name, "", r.PathValue("id"))
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

func (s *Server) handleServerStop(w http.ResponseWriter, r *http.Request) {
	in := s.mgr.Get(r.PathValue("id"))
	s.audit(r, "server.stop", in.Config().Name, "", r.PathValue("id"))
	go in.Stop() // stop can take up to grace+15s; don't hold the request
	writeJSON(w, 200, map[string]string{"status": "stopping"})
}

func (s *Server) handleServerRestart(w http.ResponseWriter, r *http.Request) {
	in := s.mgr.Get(r.PathValue("id"))
	s.audit(r, "server.restart", in.Config().Name, "", r.PathValue("id"))
	go func() {
		if in.State() != mc.StateStopped {
			if err := in.Stop(); err != nil {
				return
			}
		}
		if err := in.Start(); err != nil {
			in.Console().Append("[panel] restart failed: " + err.Error())
		}
	}()
	writeJSON(w, 200, map[string]string{"status": "restarting"})
}

func (s *Server) handleServerKill(w http.ResponseWriter, r *http.Request) {
	in := s.mgr.Get(r.PathValue("id"))
	s.audit(r, "server.kill", in.Config().Name, "", r.PathValue("id"))
	if err := in.Kill(); err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

func (s *Server) handleServerCommand(w http.ResponseWriter, r *http.Request) {
	in := s.mgr.Get(r.PathValue("id"))
	var body struct {
		Command string `json:"command"`
	}
	if !readBody(w, r, &body, 1<<16) {
		return
	}
	if err := in.SendCommand(body.Command); err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	s.audit(r, "server.command", in.Config().Name, body.Command, r.PathValue("id"))
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

// ---- Console --------------------------------------------------------------

func (s *Server) handleConsoleTail(w http.ResponseWriter, r *http.Request) {
	in := s.mgr.Get(r.PathValue("id"))
	n, _ := strconv.Atoi(r.URL.Query().Get("lines"))
	if n <= 0 || n > 2000 {
		n = 500
	}
	writeJSON(w, 200, in.Console().Last(n))
}

// handleConsoleStream is an SSE feed: history burst, then live lines, plus
// state heartbeats so the UI can flip start/stop buttons without polling.
func (s *Server) handleConsoleStream(w http.ResponseWriter, r *http.Request) {
	in := s.mgr.Get(r.PathValue("id"))
	fl, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, 500, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")

	// A client that stops reading would otherwise block the handler inside
	// Write forever, leaking this goroutine, its fd and its console
	// subscription (and making every log line an O(subscribers) operation).
	rc := http.NewResponseController(w)
	failed := false
	send := func(event, data string) {
		if failed {
			return
		}
		rc.SetWriteDeadline(time.Now().Add(15 * time.Second))
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data); err != nil {
			failed = true
		}
	}
	for _, line := range in.Console().Last(300) {
		send("line", jsonLine(line))
	}
	send("state", `"`+string(in.State())+`"`)
	fl.Flush()

	sub := in.Console().Subscribe()
	defer in.Console().Unsubscribe(sub)

	heartbeat := time.NewTicker(5 * time.Second)
	defer heartbeat.Stop()

	for {
		if failed {
			return // client stalled or disconnected; drop the subscription
		}
		select {
		case <-r.Context().Done():
			return
		case line := <-sub:
			send("line", jsonLine(line))
			// drain whatever else is queued before flushing
			for len(sub) > 0 {
				send("line", jsonLine(<-sub))
			}
			fl.Flush()
		case <-heartbeat.C:
			send("state", `"`+string(in.State())+`"`)
			fl.Flush()
		}
	}
}

func jsonLine(l mc.ConsoleLine) string {
	b, _ := json.Marshal(l)
	return string(b)
}
