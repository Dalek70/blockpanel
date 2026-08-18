package web

import (
	"context"
	"crypto/subtle"
	"net"
	"net/http"
	"strings"

	"blockpanel/internal/store"
)

type ctxKey int

const (
	ctxUser ctxKey = iota
	ctxSession
)

func userFrom(r *http.Request) *store.User {
	u, _ := r.Context().Value(ctxUser).(*store.User)
	return u
}

func sessionFrom(r *http.Request) *store.Session {
	s, _ := r.Context().Value(ctxSession).(*store.Session)
	return s
}

func (s *Server) clientIP(r *http.Request) string {
	if s.cfg.TrustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			// Use the RIGHTMOST entry: that is the address the trusted
			// reverse proxy observed and appended. The leftmost entries are
			// attacker-controllable, so trusting parts[0] would let a client
			// spoof its IP to evade the per-IP login limiter and forge audit
			// attribution. This assumes a single trusted proxy hop.
			parts := strings.Split(xff, ",")
			return strings.TrimSpace(parts[len(parts)-1])
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// auth requires a valid session cookie (or an API key) and stores
// user+session on the context.
func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// API key authentication for automation. Keys carry no CSRF token,
		// so they are only accepted from a non-browser style request (no
		// cookie) and are rate-limited like a login.
		if raw := r.Header.Get("X-API-Key"); raw != "" {
			if !s.loginLimiter.Allow("apikey:" + s.clientIP(r)) {
				writeErr(w, http.StatusTooManyRequests, "too many requests")
				return
			}
			key, user := s.db.APIKeyBySecret(raw)
			if key == nil || user == nil {
				writeErr(w, http.StatusUnauthorized, "invalid API key")
				return
			}
			if key.ReadOnly && r.Method != http.MethodGet && r.Method != http.MethodHead {
				writeErr(w, http.StatusForbidden, "this API key is read-only")
				return
			}
			if user.MustChangePW {
				writeErr(w, http.StatusForbidden, "the key owner must change their password first")
				return
			}
			ctx := context.WithValue(r.Context(), ctxUser, user)
			ctx = context.WithValue(ctx, ctxSession, &store.Session{UserID: user.ID})
			next(w, r.WithContext(ctx))
			return
		}

		cookie, err := r.Cookie(sessionCookie)
		if err != nil || cookie.Value == "" {
			writeErr(w, http.StatusUnauthorized, "not authenticated")
			return
		}
		sess, user := s.db.SessionByToken(cookie.Value, s.sessionTTL())
		if sess == nil {
			writeErr(w, http.StatusUnauthorized, "session expired")
			return
		}
		// A user issued a temporary password must set their own before doing
		// anything else. Enforced server-side, not just in the UI, so the
		// operator who knows the temp password cannot keep using the API as
		// that account and the account can't be driven with the temp
		// credential.
		if user.MustChangePW {
			switch r.URL.Path {
			case "/api/me", "/api/me/password", "/api/auth/logout":
			default:
				writeErr(w, http.StatusForbidden, "password change required before continuing")
				return
			}
		}
		ctx := context.WithValue(r.Context(), ctxUser, user)
		ctx = context.WithValue(ctx, ctxSession, sess)
		next(w, r.WithContext(ctx))
	}
}

// csrf enforces a per-session CSRF header on every mutating request.
// Defense in depth on top of SameSite cookies.
func (s *Server) csrf(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		// Auth endpoints run before a session exists; SameSite + rate
		// limiting cover them.
		p := r.URL.Path
		if p == "/api/auth/login" || p == "/api/auth/totp" || p == "/api/setup" {
			next.ServeHTTP(w, r)
			return
		}
		// API-key requests are not browser-driven, so CSRF does not apply.
		// They are rejected outright if they also carry a session cookie, so
		// this cannot be used to strip CSRF from a real browser session.
		if r.Header.Get("X-API-Key") != "" {
			if _, err := r.Cookie(sessionCookie); err == nil {
				writeErr(w, http.StatusBadRequest, "send either a session cookie or an API key, not both")
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		cookie, err := r.Cookie(sessionCookie)
		if err != nil || cookie.Value == "" {
			next.ServeHTTP(w, r) // auth middleware will reject
			return
		}
		sess, _ := s.db.SessionByToken(cookie.Value, s.sessionTTL())
		if sess == nil {
			next.ServeHTTP(w, r)
			return
		}
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(sess.CSRF)) != 1 {
			writeErr(w, http.StatusForbidden, "CSRF token missing or wrong")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy",
			"default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; "+
				"connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		if s.cfg.CookiesSecure() {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if u := userFrom(r); u == nil || !u.IsAdmin {
			writeErr(w, http.StatusForbidden, "admin only")
			return
		}
		next(w, r)
	}
}

func (s *Server) requireGlobal(perm string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.db.HasGlobal(userFrom(r), perm) {
			writeErr(w, http.StatusForbidden, "missing permission: "+perm)
			return
		}
		next(w, r)
	}
}

// requireServer checks a per-server permission for the {id} path parameter.
// Every per-server permission implies at least "view" access to that server's
// existence, so a 404 is returned only when truly unknown.
func (s *Server) requireServer(perm string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		in := s.mgr.Get(id)
		if in == nil {
			writeErr(w, http.StatusNotFound, "no such server")
			return
		}
		u := userFrom(r)
		if !s.db.HasServer(u, id, perm) {
			writeErr(w, http.StatusForbidden, "missing server permission: "+perm)
			return
		}
		next(w, r)
	}
}

func (s *Server) audit(r *http.Request, action, target, detail, serverID string) {
	name := "-"
	if u := userFrom(r); u != nil {
		name = u.Username
	}
	s.db.Audit(store.AuditEntry{
		User:     name,
		IP:       s.clientIP(r),
		Action:   action,
		Target:   target,
		Detail:   detail,
		ServerID: serverID,
	})
}
