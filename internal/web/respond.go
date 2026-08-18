// Package web is the HTTP layer: REST API, SSE streams, static SPA assets,
// auth middleware and TLS setup.
package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"strings"
)

type apiError struct {
	Error string `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, apiError{Error: msg})
}

// writeFSErr reports a filesystem error without disclosing absolute host
// paths. os errors are *fs.PathError and stringify with the full path, which
// would leak the install location, the host account name and — for imported
// servers — an admin-chosen directory the file API otherwise never reveals.
func writeFSErr(w http.ResponseWriter, status int, err error) {
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		writeJSON(w, status, apiError{Error: fmt.Sprintf("%s: %s", pathErr.Op, pathErr.Err)})
		return
	}
	writeJSON(w, status, apiError{Error: err.Error()})
}

func readBody(w http.ResponseWriter, r *http.Request, v any, maxBytes int64) bool {
	// Require a JSON content type. A cross-site HTML form can only send
	// text/plain, multipart or urlencoded without triggering a CORS
	// preflight, so enforcing application/json here blocks forged
	// cross-origin requests to the CSRF-exempt auth endpoints (login CSRF).
	ct := r.Header.Get("Content-Type")
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	if strings.TrimSpace(ct) != "application/json" {
		writeErr(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return false
	}
	if maxBytes <= 0 {
		maxBytes = 1 << 20
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request body: "+err.Error())
		return false
	}
	return true
}
