package update

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestCheckGitHubURL(t *testing.T) {
	ok := []string{
		"https://api.github.com/repos/x/y/releases/latest",
		"https://github.com/x/y/releases/download/v1/blockpanel-linux-amd64",
		"https://objects.githubusercontent.com/foo",
		"https://release-assets.githubusercontent.com/foo",
	}
	for _, u := range ok {
		if err := checkGitHubURL(u); err != nil {
			t.Errorf("checkGitHubURL(%q) = %v, want nil", u, err)
		}
	}
	bad := []string{
		"http://api.github.com/insecure",            // not https
		"https://evil.com/blockpanel",               // wrong host
		"https://github.com.evil.com/x",             // suffix trick
		"https://fakegithubusercontent.com/x",       // missing dot boundary
		"https://x.githubusercontent.com.evil.io/x", // suffix trick again
	}
	for _, u := range bad {
		if err := checkGitHubURL(u); err == nil {
			t.Errorf("checkGitHubURL(%q) accepted, want error", u)
		}
	}
}

func TestParseSums(t *testing.T) {
	raw := []byte(
		"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef  blockpanel-linux-amd64\n" +
			"fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210 *blockpanel-1.0.1.zip\n" +
			"not-a-sum-line\n")
	sums := parseSums(raw)
	if len(sums) != 2 {
		t.Fatalf("got %d entries, want 2", len(sums))
	}
	if sums["blockpanel-linux-amd64"] == "" || sums["blockpanel-1.0.1.zip"] == "" {
		t.Errorf("missing expected entries: %v", sums)
	}
}

func TestExtractFromZip(t *testing.T) {
	want := "blockpanel-" + runtime.GOOS + "-" + runtime.GOARCH
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, name := range []string{
		"blockpanel-1.0.1/README.md",
		"blockpanel-1.0.1/bin/" + want,
		"blockpanel-1.0.1/bin/blockpanel-other-arch",
	} {
		w, _ := zw.Create(name)
		w.Write([]byte("content of " + name))
	}
	zw.Close()

	data, err := extractFromZip(buf.Bytes(), want)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "content of blockpanel-1.0.1/bin/"+want {
		t.Errorf("wrong file extracted: %q", data)
	}
	if _, err := extractFromZip(buf.Bytes(), "blockpanel-missing-platform"); err == nil {
		t.Error("expected error for missing platform binary")
	}
}

func TestVerifyBinary(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good")
	os.WriteFile(good, []byte("#!/bin/sh\necho blockpanel 2.5.0\n"), 0o755)
	if err := verifyBinary(good, "2.5.0"); err != nil {
		t.Errorf("good binary rejected: %v", err)
	}
	if err := verifyBinary(good, "2.6.0"); err == nil {
		t.Error("version mismatch accepted")
	}
	imposter := filepath.Join(dir, "imposter")
	os.WriteFile(imposter, []byte("#!/bin/sh\necho notpanel 2.5.0\n"), 0o755)
	if err := verifyBinary(imposter, "2.5.0"); err == nil {
		t.Error("imposter accepted")
	}
	broken := filepath.Join(dir, "broken")
	os.WriteFile(broken, []byte("#!/bin/sh\nexit 3\n"), 0o755)
	if err := verifyBinary(broken, "2.5.0"); err == nil {
		t.Error("crashing binary accepted")
	}
}
