package web

import (
	"context"
	"crypto/tls"
	"embed"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"blockpanel/internal/ai"
	"blockpanel/internal/auth"
	"blockpanel/internal/mc"
	"blockpanel/internal/store"
	"blockpanel/internal/update"
)

//go:embed static
var staticFS embed.FS

// Server wires every subsystem into an http.Server.
type Server struct {
	db      *store.DB
	mgr     *mc.Manager
	cfg     store.Config
	runner  *ai.Runner
	sched   *mc.Scheduler
	dataDir string

	loginLimiter *auth.RateLimiter
	totpLimiter  *auth.RateLimiter
	// sensitiveLimiter guards authenticated endpoints that perform a PBKDF2
	// verification (password change, TOTP enrol/disable).
	sensitiveLimiter *auth.RateLimiter

	// pending 2FA logins: token -> userID + expiry
	pendingMu sync.Mutex
	pending   map[string]pendingLogin

	upd *update.Manager

	httpSrv *http.Server
}

type pendingLogin struct {
	userID  string
	expires time.Time
}

func New(db *store.DB, mgr *mc.Manager, cfg store.Config, runner *ai.Runner, sched *mc.Scheduler, upd *update.Manager) *Server {
	s := &Server{
		db:               db,
		mgr:              mgr,
		cfg:              cfg,
		runner:           runner,
		sched:            sched,
		dataDir:          db.DataDir(),
		loginLimiter:     auth.NewRateLimiter(10, time.Minute),
		totpLimiter:      auth.NewRateLimiter(8, time.Minute),
		sensitiveLimiter: auth.NewRateLimiter(12, time.Minute),
		pending:          map[string]pendingLogin{},
		upd:              upd,
	}
	upd.PrepareRestart = mgr.StopAll
	upd.AutoEnabled = func() bool { return db.UpdateSettings().AutoUpdate }
	upd.Audit = func(action, detail string) {
		db.Audit(store.AuditEntry{Action: action, Detail: detail, User: "system"})
	}
	return s
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()

	// --- auth / session ---
	mux.HandleFunc("GET /api/setup/status", s.handleSetupStatus)
	mux.HandleFunc("POST /api/setup", s.handleSetup)
	mux.HandleFunc("POST /api/auth/login", s.handleLogin)
	mux.HandleFunc("POST /api/auth/totp", s.handleLoginTOTP)
	mux.HandleFunc("POST /api/auth/logout", s.auth(s.handleLogout))
	mux.HandleFunc("GET /api/me", s.auth(s.handleMe))
	mux.HandleFunc("POST /api/me/password", s.auth(s.throttleSensitive(s.handleChangePassword)))
	mux.HandleFunc("POST /api/me/totp/begin", s.auth(s.throttleSensitive(s.handleTOTPBegin)))
	mux.HandleFunc("POST /api/me/totp/confirm", s.auth(s.throttleSensitive(s.handleTOTPConfirm)))
	mux.HandleFunc("POST /api/me/totp/disable", s.auth(s.throttleSensitive(s.handleTOTPDisable)))

	// --- users & roles ---
	mux.HandleFunc("GET /api/users", s.auth(s.requireGlobal(store.PermUsersManage, s.handleUsersList)))
	mux.HandleFunc("POST /api/users", s.auth(s.requireGlobal(store.PermUsersManage, s.handleUserCreate)))
	mux.HandleFunc("PATCH /api/users/{id}", s.auth(s.requireGlobal(store.PermUsersManage, s.handleUserUpdate)))
	mux.HandleFunc("DELETE /api/users/{id}", s.auth(s.requireGlobal(store.PermUsersManage, s.handleUserDelete)))
	mux.HandleFunc("POST /api/users/{id}/password", s.auth(s.requireGlobal(store.PermUsersManage, s.handleUserSetPassword)))
	mux.HandleFunc("POST /api/users/{id}/totp/reset", s.auth(s.requireGlobal(store.PermUsersManage, s.handleUserTOTPReset)))
	mux.HandleFunc("GET /api/roles", s.auth(s.handleRolesList))
	mux.HandleFunc("POST /api/roles", s.auth(s.requireGlobal(store.PermRolesManage, s.handleRoleCreate)))
	mux.HandleFunc("PATCH /api/roles/{id}", s.auth(s.requireGlobal(store.PermRolesManage, s.handleRoleUpdate)))
	mux.HandleFunc("DELETE /api/roles/{id}", s.auth(s.requireGlobal(store.PermRolesManage, s.handleRoleDelete)))
	mux.HandleFunc("GET /api/perms", s.auth(s.handlePermKeys))

	// --- servers ---
	mux.HandleFunc("GET /api/servers", s.auth(s.handleServersList))
	mux.HandleFunc("POST /api/servers", s.auth(s.requireGlobal(store.PermServersManage, s.handleServerCreate)))
	mux.HandleFunc("GET /api/servers/{id}", s.auth(s.requireServer(store.SPermView, s.handleServerGet)))
	mux.HandleFunc("PATCH /api/servers/{id}", s.auth(s.requireServer(store.SPermConfigEdit, s.handleServerUpdate)))
	mux.HandleFunc("DELETE /api/servers/{id}", s.auth(s.requireGlobal(store.PermServersManage, s.handleServerDelete)))
	mux.HandleFunc("POST /api/servers/{id}/start", s.auth(s.requireServer(store.SPermStart, s.handleServerStart)))
	mux.HandleFunc("POST /api/servers/{id}/stop", s.auth(s.requireServer(store.SPermStop, s.handleServerStop)))
	mux.HandleFunc("POST /api/servers/{id}/restart", s.auth(s.requireServer(store.SPermRestart, s.handleServerRestart)))
	mux.HandleFunc("POST /api/servers/{id}/kill", s.auth(s.requireServer(store.SPermKill, s.handleServerKill)))
	mux.HandleFunc("POST /api/servers/{id}/command", s.auth(s.requireServer(store.SPermConsoleSend, s.handleServerCommand)))
	mux.HandleFunc("GET /api/servers/{id}/console", s.auth(s.requireServer(store.SPermConsoleView, s.handleConsoleTail)))
	mux.HandleFunc("GET /api/servers/{id}/console/stream", s.auth(s.requireServer(store.SPermConsoleView, s.handleConsoleStream)))
	mux.HandleFunc("GET /api/servers/{id}/console/search", s.auth(s.requireServer(store.SPermConsoleView, s.handleConsoleSearch)))
	mux.HandleFunc("GET /api/servers/{id}/console/download", s.auth(s.requireServer(store.SPermConsoleView, s.handleConsoleDownload)))

	// --- status, history, players, properties, schedules ---
	mux.HandleFunc("GET /api/servers/{id}/ping", s.auth(s.requireServer(store.SPermView, s.handleServerPing)))
	mux.HandleFunc("GET /api/servers/{id}/history", s.auth(s.requireServer(store.SPermView, s.handleServerHistory)))
	mux.HandleFunc("GET /api/servers/{id}/usage", s.auth(s.requireServer(store.SPermView, s.handleServerUsage)))
	mux.HandleFunc("GET /api/servers/{id}/properties", s.auth(s.requireServer(store.SPermConfigEdit, s.handlePropsGet)))
	mux.HandleFunc("PUT /api/servers/{id}/properties", s.auth(s.requireServer(store.SPermConfigEdit, s.handlePropsPut)))
	mux.HandleFunc("GET /api/servers/{id}/players", s.auth(s.requireServer(store.SPermView, s.handlePlayersGet)))
	mux.HandleFunc("POST /api/servers/{id}/players/action", s.auth(s.requireServer(store.SPermPlayers, s.handlePlayerAction)))
	mux.HandleFunc("GET /api/servers/{id}/schedules", s.auth(s.requireServer(store.SPermSchedules, s.handleSchedulesList)))
	mux.HandleFunc("POST /api/servers/{id}/schedules", s.auth(s.requireServer(store.SPermSchedules, s.handleScheduleCreate)))
	mux.HandleFunc("PATCH /api/servers/{id}/schedules/{sid}", s.auth(s.requireServer(store.SPermSchedules, s.handleScheduleUpdate)))
	mux.HandleFunc("DELETE /api/servers/{id}/schedules/{sid}", s.auth(s.requireServer(store.SPermSchedules, s.handleScheduleDelete)))
	mux.HandleFunc("POST /api/servers/{id}/schedules/{sid}/run", s.auth(s.requireServer(store.SPermSchedules, s.handleScheduleRun)))

	// --- jar installer & java ---
	mux.HandleFunc("GET /api/versions", s.auth(s.handleVersionsList))
	mux.HandleFunc("POST /api/servers/{id}/jar-install", s.auth(s.requireServer(store.SPermConfigEdit, s.handleJarInstall)))
	mux.HandleFunc("GET /api/java", s.auth(s.requireAdmin(s.handleJavaList)))

	// --- file manager extras ---
	mux.HandleFunc("GET /api/servers/{id}/files/search", s.auth(s.requireServer(store.SPermFilesView, s.handleFileSearch)))
	mux.HandleFunc("POST /api/servers/{id}/files/archive", s.auth(s.requireServer(store.SPermFilesEdit, s.handleFileArchive)))
	mux.HandleFunc("POST /api/servers/{id}/files/extract", s.auth(s.requireServer(store.SPermFilesEdit, s.handleFileExtract)))
	mux.HandleFunc("POST /api/servers/{id}/files/delete-batch", s.auth(s.requireServer(store.SPermFilesEdit, s.handleFileDeleteBatch)))

	// --- API keys ---
	mux.HandleFunc("GET /api/apikeys", s.auth(s.requireGlobal(store.PermAPIKeys, s.handleAPIKeysList)))
	mux.HandleFunc("POST /api/apikeys", s.auth(s.requireGlobal(store.PermAPIKeys, s.handleAPIKeyCreate)))
	mux.HandleFunc("DELETE /api/apikeys/{id}", s.auth(s.requireGlobal(store.PermAPIKeys, s.handleAPIKeyDelete)))

	// --- files ---
	mux.HandleFunc("GET /api/servers/{id}/files", s.auth(s.requireServer(store.SPermFilesView, s.handleFilesList)))
	mux.HandleFunc("GET /api/servers/{id}/files/read", s.auth(s.requireServer(store.SPermFilesView, s.handleFileRead)))
	mux.HandleFunc("PUT /api/servers/{id}/files/write", s.auth(s.requireServer(store.SPermFilesEdit, s.handleFileWrite)))
	mux.HandleFunc("POST /api/servers/{id}/files/mkdir", s.auth(s.requireServer(store.SPermFilesEdit, s.handleFileMkdir)))
	mux.HandleFunc("POST /api/servers/{id}/files/rename", s.auth(s.requireServer(store.SPermFilesEdit, s.handleFileRename)))
	mux.HandleFunc("POST /api/servers/{id}/files/delete", s.auth(s.requireServer(store.SPermFilesEdit, s.handleFileDelete)))
	mux.HandleFunc("POST /api/servers/{id}/files/upload", s.auth(s.requireServer(store.SPermFilesEdit, s.handleFileUpload)))
	mux.HandleFunc("GET /api/servers/{id}/files/download", s.auth(s.requireServer(store.SPermFilesDownload, s.handleFileDownload)))
	mux.HandleFunc("POST /api/servers/{id}/jar-url", s.auth(s.requireServer(store.SPermConfigEdit, s.handleJarURL)))

	// --- backups ---
	mux.HandleFunc("GET /api/servers/{id}/backups", s.auth(s.requireServer(store.SPermView, s.handleBackupsList)))
	mux.HandleFunc("POST /api/servers/{id}/backups", s.auth(s.requireServer(store.SPermBackupCreate, s.handleBackupCreate)))
	mux.HandleFunc("POST /api/servers/{id}/backups/{name}/restore", s.auth(s.requireServer(store.SPermBackupRestore, s.handleBackupRestore)))
	mux.HandleFunc("GET /api/servers/{id}/backups/{name}/download", s.auth(s.requireServer(store.SPermBackupDownload, s.handleBackupDownload)))
	mux.HandleFunc("DELETE /api/servers/{id}/backups/{name}", s.auth(s.requireServer(store.SPermBackupDelete, s.handleBackupDelete)))

	// --- webhooks ---
	mux.HandleFunc("GET /api/servers/{id}/webhooks", s.auth(s.requireServer(store.SPermWebhooksManage, s.handleWebhooksList)))
	mux.HandleFunc("POST /api/servers/{id}/webhooks", s.auth(s.requireServer(store.SPermWebhooksManage, s.handleWebhookCreate)))
	mux.HandleFunc("PATCH /api/servers/{id}/webhooks/{whid}", s.auth(s.requireServer(store.SPermWebhooksManage, s.handleWebhookUpdate)))
	mux.HandleFunc("DELETE /api/servers/{id}/webhooks/{whid}", s.auth(s.requireServer(store.SPermWebhooksManage, s.handleWebhookDelete)))
	mux.HandleFunc("POST /api/servers/{id}/webhooks/{whid}/test", s.auth(s.requireServer(store.SPermWebhooksManage, s.handleWebhookTest)))

	// --- AI ---
	mux.HandleFunc("GET /api/ai/settings", s.auth(s.requireAdmin(s.handleAISettingsGet)))
	mux.HandleFunc("PUT /api/ai/settings", s.auth(s.requireAdmin(s.handleAISettingsPut)))
	mux.HandleFunc("POST /api/ai/test", s.auth(s.requireAdmin(s.handleAITest)))
	mux.HandleFunc("GET /api/ai/status", s.auth(s.handleAIStatus))
	mux.HandleFunc("POST /api/servers/{id}/ai/ask", s.auth(s.requireServer(store.SPermAIAsk, s.handleAIAsk)))
	mux.HandleFunc("POST /api/servers/{id}/ai/agent", s.auth(s.requireServer(store.SPermAIAgent, s.handleAIAgent)))
	mux.HandleFunc("POST /api/servers/{id}/ai/agent/reset", s.auth(s.requireServer(store.SPermAIAgent, s.handleAIAgentReset)))
	mux.HandleFunc("POST /api/ai/approvals/{id}", s.auth(s.handleAIApproval))

	// --- admin ---
	mux.HandleFunc("GET /api/settings", s.auth(s.requireAdmin(s.handleSettingsGet)))
	mux.HandleFunc("PUT /api/settings", s.auth(s.requireAdmin(s.handleSettingsPut)))
	mux.HandleFunc("GET /api/update/status", s.auth(s.requireAdmin(s.handleUpdateStatus)))
	mux.HandleFunc("POST /api/update/check", s.auth(s.requireAdmin(s.handleUpdateCheck)))
	mux.HandleFunc("POST /api/update/apply", s.auth(s.requireAdmin(s.handleUpdateApply)))
	mux.HandleFunc("PUT /api/update/settings", s.auth(s.requireAdmin(s.handleUpdateSettingsPut)))
	mux.HandleFunc("GET /api/audit", s.auth(s.requireGlobal(store.PermAuditView, s.handleAudit)))
	mux.HandleFunc("POST /api/restart", s.auth(s.requireAdmin(s.handleRestart)))

	// --- static SPA ---
	staticRoot, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err)
	}
	fileServer := http.FileServer(http.FS(staticRoot))
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p == "" {
			p = "index.html"
		}
		if _, err := fs.Stat(staticRoot, p); err != nil {
			writeErr(w, http.StatusNotFound, "not found")
			return
		}
		// Embedded files carry no modtime, so browsers would heuristically
		// cache stale UI across panel upgrades.
		w.Header().Set("Cache-Control", "no-cache")
		fileServer.ServeHTTP(w, r)
	})

	return s.securityHeaders(s.csrf(mux))
}

// Start runs the HTTP(S) server until ctx is canceled.
func (s *Server) Start(ctx context.Context) error {
	addr := net.JoinHostPort(s.cfg.Bind, fmt.Sprintf("%d", s.cfg.Port))
	s.httpSrv = &http.Server{
		Addr:              addr,
		Handler:           s.routes(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	errCh := make(chan error, 1)
	scheme := "http"
	if s.cfg.TLS.Mode != "http" {
		scheme = "https"
		certFile, keyFile, err := s.ensureTLS()
		if err != nil {
			return fmt.Errorf("tls setup: %w", err)
		}
		s.httpSrv.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12}
		go func() { errCh <- s.httpSrv.ListenAndServeTLS(certFile, keyFile) }()
	} else {
		go func() { errCh <- s.httpSrv.ListenAndServe() }()
	}
	log.Printf("panel listening on %s://%s", scheme, addr)

	go func() {
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.db.PruneSessions()
			}
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		s.httpSrv.Shutdown(shutCtx)
		return nil
	}
}

func (s *Server) sessionTTL() time.Duration {
	h := s.cfg.SessionTTLHours
	if h <= 0 {
		h = 168
	}
	return time.Duration(h) * time.Hour
}

func (s *Server) maxUpload() int64 {
	mb := s.cfg.MaxUploadMB
	if mb <= 0 {
		mb = 2048
	}
	return mb << 20
}
