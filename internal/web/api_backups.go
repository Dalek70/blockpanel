package web

import (
	"fmt"
	"io"
	"net/http"
	"os"

	"blockpanel/internal/mc"
	"blockpanel/internal/webhook"
)

func (s *Server) handleBackupsList(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	list, err := mc.ListBackups(s.mgr.BackupsDir(id))
	if err != nil {
		writeFSErr(w, 500, err)
		return
	}
	writeJSON(w, 200, list)
}

func (s *Server) handleBackupCreate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	in := s.mgr.Get(id)
	cfg := in.Config()
	if in.State() != mc.StateStopped {
		in.Console().Append("[panel] creating backup while running — world data may be mid-write; prefer save-off/save-all first")
	}
	name, err := mc.CreateBackup(cfg.Root, s.mgr.BackupsDir(id))
	if err != nil {
		writeFSErr(w, 500, err)
		return
	}
	if pruned, perr := mc.PruneBackups(s.mgr.BackupsDir(id), cfg.BackupKeep); perr == nil && pruned > 0 {
		in.Console().Append(fmt.Sprintf("[panel] retention: removed %d old backup(s)", pruned))
	}
	s.audit(r, "backup.create", name, "", id)
	webhook.Notify(cfg, "backup", "Backup "+name)
	writeJSON(w, 200, map[string]string{"status": "ok", "name": name})
}

func (s *Server) handleBackupRestore(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	in := s.mgr.Get(id)
	if in.State() != mc.StateStopped {
		writeErr(w, http.StatusConflict, "stop the server before restoring a backup")
		return
	}
	name := r.PathValue("name")
	if err := mc.RestoreBackup(in.Config().Root, s.mgr.BackupsDir(id), name); err != nil {
		writeFSErr(w, http.StatusBadRequest, err)
		return
	}
	s.audit(r, "backup.restore", name, "", id)
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

// handleBackupDownload: a backup zip contains every file, including ones the
// download policy blocks — so for non-admins the policy must allow downloads
// AND have no blocked extensions, otherwise the zip is a policy bypass.
func (s *Server) handleBackupDownload(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	id := r.PathValue("id")
	in := s.mgr.Get(id)
	cfg := in.Config()
	if !u.IsAdmin && (!cfg.DownloadsEnabled || len(cfg.BlockedExtensions) > 0) {
		writeErr(w, http.StatusForbidden, "backup downloads are restricted by this server's download policy")
		return
	}
	name := r.PathValue("name")
	path, err := mc.BackupPath(s.mgr.BackupsDir(id), name)
	if err != nil {
		writeFSErr(w, http.StatusBadRequest, err)
		return
	}
	f, err := os.Open(path)
	if err != nil {
		writeErr(w, http.StatusNotFound, "no such backup")
		return
	}
	defer f.Close()
	fi, _ := f.Stat()
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name))
	if fi != nil {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", fi.Size()))
	}
	s.audit(r, "backup.download", name, "", id)
	io.Copy(w, f)
}

func (s *Server) handleBackupDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	name := r.PathValue("name")
	if err := mc.DeleteBackup(s.mgr.BackupsDir(id), name); err != nil {
		writeFSErr(w, http.StatusBadRequest, err)
		return
	}
	s.audit(r, "backup.delete", name, "", id)
	writeJSON(w, 200, map[string]string{"status": "ok"})
}
