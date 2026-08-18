package mc

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// procStats samples CPU%/RSS for a process group leader via ps, which works
// identically on macOS and Linux without cgo or /proc parsing.
func procStats(pid int) (Stats, error) {
	out, err := exec.Command("ps", "-o", "rss=,pcpu=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return Stats{}, err
	}
	fields := strings.Fields(string(out))
	if len(fields) < 2 {
		return Stats{}, fmt.Errorf("unexpected ps output: %q", out)
	}
	rssKB, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return Stats{}, err
	}
	cpu, err := strconv.ParseFloat(fields[1], 64)
	if err != nil {
		return Stats{}, err
	}
	return Stats{CPUPercent: cpu, RSSMB: rssKB / 1024}, nil
}

// ReadServerPort parses server-port from server.properties, returning 25565
// (the default) when absent.
func ReadServerPort(root string) int {
	f, err := os.Open(filepath.Join(root, "server.properties"))
	if err != nil {
		return 25565
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if v, ok := strings.CutPrefix(line, "server-port="); ok {
			if p, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && p > 0 {
				return p
			}
		}
	}
	return 25565
}

// DirSize walks a directory tree summing file sizes, capped at 200k entries
// to bound the cost on pathological trees.
func DirSize(root string) int64 {
	var total int64
	count := 0
	filepath.WalkDir(root, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		count++
		if count > 200_000 {
			return filepath.SkipAll
		}
		if d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				total += info.Size()
			}
		}
		return nil
	})
	return total
}
