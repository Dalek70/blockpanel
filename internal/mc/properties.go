package mc

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// server.properties handling. Edits are applied key-by-key so comments,
// ordering and unknown keys written by mods survive a round-trip.

type Property struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

const propertiesFile = "server.properties"

// ReadProperties returns the parsed key/values in file order.
func ReadProperties(root string) ([]Property, error) {
	f, err := os.Open(filepath.Join(root, propertiesFile))
	if err != nil {
		if os.IsNotExist(err) {
			return []Property{}, nil
		}
		return nil, err
	}
	defer f.Close()

	var out []Property
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "!") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		out = append(out, Property{Key: strings.TrimSpace(k), Value: v})
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if out == nil {
		out = []Property{}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

// WriteProperties applies updates to the existing file, preserving comments,
// blank lines, ordering and any keys not being changed. New keys are appended.
func WriteProperties(root string, updates map[string]string) error {
	path := filepath.Join(root, propertiesFile)

	var lines []string
	if data, err := os.ReadFile(path); err == nil {
		lines = strings.Split(string(data), "\n")
	} else if !os.IsNotExist(err) {
		return err
	}

	// Reject values that would corrupt the file format.
	for k, v := range updates {
		if strings.ContainsAny(k, "=\n\r") || k == "" {
			return fmt.Errorf("invalid property name %q", k)
		}
		if strings.ContainsAny(v, "\n\r") {
			return fmt.Errorf("property %q cannot contain newlines", k)
		}
	}

	seen := map[string]bool{}
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "!") {
			continue
		}
		k, _, ok := strings.Cut(trimmed, "=")
		if !ok {
			continue
		}
		key := strings.TrimSpace(k)
		if v, found := updates[key]; found {
			lines[i] = key + "=" + v
			seen[key] = true
		}
	}

	// Append keys that were not already present, in stable order.
	var added []string
	for k := range updates {
		if !seen[k] {
			added = append(added, k)
		}
	}
	sort.Strings(added)
	for _, k := range added {
		lines = append(lines, k+"="+updates[k])
	}

	out := strings.Join(lines, "\n")
	if !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	return os.WriteFile(path, []byte(out), 0o644)
}
