package web

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"blockpanel/internal/auth"
	"blockpanel/internal/store"
	"blockpanel/internal/util"
)

const sessionCookie = "bp_session"

// validTokenFormat reports whether s looks like one of our 32-byte hex
// tokens. Used to reject junk before it is used as a map key.
func validTokenFormat(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// throttleSensitive rate-limits endpoints that run a full PBKDF2 verification
// before doing anything else. Without it, any authenticated user can pin every
// core by looping password-confirm requests with a wrong password.
func (s *Server) throttleSensitive(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := "sensitive:" + s.clientIP(r)
		if u := userFrom(r); u != nil {
			key = "sensitive:" + u.ID
		}
		if !s.sensitiveLimiter.Allow(key) {
			writeErr(w, http.StatusTooManyRequests, "too many attempts, wait a minute")
			return
		}
		next(w, r)
	}
}

func (s *Server) setSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   s.cfg.CookiesSecure(),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(s.sessionTTL().Seconds()),
	})
}

func (s *Server) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/", HttpOnly: true, MaxAge: -1,
		Secure: s.cfg.CookiesSecure(), SameSite: http.SameSiteLaxMode,
	})
}

// ---- First-run setup ------------------------------------------------------

func (s *Server) handleSetupStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]bool{"needed": s.db.UserCount() == 0})
}

func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	if s.db.UserCount() > 0 {
		writeErr(w, http.StatusForbidden, "setup already completed")
		return
	}
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !readBody(w, r, &body, 1<<16) {
		return
	}
	hash, err := auth.HashPassword(body.Password)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	u := &store.User{Username: body.Username, PasswordHash: hash}
	if err := s.db.CreateFirstAdmin(u); err != nil {
		writeErr(w, http.StatusForbidden, err.Error())
		return
	}
	s.db.Audit(store.AuditEntry{User: u.Username, IP: s.clientIP(r), Action: "setup.admin_created"})
	token, _, err := s.db.CreateSession(u.ID, s.clientIP(r), s.sessionTTL())
	if err != nil {
		writeErr(w, 500, "session error")
		return
	}
	s.setSessionCookie(w, token)
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

// ---- Login ----------------------------------------------------------------

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	ip := s.clientIP(r)
	if !s.loginLimiter.Allow("ip:" + ip) {
		writeErr(w, http.StatusTooManyRequests, "too many attempts, wait a minute")
		return
	}
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !readBody(w, r, &body, 1<<16) {
		return
	}
	uname := strings.ToLower(strings.TrimSpace(body.Username))
	if len(uname) > 64 {
		writeErr(w, http.StatusUnauthorized, "wrong username or password")
		return
	}
	// Throttle per (username, IP) rather than per username alone: a
	// username-only bucket lets an unauthenticated attacker permanently lock
	// a real user — including the only admin — out of their own panel just by
	// spraying failed logins.
	if !s.loginLimiter.Allow("user:" + uname + "|" + ip) {
		writeErr(w, http.StatusTooManyRequests, "too many attempts, wait a minute")
		return
	}
	u := s.db.UserByName(uname)
	if u == nil || u.Disabled || !auth.VerifyPassword(u.PasswordHash, body.Password) {
		// Burn an equivalent hash whenever the real comparison was skipped,
		// so "no such user" and "account disabled" cost the same as a wrong
		// password and cannot be distinguished by response time.
		if u == nil || u.Disabled {
			auth.VerifyPassword("pbkdf2$sha256$600000$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", body.Password)
		}
		s.db.Audit(store.AuditEntry{User: uname, IP: ip, Action: "login.fail"})
		writeErr(w, http.StatusUnauthorized, "wrong username or password")
		return
	}

	if u.TOTPEnabled {
		tok := util.NewToken()
		s.pendingMu.Lock()
		s.pending[tok] = pendingLogin{userID: u.ID, expires: time.Now().Add(5 * time.Minute)}
		for k, p := range s.pending {
			if time.Now().After(p.expires) {
				delete(s.pending, k)
			}
		}
		s.pendingMu.Unlock()
		writeJSON(w, 200, map[string]string{"status": "totp_required", "pending": tok})
		return
	}
	s.finishLogin(w, r, u)
}

func (s *Server) handleLoginTOTP(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Pending string `json:"pending"`
		Code    string `json:"code"`
	}
	if !readBody(w, r, &body, 1<<16) {
		return
	}
	// Reject malformed tokens before they reach the limiter: the key is
	// attacker-controlled, and admitting arbitrary strings would let an
	// unauthenticated client grow the limiter's table at will.
	if !validTokenFormat(body.Pending) {
		writeErr(w, http.StatusUnauthorized, "login expired, start over")
		return
	}
	if !s.totpLimiter.Allow("pending:"+body.Pending) || !s.totpLimiter.Allow("totpip:"+s.clientIP(r)) {
		writeErr(w, http.StatusTooManyRequests, "too many attempts")
		return
	}
	s.pendingMu.Lock()
	p, ok := s.pending[body.Pending]
	s.pendingMu.Unlock()
	if !ok || time.Now().After(p.expires) {
		writeErr(w, http.StatusUnauthorized, "login expired, start over")
		return
	}
	u := s.db.UserByID(p.userID)
	if u == nil || u.Disabled || !u.TOTPEnabled {
		writeErr(w, http.StatusUnauthorized, "login expired, start over")
		return
	}
	counter, ok := auth.VerifyTOTP(u.TOTPSecret, body.Code, u.LastTOTPCounter)
	if !ok {
		s.db.Audit(store.AuditEntry{User: u.Username, IP: s.clientIP(r), Action: "login.totp_fail"})
		writeErr(w, http.StatusUnauthorized, "wrong code")
		return
	}
	s.db.UpdateUser(u.ID, func(u *store.User) error {
		u.LastTOTPCounter = counter
		return nil
	})
	s.pendingMu.Lock()
	delete(s.pending, body.Pending)
	s.pendingMu.Unlock()
	s.finishLogin(w, r, u)
}

func (s *Server) finishLogin(w http.ResponseWriter, r *http.Request, u *store.User) {
	ip := s.clientIP(r)
	token, _, err := s.db.CreateSession(u.ID, ip, s.sessionTTL())
	if err != nil {
		writeErr(w, 500, "session error")
		return
	}
	s.db.UpdateUser(u.ID, func(u *store.User) error {
		u.LastLogin = time.Now()
		return nil
	})
	s.loginLimiter.Reset("user:" + u.Username)
	s.db.Audit(store.AuditEntry{User: u.Username, IP: ip, Action: "login.success"})
	s.setSessionCookie(w, token)
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.db.DeleteSession(c.Value)
	}
	s.clearSessionCookie(w)
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

// ---- Me -------------------------------------------------------------------

type meResponse struct {
	ID           string          `json:"id"`
	Username     string          `json:"username"`
	IsAdmin      bool            `json:"is_admin"`
	MustChangePW bool            `json:"must_change_pw"`
	TOTPEnabled  bool            `json:"totp_enabled"`
	CSRF         string          `json:"csrf"`
	Global       map[string]bool `json:"global"`
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	sess := sessionFrom(r)
	global := map[string]bool{}
	for _, p := range store.GlobalPermKeys {
		global[p] = s.db.HasGlobal(u, p)
	}
	writeJSON(w, 200, meResponse{
		ID: u.ID, Username: u.Username, IsAdmin: u.IsAdmin,
		MustChangePW: u.MustChangePW, TOTPEnabled: u.TOTPEnabled,
		CSRF: sess.CSRF, Global: global,
	})
}

func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	var body struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if !readBody(w, r, &body, 1<<16) {
		return
	}
	if !auth.VerifyPassword(u.PasswordHash, body.Current) {
		writeErr(w, http.StatusForbidden, "current password is wrong")
		return
	}
	hash, err := auth.HashPassword(body.New)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	err = s.db.UpdateUser(u.ID, func(u *store.User) error {
		u.PasswordHash = hash
		u.MustChangePW = false
		return nil
	})
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	// Changing a password invalidates every existing session for the account
	// (a user doing this to lock out an attacker expects exactly that), then
	// re-establishes the current browser so this request's caller stays
	// signed in.
	s.db.DeleteUserSessions(u.ID)
	token, _, err := s.db.CreateSession(u.ID, s.clientIP(r), s.sessionTTL())
	if err != nil {
		s.clearSessionCookie(w)
		writeErr(w, 500, "password changed but re-login required")
		return
	}
	s.setSessionCookie(w, token)
	s.audit(r, "user.password_changed", u.Username, "", "")
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

// ---- TOTP enrollment ------------------------------------------------------

// Pending TOTP secrets keyed by session token hash: enrollment must confirm
// with a valid code before anything is persisted. Entries expire so an
// abandoned enrollment does not retain a live TOTP secret in memory for the
// process lifetime.
var pendingTOTP sync.Map

type pendingEnrollment struct {
	secret  string
	expires time.Time
}

const totpEnrollTTL = 10 * time.Minute

func storePendingTOTP(key, secret string) {
	now := time.Now()
	pendingTOTP.Range(func(k, v any) bool {
		if p, ok := v.(pendingEnrollment); ok && now.After(p.expires) {
			pendingTOTP.Delete(k)
		}
		return true
	})
	pendingTOTP.Store(key, pendingEnrollment{secret: secret, expires: now.Add(totpEnrollTTL)})
}

func loadPendingTOTP(key string) (string, bool) {
	v, ok := pendingTOTP.Load(key)
	if !ok {
		return "", false
	}
	p, ok := v.(pendingEnrollment)
	if !ok || time.Now().After(p.expires) {
		pendingTOTP.Delete(key)
		return "", false
	}
	return p.secret, true
}

func (s *Server) handleTOTPBegin(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	var body struct {
		Password string `json:"password"`
	}
	if !readBody(w, r, &body, 1<<16) {
		return
	}
	if !auth.VerifyPassword(u.PasswordHash, body.Password) {
		writeErr(w, http.StatusForbidden, "password is wrong")
		return
	}
	secret, err := auth.NewTOTPSecret()
	if err != nil {
		writeErr(w, 500, "could not generate secret")
		return
	}
	storePendingTOTP(sessionFrom(r).TokenHash, secret)
	writeJSON(w, 200, map[string]string{
		"secret": secret,
		"uri":    auth.TOTPURI(secret, u.Username, "BlockPanel"),
	})
}

func (s *Server) handleTOTPConfirm(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	var body struct {
		Code string `json:"code"`
	}
	if !readBody(w, r, &body, 1<<16) {
		return
	}
	secret, ok := loadPendingTOTP(sessionFrom(r).TokenHash)
	if !ok {
		writeErr(w, http.StatusBadRequest, "no enrollment in progress (it may have expired — start again)")
		return
	}
	counter, valid := auth.VerifyTOTP(secret, body.Code, 0)
	if !valid {
		writeErr(w, http.StatusBadRequest, "code does not match — check the app and try again")
		return
	}
	err := s.db.UpdateUser(u.ID, func(u *store.User) error {
		u.TOTPSecret = secret
		u.TOTPEnabled = true
		u.LastTOTPCounter = counter
		return nil
	})
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	pendingTOTP.Delete(sessionFrom(r).TokenHash)
	s.audit(r, "user.totp_enabled", u.Username, "", "")
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

func (s *Server) handleTOTPDisable(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	var body struct {
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if !readBody(w, r, &body, 1<<16) {
		return
	}
	if !auth.VerifyPassword(u.PasswordHash, body.Password) {
		writeErr(w, http.StatusForbidden, "password is wrong")
		return
	}
	if _, ok := auth.VerifyTOTP(u.TOTPSecret, body.Code, u.LastTOTPCounter); !ok {
		writeErr(w, http.StatusForbidden, "wrong code")
		return
	}
	err := s.db.UpdateUser(u.ID, func(u *store.User) error {
		u.TOTPSecret = ""
		u.TOTPEnabled = false
		u.LastTOTPCounter = 0
		return nil
	})
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.audit(r, "user.totp_disabled", u.Username, "", "")
	writeJSON(w, 200, map[string]string{"status": "ok"})
}
