package mc

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"blockpanel/internal/store"
)

type State string

const (
	StateStopped  State = "stopped"
	StateStarting State = "starting"
	StateRunning  State = "running"
	StateStopping State = "stopping"
)

// EventFunc receives lifecycle events: "start", "stop", "crash".
type EventFunc func(event string, srv *store.Server, detail string)

type Stats struct {
	CPUPercent float64 `json:"cpu_percent"`
	RSSMB      float64 `json:"rss_mb"`
}

// Instance is one managed Minecraft server process.
type Instance struct {
	mu sync.Mutex
	// saveMu serializes full config update transactions (mutate + persist)
	// so the file on disk always reflects the last accepted change.
	saveMu  sync.Mutex
	cfg     *store.Server
	state   State
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	console *Console
	exited  chan struct{} // closed when the current process exits

	startedAt     time.Time
	stopRequested bool
	crashTimes    []time.Time
	stats         Stats
	lastPing      PingResult

	history *History
	players *PlayerTracker

	onEvent EventFunc
}

func newInstance(cfg *store.Server, onEvent EventFunc) *Instance {
	return &Instance{
		cfg:     cfg,
		state:   StateStopped,
		console: NewConsole(),
		history: NewHistory(),
		players: NewPlayerTracker(),
		onEvent: onEvent,
	}
}

func (in *Instance) Console() *Console        { return in.console }
func (in *Instance) History() *History        { return in.history }
func (in *Instance) Players() *PlayerTracker  { return in.players }

// LastPing returns the most recent server-list-ping result.
func (in *Instance) LastPing() PingResult {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.lastPing
}

// Config returns a deep copy of the server configuration. It must be a deep
// copy: Server holds a []*Webhook, so a shallow copy would hand callers
// pointers into state other goroutines are mutating.
func (in *Instance) Config() *store.Server {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.cfg.Clone()
}

func (in *Instance) SetConfig(cfg *store.Server) {
	in.mu.Lock()
	in.cfg = cfg.Clone()
	in.mu.Unlock()
}

// mutateConfig applies fn to the live configuration while holding the
// instance lock, so read-modify-write sequences (adding a webhook, editing
// the download policy) are atomic instead of last-writer-wins. It returns a
// copy of the result for persisting. Callers must not call back into the
// instance from fn.
func (in *Instance) mutateConfig(fn func(*store.Server) error) (*store.Server, error) {
	in.mu.Lock()
	defer in.mu.Unlock()
	working := in.cfg.Clone()
	if err := fn(working); err != nil {
		return nil, err
	}
	in.cfg = working
	return working.Clone(), nil
}

func (in *Instance) State() State {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.state
}

func (in *Instance) Uptime() time.Duration {
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.state == StateStopped {
		return 0
	}
	return time.Since(in.startedAt)
}

func (in *Instance) Stats() Stats {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.stats
}

func (in *Instance) pid() int {
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.cmd != nil && in.cmd.Process != nil {
		return in.cmd.Process.Pid
	}
	return 0
}

// buildCmd assembles the launch command. Fields are split on whitespace
// (quoted arguments are not supported in JVMArgs/ServerArgs; use
// LaunchOverride for exotic setups).
func buildCmd(cfg *store.Server) (*exec.Cmd, error) {
	if cfg.LaunchOverride != "" {
		cmd := exec.Command("/bin/sh", "-c", cfg.LaunchOverride)
		cmd.Dir = cfg.Root
		return cmd, nil
	}
	if cfg.Jar == "" {
		return nil, errors.New("no server jar configured")
	}
	java := cfg.JavaPath
	if java == "" {
		java = "java"
	}
	args := []string{}
	if cfg.MinMemMB > 0 {
		args = append(args, fmt.Sprintf("-Xms%dM", cfg.MinMemMB))
	}
	if cfg.MaxMemMB > 0 {
		args = append(args, fmt.Sprintf("-Xmx%dM", cfg.MaxMemMB))
	}
	args = append(args, strings.Fields(cfg.JVMArgs)...)
	args = append(args, "-jar", cfg.Jar)
	sa := cfg.ServerArgs
	if sa == "" {
		sa = "nogui"
	}
	args = append(args, strings.Fields(sa)...)
	cmd := exec.Command(java, args...)
	cmd.Dir = cfg.Root
	return cmd, nil
}

func (in *Instance) Start() error {
	in.mu.Lock()
	if in.state != StateStopped {
		in.mu.Unlock()
		return fmt.Errorf("server is %s", in.state)
	}
	cfg := in.cfg

	if cfg.AcceptEula {
		writeEula(cfg.Root)
	}

	cmd, err := buildCmd(cfg)
	if err != nil {
		in.mu.Unlock()
		return err
	}
	// New process group so stop/kill reaches java's children too.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		in.mu.Unlock()
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		in.mu.Unlock()
		return err
	}
	cmd.Stderr = cmd.Stdout // merge

	if err := cmd.Start(); err != nil {
		in.mu.Unlock()
		in.console.Append("[panel] failed to start: " + err.Error())
		return fmt.Errorf("start failed: %w", err)
	}

	in.cmd = cmd
	in.stdin = stdin
	in.state = StateStarting
	in.startedAt = time.Now()
	in.stopRequested = false
	exited := make(chan struct{})
	in.exited = exited
	in.mu.Unlock()

	in.console.Append(fmt.Sprintf("[panel] starting (pid %d)", cmd.Process.Pid))
	if in.onEvent != nil {
		go in.onEvent("start", cfg, "")
	}

	go in.readOutput(stdout)
	go in.statsLoop(exited)
	go in.waitExit(cmd, exited)
	return nil
}

func (in *Instance) readOutput(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		in.console.Append(line)
		// Vanilla/Paper/Forge all log `Done (X.XXXs)!` when ready.
		if strings.Contains(line, "Done (") {
			in.mu.Lock()
			if in.state == StateStarting {
				in.state = StateRunning
			}
			in.mu.Unlock()
		}
		in.trackPlayers(line)
	}
}

func (in *Instance) waitExit(cmd *exec.Cmd, exited chan struct{}) {
	err := cmd.Wait()
	close(exited)

	in.mu.Lock()
	requested := in.stopRequested
	cfg := in.cfg
	in.state = StateStopped
	in.cmd = nil
	in.stdin = nil
	in.stats = Stats{}
	in.mu.Unlock()

	detail := "exit code 0"
	if err != nil {
		detail = err.Error()
	}
	in.console.Append("[panel] process exited: " + detail)

	if requested {
		if in.onEvent != nil {
			go in.onEvent("stop", cfg, detail)
		}
		return
	}

	// Unexpected exit = crash.
	if in.onEvent != nil {
		go in.onEvent("crash", cfg, detail)
	}
	if !cfg.AutoRestart {
		return
	}
	in.mu.Lock()
	now := time.Now()
	keep := in.crashTimes[:0]
	for _, t := range in.crashTimes {
		if now.Sub(t) < 10*time.Minute {
			keep = append(keep, t)
		}
	}
	in.crashTimes = append(keep, now)
	tooMany := len(in.crashTimes) > 3
	in.mu.Unlock()

	if tooMany {
		in.console.Append("[panel] crashed 4x in 10 minutes — auto-restart disabled until manual start")
		return
	}
	in.console.Append("[panel] auto-restart in 5s")
	time.Sleep(5 * time.Second)
	if err := in.Start(); err != nil {
		in.console.Append("[panel] auto-restart failed: " + err.Error())
	}
}

// Stop requests a graceful shutdown: stop command over stdin, SIGTERM after
// the grace period, SIGKILL 10s later. Blocks until the process exits or the
// escalation completes.
func (in *Instance) Stop() error {
	in.mu.Lock()
	if in.state == StateStopped {
		in.mu.Unlock()
		return errors.New("server is not running")
	}
	if in.state == StateStopping {
		exited := in.exited
		in.mu.Unlock()
		<-exited
		return nil
	}
	in.state = StateStopping
	in.stopRequested = true
	stdin := in.stdin
	exited := in.exited
	cfg := in.cfg
	pid := 0
	if in.cmd != nil && in.cmd.Process != nil {
		pid = in.cmd.Process.Pid
	}
	in.mu.Unlock()

	stopCmd := cfg.StopCommand
	if stopCmd == "" {
		stopCmd = "stop"
	}
	grace := time.Duration(cfg.StopGraceSecs) * time.Second
	if grace <= 0 {
		grace = 30 * time.Second
	}

	in.console.Append("[panel] stopping (" + stopCmd + ")")
	if stdin != nil {
		fmt.Fprintf(stdin, "%s\n", stopCmd)
	}

	select {
	case <-exited:
		return nil
	case <-time.After(grace):
	}
	in.console.Append("[panel] grace period elapsed, sending SIGTERM")
	if pid > 0 {
		syscall.Kill(-pid, syscall.SIGTERM)
	}
	select {
	case <-exited:
		return nil
	case <-time.After(10 * time.Second):
	}
	in.console.Append("[panel] sending SIGKILL")
	if pid > 0 {
		syscall.Kill(-pid, syscall.SIGKILL)
	}
	<-exited
	return nil
}

// Kill force-terminates the process group immediately.
func (in *Instance) Kill() error {
	in.mu.Lock()
	if in.state == StateStopped {
		in.mu.Unlock()
		return errors.New("server is not running")
	}
	in.stopRequested = true
	pid := 0
	if in.cmd != nil && in.cmd.Process != nil {
		pid = in.cmd.Process.Pid
	}
	exited := in.exited
	in.mu.Unlock()

	in.console.Append("[panel] kill requested")
	if pid > 0 {
		syscall.Kill(-pid, syscall.SIGKILL)
	}
	<-exited
	return nil
}

// SendCommand writes a command to the server's stdin and echoes it to the
// console buffer.
func (in *Instance) SendCommand(command string) error {
	command = strings.TrimSpace(command)
	if command == "" {
		return errors.New("empty command")
	}
	if strings.ContainsAny(command, "\n\r") {
		return errors.New("command must be a single line")
	}
	in.mu.Lock()
	stdin := in.stdin
	st := in.state
	in.mu.Unlock()
	if st != StateRunning && st != StateStarting {
		return errors.New("server is not running")
	}
	if stdin == nil {
		return errors.New("stdin unavailable")
	}
	in.console.Append("> " + command)
	_, err := fmt.Fprintf(stdin, "%s\n", command)
	return err
}

func (in *Instance) statsLoop(exited chan struct{}) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	pingEvery := 0
	for {
		select {
		case <-exited:
			in.players.Clear()
			return
		case <-t.C:
			pid := in.pid()
			if pid == 0 {
				continue
			}
			var st Stats
			if s, err := procStats(pid); err == nil {
				st = s
				in.mu.Lock()
				in.stats = s
				in.mu.Unlock()
			}
			// Ping every 15s (every third tick) for player counts and MOTD.
			pingEvery++
			if pingEvery%3 == 0 && in.State() == StateRunning {
				cfg := in.Config()
				res := Ping("127.0.0.1", ReadServerPort(cfg.Root), 3*time.Second)
				in.mu.Lock()
				in.lastPing = res
				in.mu.Unlock()
			}
			in.history.Add(Sample{
				T: time.Now().Unix(), CPU: st.CPUPercent,
				RSSMB: st.RSSMB, Players: in.players.Count(),
			})
		}
	}
}

func writeEula(root string) {
	path := filepath.Join(root, "eula.txt")
	data, err := os.ReadFile(path)
	if err == nil && strings.Contains(strings.ToLower(string(data)), "eula=true") {
		return
	}
	os.WriteFile(path, []byte("# accepted via panel config\neula=true\n"), 0o644)
}
