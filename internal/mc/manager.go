package mc

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"blockpanel/internal/store"
)

// Manager owns all Instances, keyed by server ID.
type Manager struct {
	mu        sync.RWMutex
	db        *store.DB
	instances map[string]*Instance
	onEvent   EventFunc
}

func NewManager(db *store.DB, onEvent EventFunc) (*Manager, error) {
	m := &Manager{db: db, instances: map[string]*Instance{}, onEvent: onEvent}
	servers, err := db.LoadServers()
	if err != nil {
		return nil, err
	}
	for _, s := range servers {
		m.instances[s.ID] = newInstance(s, onEvent)
	}
	return m, nil
}

// AutoStart launches every server flagged auto_start.
func (m *Manager) AutoStart() {
	for _, in := range m.All() {
		if in.Config().AutoStart {
			if err := in.Start(); err != nil {
				in.Console().Append("[panel] auto-start failed: " + err.Error())
			}
		}
	}
}

func (m *Manager) All() []*Instance {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*Instance, 0, len(m.instances))
	for _, in := range m.instances {
		out = append(out, in)
	}
	return out
}

func (m *Manager) Get(id string) *Instance {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.instances[id]
}

// Create registers a new managed server and creates its directory tree.
func (m *Manager) Create(cfg *store.Server) (*Instance, error) {
	if cfg.Root == "" {
		cfg.Root = filepath.Join(m.db.ServersDir(), cfg.ID, "data")
	}
	if err := os.MkdirAll(cfg.Root, 0o750); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(m.BackupsDir(cfg.ID), 0o750); err != nil {
		return nil, err
	}
	if err := m.db.SaveServer(cfg); err != nil {
		return nil, err
	}
	in := newInstance(cfg, m.onEvent)
	m.mu.Lock()
	m.instances[cfg.ID] = in
	m.mu.Unlock()
	return in, nil
}

// Import registers an existing directory as a server. The directory is never
// deleted by the panel.
func (m *Manager) Import(cfg *store.Server, root string) (*Instance, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("import path: %w", err)
	}
	if !st.IsDir() {
		return nil, errors.New("import path is not a directory")
	}
	cfg.Root = abs
	cfg.Imported = true
	if err := os.MkdirAll(m.BackupsDir(cfg.ID), 0o750); err != nil {
		return nil, err
	}
	if err := m.db.SaveServer(cfg); err != nil {
		return nil, err
	}
	in := newInstance(cfg, m.onEvent)
	m.mu.Lock()
	m.instances[cfg.ID] = in
	m.mu.Unlock()
	return in, nil
}

// Delete stops the server if needed and removes it. Managed server data is
// deleted from disk; imported roots are left untouched.
func (m *Manager) Delete(id string) error {
	in := m.Get(id)
	if in == nil {
		return store.ErrNotFound
	}
	if in.State() != StateStopped {
		if err := in.Kill(); err != nil {
			return err
		}
	}
	cfg := in.Config()
	m.mu.Lock()
	delete(m.instances, id)
	m.mu.Unlock()
	// Serialize against any in-flight config write for this server so the
	// removal below cannot be undone by a save that is mid-flight.
	in.saveMu.Lock()
	in.saveMu.Unlock() //nolint:staticcheck // barrier, not a guarded section
	// The <data>/servers/<id> dir holds server.json + backups (+ data for
	// managed servers); removing it never touches an imported root.
	_ = cfg
	return m.db.DeleteServerConfig(id, true)
}

func (m *Manager) BackupsDir(id string) string {
	return filepath.Join(m.db.ServersDir(), id, "backups")
}

// UpdateConfig persists cfg and pushes it into the live instance.
//
// Prefer MutateConfig for anything that reads the current config first: this
// entry point is last-writer-wins and will silently drop a concurrent change.
func (m *Manager) UpdateConfig(cfg *store.Server) error {
	if err := m.db.SaveServer(cfg); err != nil {
		return err
	}
	if in := m.Get(cfg.ID); in != nil {
		in.SetConfig(cfg)
	}
	return nil
}

// MutateConfig atomically applies fn to a server's configuration and persists
// the result. The read-modify-write happens under the instance lock, so
// concurrent edits (two webhook additions, a policy change racing a memory
// change) compose instead of overwriting each other.
func (m *Manager) MutateConfig(id string, fn func(*store.Server) error) (*store.Server, error) {
	in := m.Get(id)
	if in == nil {
		return nil, store.ErrNotFound
	}
	// Serialize whole update transactions for this server so the persisted
	// file can never lag behind the in-memory config.
	in.saveMu.Lock()
	defer in.saveMu.Unlock()

	updated, err := in.mutateConfig(fn)
	if err != nil {
		return nil, err
	}
	// Re-check registration before writing: a concurrent Delete may have
	// removed this server (and its directory) after we resolved the
	// instance. SaveServer would otherwise recreate server.json and the
	// "deleted" server would come back on the next panel restart.
	if m.Get(id) == nil {
		return nil, store.ErrNotFound
	}
	if err := m.db.SaveServer(updated); err != nil {
		return nil, err
	}
	return updated, nil
}

// StopAll gracefully stops every running server (used on panel shutdown).
func (m *Manager) StopAll() {
	var wg sync.WaitGroup
	for _, in := range m.All() {
		if in.State() == StateStopped {
			continue
		}
		wg.Add(1)
		go func(in *Instance) {
			defer wg.Done()
			in.Stop()
		}(in)
	}
	wg.Wait()
}
