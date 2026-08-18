package mc

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const MaxEditableFile = 1 << 20 // 1 MiB text-editor cap

var (
	ErrPathEscape = errors.New("path escapes server directory")
	ErrIsSymlink  = errors.New("symlinks are not allowed")
)

type FileEntry struct {
	Name    string    `json:"name"`
	IsDir   bool      `json:"is_dir"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mod_time"`
}

// ResolveWithin joins rel onto root and guarantees the result stays inside
// root, defeating both ".." traversal and symlink escapes. The deepest
// existing ancestor of the target is resolved through EvalSymlinks and
// re-checked for containment.
func ResolveWithin(root, rel string) (string, error) {
	rel = strings.TrimSpace(rel)
	rel = strings.TrimPrefix(rel, "/")
	if strings.Contains(rel, "\x00") {
		return "", ErrPathEscape
	}
	cleaned := filepath.Clean("/" + rel) // forces lexical containment
	target := filepath.Join(root, cleaned)

	rootReal, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolve root: %w", err)
	}

	// Find the deepest existing ancestor and resolve it.
	probe := target
	var tail []string
	for {
		if _, err := os.Lstat(probe); err == nil {
			break
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			return "", ErrPathEscape
		}
		tail = append([]string{filepath.Base(probe)}, tail...)
		probe = parent
	}
	probeReal, err := filepath.EvalSymlinks(probe)
	if err != nil {
		return "", err
	}
	resolved := filepath.Join(append([]string{probeReal}, tail...)...)
	if resolved != rootReal && !strings.HasPrefix(resolved, rootReal+string(filepath.Separator)) {
		return "", ErrPathEscape
	}
	return target, nil
}

// lstatNoSymlink rejects operations directly on a symlink so the file
// manager can never be used to read or write through one.
func lstatNoSymlink(path string) (os.FileInfo, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return nil, ErrIsSymlink
	}
	return fi, nil
}

func ListDir(root, rel string) ([]FileEntry, error) {
	abs, err := ResolveWithin(root, rel)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return nil, err
	}
	out := make([]FileEntry, 0, len(entries))
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, FileEntry{
			Name:    e.Name(),
			IsDir:   e.IsDir(),
			Size:    info.Size(),
			ModTime: info.ModTime(),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].IsDir != out[j].IsDir {
			return out[i].IsDir
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out, nil
}

// ReadTextFile returns the file contents when it is small enough for the
// editor and does not look binary.
func ReadTextFile(root, rel string) (string, error) {
	abs, err := ResolveWithin(root, rel)
	if err != nil {
		return "", err
	}
	fi, err := lstatNoSymlink(abs)
	if err != nil {
		return "", err
	}
	if fi.IsDir() {
		return "", errors.New("is a directory")
	}
	if fi.Size() > MaxEditableFile {
		return "", fmt.Errorf("file too large to edit (%d bytes, max %d)", fi.Size(), MaxEditableFile)
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return "", err
	}
	if isBinary(data) {
		return "", errors.New("binary file — download it instead")
	}
	return string(data), nil
}

func WriteTextFile(root, rel, content string) error {
	abs, err := ResolveWithin(root, rel)
	if err != nil {
		return err
	}
	if fi, err := os.Lstat(abs); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			return ErrIsSymlink
		}
		if fi.IsDir() {
			return errors.New("is a directory")
		}
	}
	if len(content) > MaxEditableFile {
		return errors.New("content too large")
	}
	return os.WriteFile(abs, []byte(content), 0o644)
}

// maxMkdirDepth bounds how many directories one request may create.
// os.MkdirAll happily creates a component per path segment and does not roll
// back when the path finally exceeds the OS limit, so an unbounded path is a
// cheap disk/inode amplification primitive.
const maxMkdirDepth = 16

func Mkdir(root, rel string) error {
	cleaned := strings.Trim(filepath.Clean("/"+strings.TrimSpace(rel)), "/")
	if cleaned == "" || cleaned == "." {
		return errors.New("empty path")
	}
	if n := len(strings.Split(cleaned, "/")); n > maxMkdirDepth {
		return fmt.Errorf("path is too deep (%d levels, max %d)", n, maxMkdirDepth)
	}
	abs, err := ResolveWithin(root, rel)
	if err != nil {
		return err
	}
	return os.MkdirAll(abs, 0o755)
}

func Rename(root, from, to string) error {
	absFrom, err := ResolveWithin(root, from)
	if err != nil {
		return err
	}
	absTo, err := ResolveWithin(root, to)
	if err != nil {
		return err
	}
	if absFrom == filepath.Clean(root) {
		return errors.New("cannot rename server root")
	}
	return os.Rename(absFrom, absTo)
}

func Delete(root, rel string) error {
	abs, err := ResolveWithin(root, rel)
	if err != nil {
		return err
	}
	if abs == filepath.Clean(root) {
		return errors.New("cannot delete server root")
	}
	return os.RemoveAll(abs)
}

// OpenForDownload returns a reader for a regular file (symlinks rejected).
func OpenForDownload(root, rel string) (*os.File, os.FileInfo, error) {
	abs, err := ResolveWithin(root, rel)
	if err != nil {
		return nil, nil, err
	}
	fi, err := lstatNoSymlink(abs)
	if err != nil {
		return nil, nil, err
	}
	if fi.IsDir() {
		return nil, nil, errors.New("is a directory")
	}
	f, err := os.Open(abs)
	if err != nil {
		return nil, nil, err
	}
	return f, fi, nil
}

// SaveUpload streams an uploaded file into the server directory.
func SaveUpload(root, relDir, name string, r io.Reader) (int64, error) {
	name = filepath.Base(name)
	if name == "" || name == "." || name == ".." {
		return 0, errors.New("bad filename")
	}
	abs, err := ResolveWithin(root, filepath.Join(relDir, name))
	if err != nil {
		return 0, err
	}
	f, err := os.OpenFile(abs, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	return io.Copy(f, r)
}

func isBinary(data []byte) bool {
	n := len(data)
	if n > 8000 {
		n = 8000
	}
	return bytes.IndexByte(data[:n], 0) >= 0
}
