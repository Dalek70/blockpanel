package mc

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"blockpanel/internal/store"
)

// Scheduler runs each server's recurring tasks (restart, backup, command…).
// It ticks once a minute and fires anything due; a task that is still running
// is never started twice because execution is serialized per server.

type Scheduler struct {
	mgr     *Manager
	onEvent EventFunc
	stop    chan struct{}
	running map[string]bool // scheduleID -> in flight
	mu      chanMutex
}

// chanMutex is a tiny mutex built on a buffered channel so the zero value is
// usable without an explicit constructor.
type chanMutex struct{ ch chan struct{} }

func (m *chanMutex) Lock() {
	if m.ch == nil {
		m.ch = make(chan struct{}, 1)
	}
	m.ch <- struct{}{}
}
func (m *chanMutex) Unlock() { <-m.ch }

func NewScheduler(mgr *Manager, onEvent EventFunc) *Scheduler {
	return &Scheduler{
		mgr:     mgr,
		onEvent: onEvent,
		stop:    make(chan struct{}),
		running: map[string]bool{},
	}
}

func (s *Scheduler) Start() {
	go func() {
		// Align to the start of the next minute, then tick every minute.
		t := time.NewTimer(time.Until(time.Now().Truncate(time.Minute).Add(time.Minute)))
		defer t.Stop()
		for {
			select {
			case <-s.stop:
				return
			case <-t.C:
				s.runDue(time.Now())
				t.Reset(time.Until(time.Now().Truncate(time.Minute).Add(time.Minute)))
			}
		}
	}()
}

func (s *Scheduler) Stop() { close(s.stop) }

// ValidateSchedule checks a schedule definition and returns a normalized copy.
func ValidateSchedule(sc *store.Schedule) error {
	switch sc.Action {
	case store.ActionStart, store.ActionStop, store.ActionRestart, store.ActionBackup:
	case store.ActionCommand:
		cmd := strings.TrimSpace(sc.Command)
		if cmd == "" {
			return errors.New("a command is required for the command action")
		}
		if strings.ContainsAny(cmd, "\n\r") {
			return errors.New("command must be a single line")
		}
	default:
		return errors.New("unknown action")
	}
	switch sc.Mode {
	case "interval":
		if sc.IntervalMin < 1 || sc.IntervalMin > 60*24*30 {
			return errors.New("interval must be between 1 minute and 30 days")
		}
	case "daily":
		if _, _, err := parseHHMM(sc.TimeOfDay); err != nil {
			return err
		}
		for _, d := range sc.Weekdays {
			if d < 0 || d > 6 {
				return errors.New("weekdays must be 0 (Sunday) to 6 (Saturday)")
			}
		}
	default:
		return errors.New("mode must be interval or daily")
	}
	if strings.TrimSpace(sc.Name) == "" {
		sc.Name = string(sc.Action)
	}
	return nil
}

func parseHHMM(s string) (int, int, error) {
	h, m, ok := strings.Cut(strings.TrimSpace(s), ":")
	if !ok {
		return 0, 0, errors.New("time must be HH:MM")
	}
	hh, err1 := strconv.Atoi(h)
	mm, err2 := strconv.Atoi(m)
	if err1 != nil || err2 != nil || hh < 0 || hh > 23 || mm < 0 || mm > 59 {
		return 0, 0, errors.New("time must be HH:MM between 00:00 and 23:59")
	}
	return hh, mm, nil
}

// NextRun computes when a schedule should next fire, relative to now.
func NextRun(sc *store.Schedule, now time.Time) time.Time {
	if !sc.Enabled {
		return time.Time{}
	}
	switch sc.Mode {
	case "interval":
		base := sc.LastRun
		if base.IsZero() {
			base = now
		}
		next := base.Add(time.Duration(sc.IntervalMin) * time.Minute)
		if next.Before(now) {
			next = now.Add(time.Duration(sc.IntervalMin) * time.Minute)
		}
		return next
	case "daily":
		hh, mm, err := parseHHMM(sc.TimeOfDay)
		if err != nil {
			return time.Time{}
		}
		candidate := time.Date(now.Year(), now.Month(), now.Day(), hh, mm, 0, 0, now.Location())
		for i := 0; i < 8; i++ {
			if candidate.After(now) && dayAllowed(sc.Weekdays, candidate.Weekday()) {
				return candidate
			}
			candidate = candidate.AddDate(0, 0, 1)
		}
	}
	return time.Time{}
}

func dayAllowed(days []int, wd time.Weekday) bool {
	if len(days) == 0 {
		return true
	}
	for _, d := range days {
		if time.Weekday(d) == wd {
			return true
		}
	}
	return false
}

// due reports whether a schedule should fire at t.
func due(sc *store.Schedule, t time.Time) bool {
	if !sc.Enabled {
		return false
	}
	switch sc.Mode {
	case "interval":
		if sc.LastRun.IsZero() {
			return true
		}
		return t.Sub(sc.LastRun) >= time.Duration(sc.IntervalMin)*time.Minute
	case "daily":
		hh, mm, err := parseHHMM(sc.TimeOfDay)
		if err != nil {
			return false
		}
		if t.Hour() != hh || t.Minute() != mm {
			return false
		}
		if !dayAllowed(sc.Weekdays, t.Weekday()) {
			return false
		}
		// Guard against double-firing within the same minute.
		return sc.LastRun.IsZero() || t.Sub(sc.LastRun) > 90*time.Second
	}
	return false
}

func (s *Scheduler) runDue(now time.Time) {
	for _, in := range s.mgr.All() {
		cfg := in.Config()
		for _, sc := range cfg.Schedules {
			if !due(sc, now) {
				continue
			}
			s.mu.Lock()
			if s.running[sc.ID] {
				s.mu.Unlock()
				continue
			}
			s.running[sc.ID] = true
			s.mu.Unlock()

			go func(serverID string, sched store.Schedule) {
				note, err := s.execute(serverID, sched)
				s.mu.Lock()
				delete(s.running, sched.ID)
				s.mu.Unlock()

				ok := err == nil
				if err != nil {
					note = err.Error()
				}
				// Record the outcome on the live config.
				s.mgr.MutateConfig(serverID, func(c *store.Server) error {
					for _, cur := range c.Schedules {
						if cur.ID == sched.ID {
							cur.LastRun = time.Now()
							cur.LastOK = ok
							cur.LastNote = note
							cur.NextRun = NextRun(cur, time.Now())
						}
					}
					return nil
				})
			}(cfg.ID, *sc)
		}
	}
}

// RunNow fires a schedule immediately (the "run now" button), recording the
// outcome exactly as a timed run would.
func (s *Scheduler) RunNow(serverID string, sc store.Schedule) {
	s.mu.Lock()
	if s.running[sc.ID] {
		s.mu.Unlock()
		return
	}
	s.running[sc.ID] = true
	s.mu.Unlock()

	note, err := s.execute(serverID, sc)
	s.mu.Lock()
	delete(s.running, sc.ID)
	s.mu.Unlock()

	ok := err == nil
	if err != nil {
		note = err.Error()
	}
	s.mgr.MutateConfig(serverID, func(c *store.Server) error {
		for _, cur := range c.Schedules {
			if cur.ID == sc.ID {
				cur.LastRun = time.Now()
				cur.LastOK = ok
				cur.LastNote = note
				cur.NextRun = NextRun(cur, time.Now())
			}
		}
		return nil
	})
}

func (s *Scheduler) execute(serverID string, sc store.Schedule) (string, error) {
	in := s.mgr.Get(serverID)
	if in == nil {
		return "", errors.New("server no longer exists")
	}
	in.Console().Append(fmt.Sprintf("[panel] running scheduled task %q (%s)", sc.Name, sc.Action))

	switch sc.Action {
	case store.ActionStart:
		if in.State() != StateStopped {
			return "already running", nil
		}
		return "started", in.Start()
	case store.ActionStop:
		if in.State() == StateStopped {
			return "already stopped", nil
		}
		return "stopped", in.Stop()
	case store.ActionRestart:
		if in.State() != StateStopped {
			if err := in.Stop(); err != nil {
				return "", err
			}
		}
		return "restarted", in.Start()
	case store.ActionBackup:
		cfg := in.Config()
		name, err := CreateBackup(cfg.Root, s.mgr.BackupsDir(serverID))
		if err != nil {
			return "", err
		}
		if pruned, err := PruneBackups(s.mgr.BackupsDir(serverID), cfg.BackupKeep); err == nil && pruned > 0 {
			in.Console().Append(fmt.Sprintf("[panel] retention: removed %d old backup(s)", pruned))
		}
		if s.onEvent != nil {
			go s.onEvent("backup", cfg, "Scheduled backup "+name)
		}
		return "backup " + name, nil
	case store.ActionCommand:
		if in.State() != StateRunning {
			return "", errors.New("server is not running")
		}
		return "sent: " + sc.Command, in.SendCommand(sc.Command)
	}
	return "", errors.New("unknown action")
}
