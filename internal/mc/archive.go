package mc

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Archive/extract inside a server directory, for the file manager. Both
// directions go through ResolveWithin so nothing can be read from or written
// outside the server root.

// MaxExtractBytes caps total output of an extract operation.
const MaxExtractBytes = 8 << 30 // 8 GiB

// CreateArchive zips the given relative paths into destRel (a .zip inside the
// server directory).
func CreateArchive(root string, paths []string, destRel string) error {
	if !strings.HasSuffix(strings.ToLower(destRel), ".zip") {
		destRel += ".zip"
	}
	destAbs, err := ResolveWithin(root, destRel)
	if err != nil {
		return err
	}
	if len(paths) == 0 {
		return errors.New("nothing selected")
	}

	out, err := os.OpenFile(destAbs, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		if os.IsExist(err) {
			return errors.New("a file with that name already exists")
		}
		return err
	}
	zw := zip.NewWriter(out)

	addFile := func(abs, name string) error {
		info, err := os.Lstat(abs)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return nil // skip unreadable entries and never follow symlinks
		}
		if info.IsDir() {
			_, err := zw.Create(name + "/")
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		f, err := os.Open(abs)
		if err != nil {
			return nil
		}
		defer f.Close()
		hdr := &zip.FileHeader{Name: name, Method: zip.Deflate, Modified: info.ModTime()}
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			return err
		}
		_, err = io.Copy(w, f)
		return err
	}

	for _, rel := range paths {
		abs, err := ResolveWithin(root, rel)
		if err != nil {
			zw.Close()
			out.Close()
			os.Remove(destAbs)
			return err
		}
		// Don't archive the archive we're writing.
		if abs == destAbs {
			continue
		}
		base := filepath.Base(rel)
		info, err := os.Lstat(abs)
		if err != nil {
			// Report rather than silently producing an empty archive; a
			// path that does not resolve inside the server is a caller
			// mistake worth surfacing.
			zw.Close()
			out.Close()
			os.Remove(destAbs)
			return fmt.Errorf("cannot archive %q: no such file in this server", filepath.Base(rel))
		}
		if info.IsDir() {
			walkErr := filepath.WalkDir(abs, func(p string, d os.DirEntry, err error) error {
				if err != nil {
					return nil
				}
				if p == destAbs {
					return nil
				}
				sub, err := filepath.Rel(abs, p)
				if err != nil {
					return nil
				}
				name := base
				if sub != "." {
					name = filepath.Join(base, sub)
				}
				return addFile(p, filepath.ToSlash(name))
			})
			if walkErr != nil {
				zw.Close()
				out.Close()
				os.Remove(destAbs)
				return walkErr
			}
			continue
		}
		if err := addFile(abs, base); err != nil {
			zw.Close()
			out.Close()
			os.Remove(destAbs)
			return err
		}
	}

	if err := zw.Close(); err != nil {
		out.Close()
		os.Remove(destAbs)
		return err
	}
	return out.Close()
}

// ExtractArchive unpacks a zip inside the server directory into destRel.
func ExtractArchive(root, archiveRel, destRel string) error {
	srcAbs, err := ResolveWithin(root, archiveRel)
	if err != nil {
		return err
	}
	destAbs, err := ResolveWithin(root, destRel)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(destAbs, 0o755); err != nil {
		return err
	}

	zr, err := zip.OpenReader(srcAbs)
	if err != nil {
		return fmt.Errorf("not a readable zip archive: %w", err)
	}
	defer zr.Close()

	var declared uint64
	for _, f := range zr.File {
		declared += f.UncompressedSize64
		if declared > MaxExtractBytes {
			return fmt.Errorf("archive expands to more than %d bytes; refusing to extract", uint64(MaxExtractBytes))
		}
	}

	var written int64
	destClean := filepath.Clean(destAbs)
	for _, f := range zr.File {
		// zip-slip: force every entry to land under destAbs.
		target := filepath.Join(destClean, filepath.Clean("/"+f.Name))
		if target != destClean && !strings.HasPrefix(target, destClean+string(filepath.Separator)) {
			continue
		}
		if f.FileInfo().IsDir() {
			os.MkdirAll(target, 0o755)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		w, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
		if err != nil {
			rc.Close()
			return err
		}
		n, err := io.Copy(w, io.LimitReader(rc, MaxExtractBytes-written+1))
		rc.Close()
		w.Close()
		if err != nil {
			return err
		}
		written += n
		if written > MaxExtractBytes {
			return errors.New("archive expands past the extract limit; aborted")
		}
	}
	return nil
}

// SearchFiles finds entries whose name contains needle, breadth-limited so a
// huge world directory cannot stall the request.
func SearchFiles(root, rel, needle string, max int) ([]FileEntry, error) {
	base, err := ResolveWithin(root, rel)
	if err != nil {
		return nil, err
	}
	needle = strings.ToLower(strings.TrimSpace(needle))
	if needle == "" {
		return nil, errors.New("search term required")
	}
	if max <= 0 || max > 500 {
		max = 200
	}
	out := []FileEntry{} // never nil, so the API returns [] rather than null
	visited := 0
	filepath.WalkDir(base, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		visited++
		if visited > 200_000 || len(out) >= max {
			return filepath.SkipAll
		}
		if !strings.Contains(strings.ToLower(d.Name()), needle) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		relPath, err := filepath.Rel(base, p)
		if err != nil {
			return nil
		}
		out = append(out, FileEntry{
			Name:    filepath.ToSlash(filepath.Join(rel, relPath)),
			IsDir:   d.IsDir(),
			Size:    info.Size(),
			ModTime: info.ModTime(),
		})
		return nil
	})
	return out, nil
}
