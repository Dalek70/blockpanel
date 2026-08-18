package mc

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

type BackupInfo struct {
	Name    string    `json:"name"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mod_time"`
}

var backupNameRe = regexp.MustCompile(`^[a-zA-Z0-9._-]+\.zip$`)

// MaxBackupsPerServer bounds how many archives one server may accumulate, so
// a user with backups.create cannot fill the disk by looping the endpoint.
const MaxBackupsPerServer = 50

// CreateBackup zips the server root into backupsDir. Files that vanish or
// error mid-walk (a live server rewrites region files) are skipped rather
// than failing the whole backup.
func CreateBackup(root, backupsDir string) (string, error) {
	if err := os.MkdirAll(backupsDir, 0o750); err != nil {
		return "", err
	}
	existing, err := ListBackups(backupsDir)
	if err == nil && len(existing) >= MaxBackupsPerServer {
		return "", fmt.Errorf("this server already has %d backups (the maximum); delete some first", MaxBackupsPerServer)
	}
	// O_EXCL with a uniquifying suffix: the timestamp only has one-second
	// resolution, so two backups started in the same second would otherwise
	// open, truncate and interleave into the same file and both "succeed",
	// leaving one corrupt archive.
	base := time.Now().Format("2006-01-02_15-04-05")
	var name, tmp string
	var out *os.File
	for attempt := 0; ; attempt++ {
		name = base + ".zip"
		if attempt > 0 {
			name = fmt.Sprintf("%s_%d.zip", base, attempt)
		}
		if _, err := os.Stat(filepath.Join(backupsDir, name)); err == nil {
			continue
		}
		tmp = filepath.Join(backupsDir, ".partial-"+name)
		out, err = os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			break
		}
		if !os.IsExist(err) {
			return "", err
		}
		if attempt > 100 {
			return "", errors.New("could not allocate a backup filename")
		}
	}
	zw := zip.NewWriter(out)

	rootClean := filepath.Clean(root)
	err = filepath.WalkDir(rootClean, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil // never follow symlinks into a backup
		}
		rel, err := filepath.Rel(rootClean, path)
		if err != nil || rel == "." {
			return nil
		}
		if d.IsDir() {
			_, err := zw.Create(rel + "/")
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer f.Close()
		info, _ := d.Info()
		hdr := &zip.FileHeader{Name: rel, Method: zip.Deflate}
		if info != nil {
			hdr.Modified = info.ModTime()
		}
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			return err
		}
		_, err = io.Copy(w, f)
		if err != nil {
			return nil // skip files that error mid-read
		}
		return nil
	})
	if cerr := zw.Close(); err == nil {
		err = cerr
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return "", err
	}
	final := filepath.Join(backupsDir, name)
	if err := os.Rename(tmp, final); err != nil {
		os.Remove(tmp)
		return "", err
	}
	return name, nil
}

func ListBackups(backupsDir string) ([]BackupInfo, error) {
	entries, err := os.ReadDir(backupsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []BackupInfo{}, nil
		}
		return nil, err
	}
	out := []BackupInfo{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".zip") || strings.HasPrefix(e.Name(), ".partial-") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, BackupInfo{Name: e.Name(), Size: info.Size(), ModTime: info.ModTime()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ModTime.After(out[j].ModTime) })
	return out, nil
}

// BackupPath validates name against the safe pattern and returns its path.
func BackupPath(backupsDir, name string) (string, error) {
	if !backupNameRe.MatchString(name) || strings.Contains(name, "..") {
		return "", errors.New("bad backup name")
	}
	return filepath.Join(backupsDir, name), nil
}

// MaxRestoreBytes caps total extracted output so a crafted or corrupt zip
// (a decompression bomb) cannot fill the disk during a restore.
const MaxRestoreBytes = 64 << 30 // 64 GiB

// RestoreBackup wipes the server root and extracts the zip into it. Caller
// must ensure the server is stopped.
func RestoreBackup(root, backupsDir, name string) error {
	zipPath, err := BackupPath(backupsDir, name)
	if err != nil {
		return err
	}
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer zr.Close()

	// Reject up front when the declared uncompressed size is absurd; the
	// running total below is the real guard (headers can lie).
	var declared uint64
	for _, f := range zr.File {
		declared += f.UncompressedSize64
		if declared > MaxRestoreBytes {
			return fmt.Errorf("backup expands to more than %d bytes; refusing to restore", uint64(MaxRestoreBytes))
		}
	}
	var written int64

	rootClean := filepath.Clean(root)
	entries, err := os.ReadDir(rootClean)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(rootClean, e.Name())); err != nil {
			return fmt.Errorf("clearing %s: %w", e.Name(), err)
		}
	}

	for _, f := range zr.File {
		// zip-slip protection
		dest := filepath.Join(rootClean, filepath.Clean("/"+f.Name))
		if dest != rootClean && !strings.HasPrefix(dest, rootClean+string(filepath.Separator)) {
			continue
		}
		if f.FileInfo().IsDir() {
			os.MkdirAll(dest, 0o755)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		w, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
		if err != nil {
			rc.Close()
			return err
		}
		// Bound each entry by whatever budget is left, so a lying header
		// cannot expand past the cap.
		n, err := io.Copy(w, io.LimitReader(rc, MaxRestoreBytes-written+1))
		rc.Close()
		w.Close()
		if err != nil {
			return err
		}
		written += n
		if written > MaxRestoreBytes {
			return fmt.Errorf("backup expands past the %d byte restore limit; aborted", uint64(MaxRestoreBytes))
		}
	}
	return nil
}

func DeleteBackup(backupsDir, name string) error {
	path, err := BackupPath(backupsDir, name)
	if err != nil {
		return err
	}
	return os.Remove(path)
}

// PruneBackups enforces a retention limit, deleting the oldest archives
// beyond keep. keep <= 0 means "keep everything" and prunes nothing.
// Returns how many were removed.
func PruneBackups(backupsDir string, keep int) (int, error) {
	if keep <= 0 {
		return 0, nil
	}
	list, err := ListBackups(backupsDir)
	if err != nil {
		return 0, err
	}
	if len(list) <= keep {
		return 0, nil
	}
	removed := 0
	// ListBackups is newest-first, so everything past `keep` is surplus.
	for _, b := range list[keep:] {
		if err := DeleteBackup(backupsDir, b.Name); err == nil {
			removed++
		}
	}
	return removed, nil
}
