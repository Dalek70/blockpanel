package mc

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Java runtime discovery, so the UI can offer installed JVMs instead of
// making the operator type a path.

type JavaRuntime struct {
	Path    string `json:"path"`
	Version string `json:"version"`
	Vendor  string `json:"vendor,omitempty"`
	Default bool   `json:"default"`
}

var javaVersionRe = regexp.MustCompile(`version "([^"]+)"`)

// candidateJavaPaths lists likely java executables on macOS and Linux.
func candidateJavaPaths() []string {
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		if p == "" || seen[p] {
			return
		}
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			seen[p] = true
			out = append(out, p)
		}
	}

	if p, err := exec.LookPath("java"); err == nil {
		if resolved, err := filepath.EvalSymlinks(p); err == nil {
			add(resolved)
		} else {
			add(p)
		}
	}
	// macOS: /Library/Java/JavaVirtualMachines/*/Contents/Home/bin/java
	// and the per-user equivalent.
	roots := []string{
		"/Library/Java/JavaVirtualMachines",
		filepath.Join(os.Getenv("HOME"), "Library/Java/JavaVirtualMachines"),
	}
	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, e := range entries {
			add(filepath.Join(root, e.Name(), "Contents/Home/bin/java"))
		}
	}
	// Linux: /usr/lib/jvm/*/bin/java
	if entries, err := os.ReadDir("/usr/lib/jvm"); err == nil {
		for _, e := range entries {
			add(filepath.Join("/usr/lib/jvm", e.Name(), "bin/java"))
		}
	}
	for _, p := range []string{"/usr/bin/java", "/usr/local/bin/java", "/opt/java/bin/java"} {
		add(p)
	}
	return out
}

// DetectJava probes each candidate for its version string. Probing runs
// `java -version`, which is cheap and side-effect free.
func DetectJava() []JavaRuntime {
	defaultPath := ""
	if p, err := exec.LookPath("java"); err == nil {
		if resolved, err := filepath.EvalSymlinks(p); err == nil {
			defaultPath = resolved
		} else {
			defaultPath = p
		}
	}

	var out []JavaRuntime
	for _, path := range candidateJavaPaths() {
		rt := JavaRuntime{Path: path, Default: path == defaultPath}
		cmd := exec.Command(path, "-version")
		done := make(chan struct{})
		var combined []byte
		go func() {
			combined, _ = cmd.CombinedOutput()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			if cmd.Process != nil {
				cmd.Process.Kill()
			}
			continue
		}
		text := string(combined)
		if m := javaVersionRe.FindStringSubmatch(text); m != nil {
			rt.Version = m[1]
		}
		for _, line := range strings.Split(text, "\n") {
			l := strings.ToLower(line)
			switch {
			case strings.Contains(l, "temurin"):
				rt.Vendor = "Temurin"
			case strings.Contains(l, "microsoft"):
				rt.Vendor = "Microsoft"
			case strings.Contains(l, "zulu"):
				rt.Vendor = "Zulu"
			case strings.Contains(l, "graalvm"):
				rt.Vendor = "GraalVM"
			case strings.Contains(l, "openjdk") && rt.Vendor == "":
				rt.Vendor = "OpenJDK"
			}
		}
		if rt.Version != "" {
			out = append(out, rt)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Default != out[j].Default {
			return out[i].Default
		}
		return out[i].Version > out[j].Version
	})
	return out
}
