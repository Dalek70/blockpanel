// Package store persists panel state as JSON files under the data directory.
// Scale target is a small admin panel (tens of users), so a mutex-guarded
// in-memory struct with atomic file writes is deliberate: zero dependencies,
// trivially backed up, human-inspectable.
package store

import (
	"bufio"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"blockpanel/internal/util"
)

var ErrNotFound = errors.New("not found")

type panelFile struct {
	Users   []*User        `json:"users"`
	Roles   []*Role        `json:"roles"`
	AI      AISettings     `json:"ai"`
	APIKeys []*APIKey      `json:"api_keys,omitempty"`
	Update  UpdateSettings `json:"update"`
}

type DB struct {
	mu       sync.RWMutex
	dataDir  string
	users    []*User
	roles    []*Role
	ai       AISettings
	update   UpdateSettings
	apiKeys  []*APIKey
	sessions map[string]*Session // key: token hash

	auditMu   sync.Mutex
	auditFile *os.File
}

func Open(dataDir string) (*DB, error) {
	for _, d := range []string{dataDir, filepath.Join(dataDir, "servers"), filepath.Join(dataDir, "certs"), filepath.Join(dataDir, "logs")} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			return nil, err
		}
	}
	db := &DB{dataDir: dataDir, sessions: map[string]*Session{}, ai: DefaultAISettings()}

	var pf panelFile
	err := util.ReadJSON(db.panelPath(), &pf)
	switch {
	case err == nil:
		db.users, db.roles, db.ai, db.apiKeys, db.update = pf.Users, pf.Roles, pf.AI, pf.APIKeys, pf.Update
		if db.ai.ContextLines <= 0 {
			db.ai.ContextLines = 256
		}
		if db.ai.AgentMaxIterations <= 0 {
			db.ai.AgentMaxIterations = 10
		}
	case os.IsNotExist(err):
		// first run
	default:
		return nil, fmt.Errorf("read panel.json: %w", err)
	}

	var sess map[string]*Session
	if err := util.ReadJSON(db.sessionsPath(), &sess); err == nil {
		now := time.Now()
		for k, s := range sess {
			if s.Expires.After(now) {
				db.sessions[k] = s
			}
		}
	}

	f, err := os.OpenFile(filepath.Join(dataDir, "logs", "audit.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	db.auditFile = f
	return db, nil
}

func (db *DB) DataDir() string      { return db.dataDir }
func (db *DB) ServersDir() string   { return filepath.Join(db.dataDir, "servers") }
func (db *DB) panelPath() string    { return filepath.Join(db.dataDir, "panel.json") }
func (db *DB) sessionsPath() string { return filepath.Join(db.dataDir, "sessions.json") }

// save must be called with mu held (write lock).
func (db *DB) save() error {
	return util.WriteJSONAtomic(db.panelPath(), panelFile{
		Users: db.users, Roles: db.roles, AI: db.ai, APIKeys: db.apiKeys, Update: db.update,
	})
}

// ---- API keys -------------------------------------------------------------

func (db *DB) APIKeys() []*APIKey {
	db.mu.RLock()
	defer db.mu.RUnlock()
	out := make([]*APIKey, len(db.apiKeys))
	for i, k := range db.apiKeys {
		c := *k
		out[i] = &c
	}
	return out
}

// CreateAPIKey stores the hash of secret and returns the stored record.
func (db *DB) CreateAPIKey(k *APIKey, secret string) error {
	if strings.TrimSpace(k.Name) == "" {
		return errors.New("key name required")
	}
	k.ID = util.NewID()
	k.Hash = HashToken(secret)
	k.Prefix = secret[:8]
	k.CreatedAt = time.Now()
	db.mu.Lock()
	defer db.mu.Unlock()
	if len(db.apiKeys) >= 100 {
		return errors.New("too many API keys")
	}
	db.apiKeys = append(db.apiKeys, k)
	return db.save()
}

// APIKeyBySecret resolves a presented secret to its key and owning user.
func (db *DB) APIKeyBySecret(secret string) (*APIKey, *User) {
	h := HashToken(secret)
	db.mu.Lock()
	defer db.mu.Unlock()
	for _, k := range db.apiKeys {
		if subtle.ConstantTimeCompare([]byte(k.Hash), []byte(h)) != 1 || k.Disabled {
			continue
		}
		for _, u := range db.users {
			if u.ID == k.UserID && !u.Disabled {
				k.LastUsed = time.Now()
				kc, uc := *k, u.Clone()
				return &kc, uc
			}
		}
		return nil, nil
	}
	return nil, nil
}

func (db *DB) DeleteAPIKey(id string) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	for i, k := range db.apiKeys {
		if k.ID == id {
			db.apiKeys = append(db.apiKeys[:i], db.apiKeys[i+1:]...)
			return db.save()
		}
	}
	return ErrNotFound
}

// DeleteUserAPIKeys removes every key owned by a user (called when the
// account is deleted, so its keys cannot outlive it).
func (db *DB) DeleteUserAPIKeys(userID string) {
	db.mu.Lock()
	defer db.mu.Unlock()
	kept := db.apiKeys[:0]
	for _, k := range db.apiKeys {
		if k.UserID != userID {
			kept = append(kept, k)
		}
	}
	db.apiKeys = kept
	db.save()
}

func (db *DB) saveSessions() error {
	return util.WriteJSONAtomic(db.sessionsPath(), db.sessions)
}

// ---- Users ----------------------------------------------------------------

func (db *DB) UserCount() int {
	db.mu.RLock()
	defer db.mu.RUnlock()
	return len(db.users)
}

// Users returns deep copies. Callers read these outside the store lock (in
// request handlers, permission checks and JSON rendering), so returning live
// pointers would race with UpdateUser writing the same structs.
func (db *DB) Users() []*User {
	db.mu.RLock()
	defer db.mu.RUnlock()
	out := make([]*User, len(db.users))
	for i, u := range db.users {
		out[i] = u.Clone()
	}
	return out
}

func (db *DB) UserByID(id string) *User {
	db.mu.RLock()
	defer db.mu.RUnlock()
	for _, u := range db.users {
		if u.ID == id {
			return u.Clone()
		}
	}
	return nil
}

func (db *DB) UserByName(name string) *User {
	name = strings.ToLower(strings.TrimSpace(name))
	db.mu.RLock()
	defer db.mu.RUnlock()
	for _, u := range db.users {
		if u.Username == name {
			return u.Clone()
		}
	}
	return nil
}

func (db *DB) CreateUser(u *User) error {
	u.Username = strings.ToLower(strings.TrimSpace(u.Username))
	if u.Username == "" {
		return errors.New("username required")
	}
	db.mu.Lock()
	defer db.mu.Unlock()
	for _, e := range db.users {
		if e.Username == u.Username {
			return errors.New("username already exists")
		}
	}
	if u.ID == "" {
		u.ID = util.NewID()
	}
	u.CreatedAt = time.Now()
	db.users = append(db.users, u)
	return db.save()
}

// CreateFirstAdmin atomically creates the initial admin, but only while no
// users exist. This closes the first-run race where two concurrent /api/setup
// requests could each pass a separate "user count == 0" check and both create
// an admin.
func (db *DB) CreateFirstAdmin(u *User) error {
	u.Username = strings.ToLower(strings.TrimSpace(u.Username))
	if u.Username == "" {
		return errors.New("username required")
	}
	db.mu.Lock()
	defer db.mu.Unlock()
	if len(db.users) != 0 {
		return errors.New("setup already completed")
	}
	if u.ID == "" {
		u.ID = util.NewID()
	}
	u.IsAdmin = true
	u.CreatedAt = time.Now()
	db.users = append(db.users, u)
	return db.save()
}

// UpdateUser applies fn to the user under the write lock and persists.
func (db *DB) UpdateUser(id string, fn func(*User) error) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	for _, u := range db.users {
		if u.ID == id {
			if err := fn(u); err != nil {
				return err
			}
			return db.save()
		}
	}
	return ErrNotFound
}

// UpdateUserGuarded applies fn under the write lock, then evaluates invariant
// against the full user set; if invariant fails the mutation is rolled back
// and nothing is persisted. This makes "don't leave the panel with no admin"
// atomic instead of a check-then-act race across two separate locks.
func (db *DB) UpdateUserGuarded(id string, fn func(*User) error, invariant func([]*User) error) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	for _, u := range db.users {
		if u.ID == id {
			backup := *u
			if err := fn(u); err != nil {
				return err
			}
			if invariant != nil {
				if err := invariant(db.users); err != nil {
					*u = backup
					return err
				}
			}
			return db.save()
		}
	}
	return ErrNotFound
}

func (db *DB) DeleteUser(id string) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	for i, u := range db.users {
		if u.ID == id {
			db.users = append(db.users[:i], db.users[i+1:]...)
			for k, s := range db.sessions {
				if s.UserID == id {
					delete(db.sessions, k)
				}
			}
			// API keys must not outlive their owner.
			kept := db.apiKeys[:0]
			for _, k := range db.apiKeys {
				if k.UserID != id {
					kept = append(kept, k)
				}
			}
			db.apiKeys = kept
			db.saveSessions()
			return db.save()
		}
	}
	return ErrNotFound
}

// AdminCount returns the number of enabled admin accounts.
func (db *DB) AdminCount() int {
	db.mu.RLock()
	defer db.mu.RUnlock()
	n := 0
	for _, u := range db.users {
		if u.IsAdmin && !u.Disabled {
			n++
		}
	}
	return n
}

// ---- Roles ----------------------------------------------------------------

func (db *DB) Roles() []*Role {
	db.mu.RLock()
	defer db.mu.RUnlock()
	out := make([]*Role, len(db.roles))
	for i, r := range db.roles {
		out[i] = r.Clone()
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// RoleByID returns a deep copy; see Users for why.
func (db *DB) RoleByID(id string) *Role {
	if id == "" {
		return nil
	}
	db.mu.RLock()
	defer db.mu.RUnlock()
	for _, r := range db.roles {
		if r.ID == id {
			return r.Clone()
		}
	}
	return nil
}

func (db *DB) CreateRole(r *Role) error {
	if strings.TrimSpace(r.Name) == "" {
		return errors.New("role name required")
	}
	db.mu.Lock()
	defer db.mu.Unlock()
	for _, e := range db.roles {
		if strings.EqualFold(e.Name, r.Name) {
			return errors.New("role name already exists")
		}
	}
	if r.ID == "" {
		r.ID = util.NewID()
	}
	db.roles = append(db.roles, r)
	return db.save()
}

func (db *DB) UpdateRole(id string, fn func(*Role) error) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	for _, r := range db.roles {
		if r.ID == id {
			if err := fn(r); err != nil {
				return err
			}
			return db.save()
		}
	}
	return ErrNotFound
}

func (db *DB) DeleteRole(id string) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	for i, r := range db.roles {
		if r.ID == id {
			db.roles = append(db.roles[:i], db.roles[i+1:]...)
			for _, u := range db.users {
				if u.RoleID == id {
					u.RoleID = ""
				}
			}
			return db.save()
		}
	}
	return ErrNotFound
}

// ---- AI settings ----------------------------------------------------------

func (db *DB) AISettings() AISettings {
	db.mu.RLock()
	defer db.mu.RUnlock()
	return db.ai
}

func (db *DB) SetAISettings(a AISettings) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	db.ai = a
	return db.save()
}

// ---- Update settings ------------------------------------------------------

func (db *DB) UpdateSettings() UpdateSettings {
	db.mu.RLock()
	defer db.mu.RUnlock()
	return db.update
}

func (db *DB) SetUpdateSettings(u UpdateSettings) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	db.update = u
	return db.save()
}

// ---- Sessions -------------------------------------------------------------

func HashToken(tok string) string {
	h := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(h[:])
}

func (db *DB) CreateSession(userID, ip string, ttl time.Duration) (token string, csrf string, err error) {
	token = util.NewToken()
	csrf = util.NewToken()
	s := &Session{
		TokenHash: HashToken(token),
		UserID:    userID,
		CSRF:      csrf,
		Expires:   time.Now().Add(ttl),
		Created:   time.Now(),
		IP:        ip,
	}
	db.mu.Lock()
	defer db.mu.Unlock()
	db.sessions[s.TokenHash] = s
	return token, csrf, db.saveSessions()
}

// SessionByToken returns the session and its user if the token is valid.
// Sliding expiry: extends the session when it is past half its TTL.
func (db *DB) SessionByToken(token string, ttl time.Duration) (*Session, *User) {
	h := HashToken(token)
	db.mu.Lock()
	defer db.mu.Unlock()
	s, ok := db.sessions[h]
	if !ok || time.Now().After(s.Expires) {
		return nil, nil
	}
	if time.Until(s.Expires) < ttl/2 {
		s.Expires = time.Now().Add(ttl)
		db.saveSessions()
	}
	for _, u := range db.users {
		if u.ID == s.UserID {
			if u.Disabled {
				return nil, nil
			}
			// Copies, not live pointers: this runs on every authenticated
			// request and the results are read (permission maps, CSRF token)
			// long after the lock is released.
			sess := *s
			return &sess, u.Clone()
		}
	}
	return nil, nil
}

func (db *DB) DeleteSession(token string) {
	db.mu.Lock()
	defer db.mu.Unlock()
	delete(db.sessions, HashToken(token))
	db.saveSessions()
}

func (db *DB) DeleteUserSessions(userID string) {
	db.mu.Lock()
	defer db.mu.Unlock()
	for k, s := range db.sessions {
		if s.UserID == userID {
			delete(db.sessions, k)
		}
	}
	db.saveSessions()
}

func (db *DB) PruneSessions() {
	db.mu.Lock()
	defer db.mu.Unlock()
	now := time.Now()
	changed := false
	for k, s := range db.sessions {
		if now.After(s.Expires) {
			delete(db.sessions, k)
			changed = true
		}
	}
	if changed {
		db.saveSessions()
	}
}

// ---- Audit ----------------------------------------------------------------

// maxAuditField bounds each free-text audit field. Several audited values
// come straight from a request (file paths, console commands, AI prompts);
// without a cap an attacker could write megabyte-sized entries to force log
// rotation and destroy the record of their earlier activity.
const maxAuditField = 512

func clampField(s string) string {
	if len(s) <= maxAuditField {
		return s
	}
	return s[:maxAuditField] + "…[truncated]"
}

func (db *DB) Audit(e AuditEntry) {
	e.Time = time.Now()
	if e.User == "" {
		e.User = "-"
	}
	e.Target = clampField(e.Target)
	e.Detail = clampField(e.Detail)
	line, err := json.Marshal(e)
	if err != nil {
		return
	}
	db.auditMu.Lock()
	defer db.auditMu.Unlock()
	db.auditFile.Write(append(line, '\n'))
	db.rotateAuditLocked()
}

// rotateAuditLocked rotates audit.jsonl once it exceeds 10 MB, keeping one
// previous generation.
func (db *DB) rotateAuditLocked() {
	st, err := db.auditFile.Stat()
	if err != nil || st.Size() < 10*1024*1024 {
		return
	}
	path := filepath.Join(db.dataDir, "logs", "audit.jsonl")
	db.auditFile.Close()
	os.Rename(path, path+".1")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err == nil {
		db.auditFile = f
	}
}

// AuditEntries returns up to limit newest entries, filtered by user/action
// substring when non-empty.
func (db *DB) AuditEntries(limit int, user, action string) []AuditEntry {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	// Stream the files and keep only a bounded window of matches. Reading
	// both generations fully into memory (up to 20 MB, then a struct per
	// line) let any audit.view user OOM the panel with concurrent requests.
	out := make([]AuditEntry, 0, limit)
	for _, name := range []string{"audit.jsonl.1", "audit.jsonl"} {
		f, err := os.Open(filepath.Join(db.dataDir, "logs", name))
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			line := sc.Bytes()
			if len(line) == 0 {
				continue
			}
			var e AuditEntry
			if json.Unmarshal(line, &e) != nil {
				continue
			}
			if user != "" && !strings.Contains(e.User, user) {
				continue
			}
			if action != "" && !strings.Contains(e.Action, action) {
				continue
			}
			// Ring behaviour: retain only the newest `limit` matches.
			if len(out) == limit {
				copy(out, out[1:])
				out = out[:limit-1]
			}
			out = append(out, e)
		}
		f.Close()
	}
	// newest first
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// ---- Server configs (per-directory JSON) ----------------------------------

func (db *DB) serverConfigPath(id string) string {
	return filepath.Join(db.ServersDir(), id, "server.json")
}

func (db *DB) LoadServers() ([]*Server, error) {
	entries, err := os.ReadDir(db.ServersDir())
	if err != nil {
		return nil, err
	}
	var out []*Server
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		var s Server
		if err := util.ReadJSON(db.serverConfigPath(e.Name()), &s); err != nil {
			continue
		}
		out = append(out, &s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (db *DB) SaveServer(s *Server) error {
	dir := filepath.Join(db.ServersDir(), s.ID)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	return util.WriteJSONAtomic(db.serverConfigPath(s.ID), s)
}

func (db *DB) DeleteServerConfig(id string, removeData bool) error {
	dir := filepath.Join(db.ServersDir(), id)
	if removeData {
		return os.RemoveAll(dir)
	}
	return os.Remove(db.serverConfigPath(id))
}
