package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"blockpanel/internal/mc"
	"blockpanel/internal/store"
	"blockpanel/internal/util"
)

// ---- Status / history ------------------------------------------------------

func (s *Server) handleServerPing(w http.ResponseWriter, r *http.Request) {
	in := s.mgr.Get(r.PathValue("id"))
	if in.State() != mc.StateRunning {
		writeJSON(w, 200, mc.PingResult{Error: "server is not running"})
		return
	}
	writeJSON(w, 200, in.LastPing())
}

func (s *Server) handleServerHistory(w http.ResponseWriter, r *http.Request) {
	in := s.mgr.Get(r.PathValue("id"))
	n, _ := strconv.Atoi(r.URL.Query().Get("points"))
	if n <= 0 || n > 720 {
		n = 240
	}
	writeJSON(w, 200, in.History().Recent(n))
}

func (s *Server) handleServerUsage(w http.ResponseWriter, r *http.Request) {
	in := s.mgr.Get(r.PathValue("id"))
	cfg := in.Config()
	writeJSON(w, 200, map[string]any{
		"disk_bytes":    mc.DirSize(cfg.Root),
		"backup_bytes":  mc.DirSize(s.mgr.BackupsDir(cfg.ID)),
		"players":       in.Players().List(),
		"player_count":  in.Players().Count(),
	})
}

// ---- server.properties -----------------------------------------------------

func (s *Server) handlePropsGet(w http.ResponseWriter, r *http.Request) {
	in := s.mgr.Get(r.PathValue("id"))
	props, err := mc.ReadProperties(in.Config().Root)
	if err != nil {
		writeFSErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, 200, props)
}

func (s *Server) handlePropsPut(w http.ResponseWriter, r *http.Request) {
	in := s.mgr.Get(r.PathValue("id"))
	var body struct {
		Updates map[string]string `json:"updates"`
	}
	if !readBody(w, r, &body, 1<<20) {
		return
	}
	if len(body.Updates) == 0 {
		writeErr(w, http.StatusBadRequest, "no changes supplied")
		return
	}
	if len(body.Updates) > 200 {
		writeErr(w, http.StatusBadRequest, "too many properties in one request")
		return
	}
	if err := mc.WriteProperties(in.Config().Root, body.Updates); err != nil {
		writeFSErr(w, http.StatusBadRequest, err)
		return
	}
	keys := make([]string, 0, len(body.Updates))
	for k := range body.Updates {
		keys = append(keys, k)
	}
	s.audit(r, "properties.update", strings.Join(keys, ","), "", r.PathValue("id"))
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

// ---- Players ---------------------------------------------------------------

func (s *Server) handlePlayersGet(w http.ResponseWriter, r *http.Request) {
	in := s.mgr.Get(r.PathValue("id"))
	root := in.Config().Root
	read := func(kind mc.PlayerListKind) []mc.PlayerEntry {
		list, err := mc.ReadPlayerList(root, kind)
		if err != nil {
			return []mc.PlayerEntry{}
		}
		return list
	}
	writeJSON(w, 200, map[string]any{
		"online":    in.Players().List(),
		"whitelist": read(mc.ListWhitelist),
		"ops":       read(mc.ListOps),
		"bans":      read(mc.ListBans),
	})
}

// handlePlayerAction runs a moderation action. While the server is running
// the action goes through the console so the server updates its own state;
// removals also work offline by editing the JSON list directly.
func (s *Server) handlePlayerAction(w http.ResponseWriter, r *http.Request) {
	in := s.mgr.Get(r.PathValue("id"))
	var body struct {
		Action string `json:"action"` // whitelist_add|whitelist_remove|op|deop|ban|pardon|kick
		Player string `json:"player"`
		Reason string `json:"reason,omitempty"`
	}
	if !readBody(w, r, &body, 1<<16) {
		return
	}
	if !mc.ValidPlayerName(body.Player) {
		writeErr(w, http.StatusBadRequest, "invalid player name")
		return
	}
	// Reason is interpolated into a console command, so keep it to one line
	// of safe characters.
	reason := strings.TrimSpace(body.Reason)
	if strings.ContainsAny(reason, "\n\r") || len(reason) > 100 {
		writeErr(w, http.StatusBadRequest, "invalid reason")
		return
	}

	running := in.State() == mc.StateRunning
	var command string
	var offline func() error
	root := in.Config().Root

	switch body.Action {
	case "whitelist_add":
		command = "whitelist add " + body.Player
	case "whitelist_remove":
		command = "whitelist remove " + body.Player
		offline = func() error { return mc.RemoveFromPlayerList(root, mc.ListWhitelist, body.Player) }
	case "op":
		command = "op " + body.Player
	case "deop":
		command = "deop " + body.Player
		offline = func() error { return mc.RemoveFromPlayerList(root, mc.ListOps, body.Player) }
	case "ban":
		command = "ban " + body.Player
		if reason != "" {
			command += " " + reason
		}
	case "pardon":
		command = "pardon " + body.Player
		offline = func() error { return mc.RemoveFromPlayerList(root, mc.ListBans, body.Player) }
	case "kick":
		if !running {
			writeErr(w, http.StatusConflict, "the server must be running to kick a player")
			return
		}
		command = "kick " + body.Player
		if reason != "" {
			command += " " + reason
		}
	default:
		writeErr(w, http.StatusBadRequest, "unknown action")
		return
	}

	if running {
		if err := in.SendCommand(command); err != nil {
			writeErr(w, http.StatusConflict, err.Error())
			return
		}
	} else {
		if offline == nil {
			writeErr(w, http.StatusConflict, "the server must be running for this action")
			return
		}
		if err := offline(); err != nil {
			writeFSErr(w, http.StatusBadRequest, err)
			return
		}
	}
	s.audit(r, "player."+body.Action, body.Player, reason, r.PathValue("id"))
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

// ---- Schedules -------------------------------------------------------------

func (s *Server) handleSchedulesList(w http.ResponseWriter, r *http.Request) {
	in := s.mgr.Get(r.PathValue("id"))
	list := in.Config().Schedules
	if list == nil {
		list = []*store.Schedule{}
	}
	writeJSON(w, 200, list)
}

const maxSchedulesPerServer = 25

func (s *Server) handleScheduleCreate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var sc store.Schedule
	if !readBody(w, r, &sc, 1<<16) {
		return
	}
	sc.ID = util.NewID()
	sc.LastRun = time.Time{}
	if err := mc.ValidateSchedule(&sc); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	sc.NextRun = mc.NextRun(&sc, time.Now())
	cfg, err := s.mgr.MutateConfig(id, func(c *store.Server) error {
		if len(c.Schedules) >= maxSchedulesPerServer {
			return fmt.Errorf("this server already has the maximum of %d schedules", maxSchedulesPerServer)
		}
		c.Schedules = append(c.Schedules, &sc)
		return nil
	})
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s.audit(r, "schedule.create", sc.Name, string(sc.Action), id)
	writeJSON(w, 200, cfg.Schedules)
}

var errNoSuchSchedule = errors.New("no such schedule")

func (s *Server) handleScheduleUpdate(w http.ResponseWriter, r *http.Request) {
	id, sid := r.PathValue("id"), r.PathValue("sid")
	var body store.Schedule
	if !readBody(w, r, &body, 1<<16) {
		return
	}
	cfg, err := s.mgr.MutateConfig(id, func(c *store.Server) error {
		for _, cur := range c.Schedules {
			if cur.ID != sid {
				continue
			}
			next := *cur
			next.Name = body.Name
			next.Action = body.Action
			next.Command = body.Command
			next.Mode = body.Mode
			next.IntervalMin = body.IntervalMin
			next.TimeOfDay = body.TimeOfDay
			next.Weekdays = body.Weekdays
			next.Enabled = body.Enabled
			if err := mc.ValidateSchedule(&next); err != nil {
				return err
			}
			next.NextRun = mc.NextRun(&next, time.Now())
			*cur = next
			return nil
		}
		return errNoSuchSchedule
	})
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, errNoSuchSchedule) {
			status = http.StatusNotFound
		}
		writeErr(w, status, err.Error())
		return
	}
	s.audit(r, "schedule.update", sid, "", id)
	writeJSON(w, 200, cfg.Schedules)
}

func (s *Server) handleScheduleDelete(w http.ResponseWriter, r *http.Request) {
	id, sid := r.PathValue("id"), r.PathValue("sid")
	cfg, err := s.mgr.MutateConfig(id, func(c *store.Server) error {
		for i, cur := range c.Schedules {
			if cur.ID == sid {
				c.Schedules = append(c.Schedules[:i], c.Schedules[i+1:]...)
				return nil
			}
		}
		return errNoSuchSchedule
	})
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, errNoSuchSchedule) {
			status = http.StatusNotFound
		}
		writeErr(w, status, err.Error())
		return
	}
	s.audit(r, "schedule.delete", sid, "", id)
	writeJSON(w, 200, cfg.Schedules)
}

// handleScheduleRun fires a schedule immediately, for testing it.
func (s *Server) handleScheduleRun(w http.ResponseWriter, r *http.Request) {
	id, sid := r.PathValue("id"), r.PathValue("sid")
	in := s.mgr.Get(id)
	for _, sc := range in.Config().Schedules {
		if sc.ID == sid {
			s.audit(r, "schedule.run_now", sc.Name, "", id)
			go s.sched.RunNow(id, *sc)
			writeJSON(w, 200, map[string]string{"status": "started"})
			return
		}
	}
	writeErr(w, http.StatusNotFound, "no such schedule")
}

// ---- Jar installer ---------------------------------------------------------

func (s *Server) handleVersionsList(w http.ResponseWriter, r *http.Request) {
	flavor := mc.ServerFlavor(r.URL.Query().Get("type"))
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	versions, err := mc.ListVersions(ctx, safeHTTPClient(20*time.Second), flavor)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	if len(versions) > 200 {
		versions = versions[:200]
	}
	writeJSON(w, 200, map[string]any{"type": flavor, "versions": versions})
}

func (s *Server) handleJarInstall(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	in := s.mgr.Get(id)
	var body struct {
		Type    string `json:"type"`
		Version string `json:"version"`
	}
	if !readBody(w, r, &body, 1<<16) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	client := safeHTTPClient(15 * time.Minute)
	url, filename, err := mc.ResolveDownload(ctx, client, mc.ServerFlavor(body.Type), body.Version)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	size, err := s.downloadJar(client, in.Config().Root, url, filename)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	if _, err := s.mgr.MutateConfig(id, func(c *store.Server) error {
		c.Jar = filename
		return nil
	}); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.audit(r, "jar.install", body.Type+" "+body.Version, filename, id)
	writeJSON(w, 200, map[string]string{
		"status": "ok", "jar": filename, "size": util.HumanBytes(size),
	})
}

// ---- Java runtimes ---------------------------------------------------------

func (s *Server) handleJavaList(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, mc.DetectJava())
}

// ---- File manager extras ---------------------------------------------------

func (s *Server) handleFileArchive(w http.ResponseWriter, r *http.Request) {
	in := s.mgr.Get(r.PathValue("id"))
	var body struct {
		Paths []string `json:"paths"`
		Dest  string   `json:"dest"`
	}
	if !readBody(w, r, &body, 1<<20) {
		return
	}
	if len(body.Paths) == 0 || len(body.Paths) > 500 {
		writeErr(w, http.StatusBadRequest, "select between 1 and 500 items")
		return
	}
	if strings.TrimSpace(body.Dest) == "" {
		writeErr(w, http.StatusBadRequest, "destination filename required")
		return
	}
	if err := mc.CreateArchive(in.Config().Root, body.Paths, body.Dest); err != nil {
		writeFSErr(w, http.StatusBadRequest, err)
		return
	}
	s.audit(r, "files.archive", body.Dest, fmt.Sprintf("%d items", len(body.Paths)), r.PathValue("id"))
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

func (s *Server) handleFileExtract(w http.ResponseWriter, r *http.Request) {
	in := s.mgr.Get(r.PathValue("id"))
	var body struct {
		Path string `json:"path"`
		Dest string `json:"dest"`
	}
	if !readBody(w, r, &body, 1<<16) {
		return
	}
	dest := body.Dest
	if strings.TrimSpace(dest) == "" {
		dest = filepath.Dir(body.Path)
	}
	if err := mc.ExtractArchive(in.Config().Root, body.Path, dest); err != nil {
		writeFSErr(w, http.StatusBadRequest, err)
		return
	}
	s.audit(r, "files.extract", body.Path, dest, r.PathValue("id"))
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

func (s *Server) handleFileSearch(w http.ResponseWriter, r *http.Request) {
	in := s.mgr.Get(r.PathValue("id"))
	q := r.URL.Query()
	results, err := mc.SearchFiles(in.Config().Root, q.Get("path"), q.Get("q"), 200)
	if err != nil {
		writeFSErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, 200, results)
}

// handleFileDeleteBatch removes several paths in one request.
func (s *Server) handleFileDeleteBatch(w http.ResponseWriter, r *http.Request) {
	in := s.mgr.Get(r.PathValue("id"))
	var body struct {
		Paths []string `json:"paths"`
	}
	if !readBody(w, r, &body, 1<<20) {
		return
	}
	if len(body.Paths) == 0 || len(body.Paths) > 500 {
		writeErr(w, http.StatusBadRequest, "select between 1 and 500 items")
		return
	}
	root := in.Config().Root
	var failed []string
	for _, p := range body.Paths {
		if err := mc.Delete(root, p); err != nil {
			failed = append(failed, filepath.Base(p))
		}
	}
	s.audit(r, "files.delete_batch", fmt.Sprintf("%d items", len(body.Paths)), "", r.PathValue("id"))
	if len(failed) > 0 {
		writeJSON(w, 207, map[string]any{"status": "partial", "failed": failed})
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

// ---- Console log download --------------------------------------------------

func (s *Server) handleConsoleDownload(w http.ResponseWriter, r *http.Request) {
	in := s.mgr.Get(r.PathValue("id"))
	name := util.Slugify(in.Config().Name) + "-console.log"
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name))
	s.audit(r, "console.download", "", "", r.PathValue("id"))
	fmt.Fprint(w, in.Console().LastText(0))
}

func (s *Server) handleConsoleSearch(w http.ResponseWriter, r *http.Request) {
	in := s.mgr.Get(r.PathValue("id"))
	q := r.URL.Query().Get("q")
	if strings.TrimSpace(q) == "" {
		writeErr(w, http.StatusBadRequest, "search term required")
		return
	}
	writeJSON(w, 200, in.Console().Search(q, 200))
}
