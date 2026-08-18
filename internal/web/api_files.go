package web

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"blockpanel/internal/mc"
	mcstore "blockpanel/internal/store"
	"blockpanel/internal/util"
)

// disallowedIP reports whether an address is one the panel must never make a
// server-side request to: loopback, private (RFC1918 / ULA), link-local
// (incl. 169.254 cloud metadata), CGNAT, multicast or unspecified. Used to
// stop the jar-download feature from being turned into an SSRF probe of the
// internal network or the cloud metadata service.
func disallowedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() ||
		ip.IsMulticast() || ip.IsUnspecified() {
		return true
	}
	// 100.64.0.0/10 carrier-grade NAT
	if v4 := ip.To4(); v4 != nil && v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127 {
		return true
	}
	return false
}

// safeHTTPClient blocks connections to non-public addresses at dial time. The
// dialer Control hook runs after DNS resolution with the concrete IP being
// connected to, so it also defeats DNS-rebinding and redirect-based bypasses.
func safeHTTPClient(timeout time.Duration) *http.Client {
	dialer := &net.Dialer{
		Timeout: 30 * time.Second,
		Control: func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			if disallowedIP(net.ParseIP(host)) {
				return fmt.Errorf("refusing to connect to non-public address %s", host)
			}
			return nil
		},
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: &http.Transport{DialContext: dialer.DialContext},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			if req.URL.Scheme != "https" {
				return errors.New("refusing redirect to non-https URL")
			}
			return nil
		},
	}
}

func (s *Server) handleFilesList(w http.ResponseWriter, r *http.Request) {
	in := s.mgr.Get(r.PathValue("id"))
	entries, err := mc.ListDir(in.Config().Root, r.URL.Query().Get("path"))
	if err != nil {
		writeFSErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, 200, entries)
}

func (s *Server) handleFileRead(w http.ResponseWriter, r *http.Request) {
	in := s.mgr.Get(r.PathValue("id"))
	content, err := mc.ReadTextFile(in.Config().Root, r.URL.Query().Get("path"))
	if err != nil {
		writeFSErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, 200, map[string]string{"content": content})
}

func (s *Server) handleFileWrite(w http.ResponseWriter, r *http.Request) {
	in := s.mgr.Get(r.PathValue("id"))
	var body struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if !readBody(w, r, &body, mc.MaxEditableFile+4096) {
		return
	}
	if err := mc.WriteTextFile(in.Config().Root, body.Path, body.Content); err != nil {
		writeFSErr(w, http.StatusBadRequest, err)
		return
	}
	s.audit(r, "files.write", body.Path, "", r.PathValue("id"))
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

func (s *Server) handleFileMkdir(w http.ResponseWriter, r *http.Request) {
	in := s.mgr.Get(r.PathValue("id"))
	var body struct {
		Path string `json:"path"`
	}
	if !readBody(w, r, &body, 1<<16) {
		return
	}
	if err := mc.Mkdir(in.Config().Root, body.Path); err != nil {
		writeFSErr(w, http.StatusBadRequest, err)
		return
	}
	s.audit(r, "files.mkdir", body.Path, "", r.PathValue("id"))
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

func (s *Server) handleFileRename(w http.ResponseWriter, r *http.Request) {
	in := s.mgr.Get(r.PathValue("id"))
	var body struct {
		From string `json:"from"`
		To   string `json:"to"`
	}
	if !readBody(w, r, &body, 1<<16) {
		return
	}
	if err := mc.Rename(in.Config().Root, body.From, body.To); err != nil {
		writeFSErr(w, http.StatusBadRequest, err)
		return
	}
	s.audit(r, "files.rename", body.From+" -> "+body.To, "", r.PathValue("id"))
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

func (s *Server) handleFileDelete(w http.ResponseWriter, r *http.Request) {
	in := s.mgr.Get(r.PathValue("id"))
	var body struct {
		Path string `json:"path"`
	}
	if !readBody(w, r, &body, 1<<16) {
		return
	}
	if err := mc.Delete(in.Config().Root, body.Path); err != nil {
		writeFSErr(w, http.StatusBadRequest, err)
		return
	}
	s.audit(r, "files.delete", body.Path, "", r.PathValue("id"))
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

func (s *Server) handleFileUpload(w http.ResponseWriter, r *http.Request) {
	in := s.mgr.Get(r.PathValue("id"))
	r.Body = http.MaxBytesReader(w, r.Body, s.maxUpload())
	mr, err := r.MultipartReader()
	if err != nil {
		writeErr(w, http.StatusBadRequest, "multipart upload expected: "+err.Error())
		return
	}
	dir := r.URL.Query().Get("path")
	var saved []string
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			writeFSErr(w, http.StatusBadRequest, err)
			return
		}
		if part.FormName() != "file" || part.FileName() == "" {
			part.Close()
			continue
		}
		n, err := mc.SaveUpload(in.Config().Root, dir, part.FileName(), part)
		part.Close()
		if err != nil {
			writeErr(w, http.StatusBadRequest, fmt.Sprintf("%s: %s", part.FileName(), err))
			return
		}
		saved = append(saved, fmt.Sprintf("%s (%s)", part.FileName(), util.HumanBytes(n)))
		s.audit(r, "files.upload", filepath.Join(dir, part.FileName()), "", r.PathValue("id"))
	}
	if len(saved) == 0 {
		writeErr(w, http.StatusBadRequest, "no files in upload")
		return
	}
	writeJSON(w, 200, map[string]any{"saved": saved})
}

// handleFileDownload enforces the per-server download policy for non-admins:
// downloads off = nothing; blocked extensions (e.g. jar) = those files never
// leave the server. Admins bypass policy.
func (s *Server) handleFileDownload(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	in := s.mgr.Get(r.PathValue("id"))
	cfg := in.Config()
	rel := r.URL.Query().Get("path")

	// Check the policy against the SAME name the file is actually opened as.
	// OpenForDownload resolves through TrimSpace+Clean, so checking the raw
	// query string would let "server.jar " or "server.jar/" read a different
	// "extension" than the file that ends up being served.
	checkName := filepath.Base(filepath.Clean("/" + strings.TrimSpace(rel)))
	if !u.IsAdmin && cfg.DownloadBlocked(checkName) {
		s.audit(r, "files.download_blocked", rel, "download policy", r.PathValue("id"))
		writeErr(w, http.StatusForbidden, "downloads of this file are disabled by the server's download policy")
		return
	}
	f, fi, err := mc.OpenForDownload(cfg.Root, rel)
	if err != nil {
		writeFSErr(w, http.StatusBadRequest, err)
		return
	}
	defer f.Close()
	name := filepath.Base(rel)
	ctype := mime.TypeByExtension(filepath.Ext(name))
	if ctype == "" {
		ctype = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name))
	w.Header().Set("Content-Length", fmt.Sprintf("%d", fi.Size()))
	s.audit(r, "files.download", rel, "", r.PathValue("id"))
	io.Copy(w, f)
}

// handleJarURL downloads a server jar over HTTPS into the server root (e.g.
// a Paper build) and points the config at it.
func (s *Server) handleJarURL(w http.ResponseWriter, r *http.Request) {
	in := s.mgr.Get(r.PathValue("id"))
	cfg := in.Config()
	var body struct {
		URL      string `json:"url"`
		Filename string `json:"filename"`
	}
	if !readBody(w, r, &body, 1<<16) {
		return
	}
	parsed, err := url.Parse(body.URL)
	if err != nil || parsed.Scheme != "https" {
		writeErr(w, http.StatusBadRequest, "an https:// URL is required")
		return
	}
	name := body.Filename
	if name == "" {
		name = filepath.Base(parsed.Path)
	}
	name = filepath.Base(name)
	if name == "" || name == "." || name == "/" || !strings.HasSuffix(strings.ToLower(name), ".jar") {
		writeErr(w, http.StatusBadRequest, "target filename must end in .jar")
		return
	}

	client := safeHTTPClient(10 * time.Minute)
	req, err := http.NewRequestWithContext(context.Background(), "GET", parsed.String(), nil)
	if err != nil {
		writeFSErr(w, http.StatusBadRequest, err)
		return
	}
	resp, err := client.Do(req)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "download failed: "+err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		writeErr(w, http.StatusBadGateway, "download failed: "+resp.Status)
		return
	}
	const maxJar = 1 << 30 // 1 GiB cap
	n, err := mc.SaveUpload(cfg.Root, "", name, io.LimitReader(resp.Body, maxJar))
	if err != nil {
		writeFSErr(w, 500, err)
		return
	}
	if n >= maxJar {
		mc.Delete(cfg.Root, name)
		writeErr(w, http.StatusBadRequest, "file exceeds the 1 GiB limit")
		return
	}
	if _, err := s.mgr.MutateConfig(r.PathValue("id"), func(c *mcstore.Server) error {
		c.Jar = name
		return nil
	}); err != nil {
		writeFSErr(w, 500, err)
		return
	}
	s.audit(r, "files.jar_url", body.URL, name, r.PathValue("id"))
	writeJSON(w, 200, map[string]string{"status": "ok", "jar": name, "size": util.HumanBytes(n)})
}
