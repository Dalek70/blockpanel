package mc

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ---- Live player tracking from console output ------------------------------

var (
	joinRe  = regexp.MustCompile(`\]: ([A-Za-z0-9_]{1,16})(?:\[[^\]]*\])? joined the game`)
	leaveRe = regexp.MustCompile(`\]: ([A-Za-z0-9_]{1,16}) left the game`)
)

// trackPlayers updates the online-player list from a console line. Vanilla,
// Paper and Fabric all use the "<name> joined/left the game" wording.
func (in *Instance) trackPlayers(line string) {
	if m := joinRe.FindStringSubmatch(line); m != nil {
		in.players.Join(m[1])
		if in.onEvent != nil {
			go in.onEvent("player_join", in.Config(), m[1]+" joined")
		}
		return
	}
	if m := leaveRe.FindStringSubmatch(line); m != nil {
		in.players.Leave(m[1])
		if in.onEvent != nil {
			go in.onEvent("player_leave", in.Config(), m[1]+" left")
		}
	}
}

// ---- Player list files -----------------------------------------------------

// PlayerEntry is one row of whitelist.json / ops.json / banned-players.json.
// The three files share a name+uuid shape and differ in extra fields, so a
// single permissive struct covers all of them.
type PlayerEntry struct {
	UUID   string `json:"uuid,omitempty"`
	Name   string `json:"name"`
	Level  int    `json:"level,omitempty"`
	Source string `json:"source,omitempty"`
	Reason string `json:"reason,omitempty"`
	Expires string `json:"expires,omitempty"`
	Created string `json:"created,omitempty"`
	BypassesPlayerLimit bool `json:"bypassesPlayerLimit,omitempty"`
}

// PlayerListKind identifies which server file to operate on.
type PlayerListKind string

const (
	ListWhitelist PlayerListKind = "whitelist"
	ListOps       PlayerListKind = "ops"
	ListBans      PlayerListKind = "bans"
	ListBannedIPs PlayerListKind = "banned-ips"
)

func playerListFile(kind PlayerListKind) (string, error) {
	switch kind {
	case ListWhitelist:
		return "whitelist.json", nil
	case ListOps:
		return "ops.json", nil
	case ListBans:
		return "banned-players.json", nil
	case ListBannedIPs:
		return "banned-ips.json", nil
	}
	return "", errors.New("unknown player list")
}

// ReadPlayerList parses one of the server's player files. A missing file is
// an empty list, which is what a fresh server looks like.
func ReadPlayerList(root string, kind PlayerListKind) ([]PlayerEntry, error) {
	name, err := playerListFile(kind)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(root, name))
	if err != nil {
		if os.IsNotExist(err) {
			return []PlayerEntry{}, nil
		}
		return nil, err
	}
	var out []PlayerEntry
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	if out == nil {
		out = []PlayerEntry{}
	}
	return out, nil
}

// RemoveFromPlayerList deletes an entry by name, for use when the server is
// stopped (while running, the console command is preferred so the server
// reloads its own state).
func RemoveFromPlayerList(root string, kind PlayerListKind, name string) error {
	file, err := playerListFile(kind)
	if err != nil {
		return err
	}
	entries, err := ReadPlayerList(root, kind)
	if err != nil {
		return err
	}
	out := entries[:0]
	for _, e := range entries {
		if !strings.EqualFold(e.Name, name) {
			out = append(out, e)
		}
	}
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(root, file), data, 0o644)
}

// ValidPlayerName reports whether s is a plausible Minecraft username. Used
// to keep console commands built from user input to a single safe token.
func ValidPlayerName(s string) bool {
	if len(s) < 1 || len(s) > 16 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_') {
			return false
		}
	}
	return true
}
