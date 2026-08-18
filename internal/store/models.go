package store

import "time"

// ---- Permissions ----------------------------------------------------------

// Global permission keys. Admin bypasses all checks. Panel settings and AI
// settings are hard-wired to admin only and have no key on purpose.
const (
	PermServersManage = "servers.manage" // create / import / delete servers
	PermUsersManage   = "users.manage"
	PermRolesManage   = "roles.manage"
	PermAuditView     = "audit.view"
	PermAIUse         = "ai.use" // master switch for all AI features for a user
	PermAPIKeys       = "apikeys.manage"
)

// Per-server permission keys.
const (
	SPermView           = "view"
	SPermStart          = "start"
	SPermStop           = "stop"
	SPermRestart        = "restart"
	SPermKill           = "kill"
	SPermConsoleView    = "console.view"
	SPermConsoleSend    = "console.send"
	SPermFilesView      = "files.view"
	SPermFilesEdit      = "files.edit"
	SPermFilesDownload  = "files.download"
	SPermConfigEdit     = "config.edit"
	SPermBackupCreate   = "backups.create"
	SPermBackupRestore  = "backups.restore"
	SPermBackupDownload = "backups.download"
	SPermBackupDelete   = "backups.delete"
	SPermWebhooksManage = "webhooks.manage"
	SPermAIAsk          = "ai.ask"
	SPermAIAgent        = "ai.agent"
	SPermSchedules      = "schedules.manage"
	SPermPlayers        = "players.manage" // whitelist / ops / bans / kick
)

// GlobalPermKeys lists every global permission for UIs.
var GlobalPermKeys = []string{
	PermServersManage, PermUsersManage, PermRolesManage, PermAuditView,
	PermAIUse, PermAPIKeys,
}

// ServerPermKeys lists every per-server permission for UIs.
var ServerPermKeys = []string{
	SPermView, SPermStart, SPermStop, SPermRestart, SPermKill,
	SPermConsoleView, SPermConsoleSend,
	SPermFilesView, SPermFilesEdit, SPermFilesDownload,
	SPermConfigEdit,
	SPermBackupCreate, SPermBackupRestore, SPermBackupDownload, SPermBackupDelete,
	SPermWebhooksManage, SPermAIAsk, SPermAIAgent,
	SPermSchedules, SPermPlayers,
}

// ---- Users & roles --------------------------------------------------------

type User struct {
	ID           string `json:"id"`
	Username     string `json:"username"`
	PasswordHash string `json:"password_hash"`
	IsAdmin      bool   `json:"is_admin"`
	Disabled     bool   `json:"disabled"`
	MustChangePW bool   `json:"must_change_pw"`

	RoleID string `json:"role_id"`
	// Overrides: tri-state per-key override of the role's global perms.
	// Key present = explicit allow/deny regardless of role.
	Overrides map[string]bool `json:"overrides,omitempty"`
	// ServerOverrides: serverID (or "*") -> perm -> allow/deny.
	ServerOverrides map[string]map[string]bool `json:"server_overrides,omitempty"`

	TOTPSecret      string `json:"totp_secret,omitempty"`
	TOTPEnabled     bool   `json:"totp_enabled"`
	LastTOTPCounter int64  `json:"last_totp_counter"`

	CreatedAt time.Time `json:"created_at"`
	LastLogin time.Time `json:"last_login"`
}

type Role struct {
	ID     string          `json:"id"`
	Name   string          `json:"name"`
	Global map[string]bool `json:"global,omitempty"`
	// Servers: serverID (or "*") -> perm -> allow/deny.
	Servers map[string]map[string]bool `json:"servers,omitempty"`
}

func cloneBoolMap(m map[string]bool) map[string]bool {
	if m == nil {
		return nil
	}
	out := make(map[string]bool, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func cloneServerPerms(m map[string]map[string]bool) map[string]map[string]bool {
	if m == nil {
		return nil
	}
	out := make(map[string]map[string]bool, len(m))
	for k, v := range m {
		out[k] = cloneBoolMap(v)
	}
	return out
}

// Clone returns a deep copy. The store hands these out to request handlers,
// which read them (including permission maps) without holding the store lock,
// so they must not alias state another goroutine can mutate.
func (u *User) Clone() *User {
	if u == nil {
		return nil
	}
	c := *u
	c.Overrides = cloneBoolMap(u.Overrides)
	c.ServerOverrides = cloneServerPerms(u.ServerOverrides)
	return &c
}

// Clone returns a deep copy, for the same reason as User.Clone.
func (r *Role) Clone() *Role {
	if r == nil {
		return nil
	}
	c := *r
	c.Global = cloneBoolMap(r.Global)
	c.Servers = cloneServerPerms(r.Servers)
	return &c
}

// HasGlobal resolves a global permission for u. Resolution order:
// admin > user override > role > deny.
func (db *DB) HasGlobal(u *User, perm string) bool {
	if u == nil {
		return false
	}
	if u.IsAdmin {
		return true
	}
	if v, ok := u.Overrides[perm]; ok {
		return v
	}
	if r := db.RoleByID(u.RoleID); r != nil {
		if v, ok := r.Global[perm]; ok {
			return v
		}
	}
	return false
}

// HasServer resolves a per-server permission. Resolution order:
// admin > user[server] > user[*] > role[server] > role[*] > deny.
func (db *DB) HasServer(u *User, serverID, perm string) bool {
	if u == nil {
		return false
	}
	if u.IsAdmin {
		return true
	}
	if m, ok := u.ServerOverrides[serverID]; ok {
		if v, ok := m[perm]; ok {
			return v
		}
	}
	if m, ok := u.ServerOverrides["*"]; ok {
		if v, ok := m[perm]; ok {
			return v
		}
	}
	if r := db.RoleByID(u.RoleID); r != nil {
		if m, ok := r.Servers[serverID]; ok {
			if v, ok := m[perm]; ok {
				return v
			}
		}
		if m, ok := r.Servers["*"]; ok {
			if v, ok := m[perm]; ok {
				return v
			}
		}
	}
	return false
}

// ---- Update settings ------------------------------------------------------

// UpdateSettings controls the built-in updater. AutoUpdate is deliberately
// off by default: turning it on means unattended binary swaps and restarts.
type UpdateSettings struct {
	AutoUpdate bool `json:"auto_update"`
}

// ---- AI settings ----------------------------------------------------------

type AISettings struct {
	Enabled  bool   `json:"enabled"`
	Provider string `json:"provider"` // sglang | vllm | openrouter | lmstudio | llamacpp | custom
	BaseURL  string `json:"base_url"` // e.g. http://127.0.0.1:8000/v1
	APIKey   string `json:"api_key,omitempty"`
	Model    string `json:"model"`

	Temperature     float64 `json:"temperature"`
	MaxTokens       int     `json:"max_tokens"`
	ReasoningEffort string  `json:"reasoning_effort"` // "", low, medium, high (OpenRouter reasoning param)
	ExtraBody       string  `json:"extra_body"`       // raw JSON merged into requests (e.g. chat_template_kwargs)

	WebSearchEnabled   bool `json:"web_search_enabled"`
	ContextLines       int  `json:"context_lines"`        // console lines sent with "ask" (default 256)
	AgentMaxIterations int  `json:"agent_max_iterations"` // tool-loop cap
}

func DefaultAISettings() AISettings {
	return AISettings{
		Provider:           "lmstudio",
		BaseURL:            "http://127.0.0.1:1234/v1",
		Temperature:        0.3,
		MaxTokens:          4096,
		ContextLines:       256,
		AgentMaxIterations: 10,
	}
}

// ProviderDefaultURL returns the conventional base URL for a provider.
func ProviderDefaultURL(p string) string {
	switch p {
	case "sglang":
		return "http://127.0.0.1:30000/v1"
	case "vllm":
		return "http://127.0.0.1:8000/v1"
	case "openrouter":
		return "https://openrouter.ai/api/v1"
	case "lmstudio":
		return "http://127.0.0.1:1234/v1"
	case "llamacpp":
		return "http://127.0.0.1:8080/v1"
	}
	return ""
}

// ---- Servers --------------------------------------------------------------

type Webhook struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	URL     string   `json:"url"`
	Events  []string `json:"events"` // start, stop, crash, backup
	Enabled bool     `json:"enabled"`
}

// ScheduleAction is what a scheduled task performs.
type ScheduleAction string

const (
	ActionStart   ScheduleAction = "start"
	ActionStop    ScheduleAction = "stop"
	ActionRestart ScheduleAction = "restart"
	ActionBackup  ScheduleAction = "backup"
	ActionCommand ScheduleAction = "command"
)

// Schedule is a recurring task on a server. Two timing modes are supported:
// "interval" (every N minutes) and "daily" (at HH:MM local time, optionally
// restricted to certain weekdays).
type Schedule struct {
	ID      string         `json:"id"`
	Name    string         `json:"name"`
	Action  ScheduleAction `json:"action"`
	Command string         `json:"command,omitempty"` // for ActionCommand

	Mode        string `json:"mode"`               // interval | daily
	IntervalMin int    `json:"interval_min"`       // for mode=interval
	TimeOfDay   string `json:"time_of_day"`        // "HH:MM" for mode=daily
	Weekdays    []int  `json:"weekdays,omitempty"` // 0=Sunday; empty = every day

	Enabled  bool      `json:"enabled"`
	LastRun  time.Time `json:"last_run,omitempty"`
	LastOK   bool      `json:"last_ok,omitempty"`
	LastNote string    `json:"last_note,omitempty"`
	NextRun  time.Time `json:"next_run,omitempty"`
}

type Server struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	// Root is the absolute path of the Minecraft server directory. Managed
	// servers live under <data>/servers/<id>/data; imported servers point at
	// an existing directory and are never deleted from disk by the panel.
	Root     string `json:"root"`
	Imported bool   `json:"imported"`

	JavaPath       string `json:"java_path"` // default "java"
	Jar            string `json:"jar"`       // filename inside Root
	MinMemMB       int    `json:"min_mem_mb"`
	MaxMemMB       int    `json:"max_mem_mb"`
	JVMArgs        string `json:"jvm_args"`
	ServerArgs     string `json:"server_args"`               // after the jar, default "nogui"
	LaunchOverride string `json:"launch_override,omitempty"` // admin only: full command run via sh -c
	StopCommand    string `json:"stop_command"`
	StopGraceSecs  int    `json:"stop_grace_secs"`
	AutoRestart    bool   `json:"auto_restart"`
	AutoStart      bool   `json:"auto_start"` // start with the panel
	AcceptEula     bool   `json:"accept_eula"`

	// Download policy — editable by admin only.
	DownloadsEnabled  bool     `json:"downloads_enabled"`
	BlockedExtensions []string `json:"blocked_extensions"` // e.g. ["jar","zip"]

	// BackupKeep is the retention limit: when a new backup is created, the
	// oldest are pruned beyond this count. 0 means keep everything (up to
	// the hard per-server maximum).
	BackupKeep int `json:"backup_keep"`

	Webhooks  []*Webhook  `json:"webhooks,omitempty"`
	Schedules []*Schedule `json:"schedules,omitempty"`
}

// Clone returns a deep copy. Server holds a slice of *Webhook, so a plain
// struct copy would still share those pointers and the slice backing array
// between goroutines.
func (s *Server) Clone() *Server {
	if s == nil {
		return nil
	}
	c := *s
	if s.BlockedExtensions != nil {
		c.BlockedExtensions = append([]string(nil), s.BlockedExtensions...)
	}
	if s.Webhooks != nil {
		c.Webhooks = make([]*Webhook, len(s.Webhooks))
		for i, wh := range s.Webhooks {
			w := *wh
			if wh.Events != nil {
				w.Events = append([]string(nil), wh.Events...)
			}
			c.Webhooks[i] = &w
		}
	}
	if s.Schedules != nil {
		c.Schedules = make([]*Schedule, len(s.Schedules))
		for i, sc := range s.Schedules {
			cp := *sc
			if sc.Weekdays != nil {
				cp.Weekdays = append([]int(nil), sc.Weekdays...)
			}
			c.Schedules[i] = &cp
		}
	}
	return &c
}

// DownloadBlocked reports whether a filename is blocked by the server's
// download policy (for non-admin users).
func (s *Server) DownloadBlocked(name string) bool {
	if !s.DownloadsEnabled {
		return true
	}
	lower := name
	for i := len(name) - 1; i >= 0; i-- {
		if name[i] == '.' {
			lower = name[i+1:]
			break
		}
		if name[i] == '/' {
			break
		}
	}
	for _, ext := range s.BlockedExtensions {
		e := ext
		for len(e) > 0 && e[0] == '.' {
			e = e[1:]
		}
		if equalFold(e, lower) {
			return true
		}
	}
	return false
}

func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if 'A' <= ca && ca <= 'Z' {
			ca += 32
		}
		if 'A' <= cb && cb <= 'Z' {
			cb += 32
		}
		if ca != cb {
			return false
		}
	}
	return true
}

// ---- API keys -------------------------------------------------------------

// APIKey authenticates automation (scripts, monitoring) as a specific user.
// Only a hash of the secret is stored; the plaintext is shown once at
// creation. A key never grants more than its owning user has.
type APIKey struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	UserID    string    `json:"user_id"`
	Hash      string    `json:"hash"` // sha256 of the secret
	Prefix    string    `json:"prefix"`
	ReadOnly  bool      `json:"read_only"` // restrict to safe (GET) requests
	CreatedAt time.Time `json:"created_at"`
	LastUsed  time.Time `json:"last_used,omitempty"`
	Disabled  bool      `json:"disabled"`
}

// ---- Sessions -------------------------------------------------------------

type Session struct {
	TokenHash string    `json:"token_hash"` // sha256 of the cookie value
	UserID    string    `json:"user_id"`
	CSRF      string    `json:"csrf"`
	Expires   time.Time `json:"expires"`
	Created   time.Time `json:"created"`
	IP        string    `json:"ip"`
}

// ---- Panel config ---------------------------------------------------------

type TLSConfig struct {
	Mode       string   `json:"mode"` // http | self-signed | custom
	CertFile   string   `json:"cert_file,omitempty"`
	KeyFile    string   `json:"key_file,omitempty"`
	ExtraHosts []string `json:"extra_hosts,omitempty"` // extra SANs for the self-signed cert
}

type Config struct {
	Bind            string    `json:"bind"`
	Port            int       `json:"port"`
	TLS             TLSConfig `json:"tls"`
	SessionTTLHours int       `json:"session_ttl_hours"`
	MaxUploadMB     int64     `json:"max_upload_mb"`
	TrustProxy      bool      `json:"trust_proxy"` // honor X-Forwarded-For for audit logging
	// BehindTLSProxy marks a deployment where the panel itself speaks HTTP
	// but a reverse proxy terminates HTTPS in front of it. Without this the
	// session cookie would be issued without Secure (because the listener is
	// plaintext) and could leak over an http:// request to the same host.
	BehindTLSProxy bool `json:"behind_tls_proxy"`
}

// CookiesSecure reports whether session cookies must carry the Secure flag
// and HSTS should be sent: true when the panel serves TLS directly, or when
// it sits behind a TLS-terminating proxy.
func (c Config) CookiesSecure() bool {
	return c.TLS.Mode != "http" || c.BehindTLSProxy
}

func DefaultConfig() Config {
	return Config{
		Bind:            "0.0.0.0",
		Port:            8443,
		TLS:             TLSConfig{Mode: "self-signed"},
		SessionTTLHours: 168,
		MaxUploadMB:     2048,
	}
}

// ---- Audit ----------------------------------------------------------------

type AuditEntry struct {
	Time     time.Time `json:"time"`
	User     string    `json:"user"` // username, or "-" pre-auth
	IP       string    `json:"ip"`
	Action   string    `json:"action"`
	Target   string    `json:"target,omitempty"`
	Detail   string    `json:"detail,omitempty"`
	ServerID string    `json:"server_id,omitempty"`
}
