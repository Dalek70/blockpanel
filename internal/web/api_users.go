package web

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"blockpanel/internal/auth"
	"blockpanel/internal/store"
)

type userView struct {
	ID              string                     `json:"id"`
	Username        string                     `json:"username"`
	IsAdmin         bool                       `json:"is_admin"`
	Disabled        bool                       `json:"disabled"`
	MustChangePW    bool                       `json:"must_change_pw"`
	RoleID          string                     `json:"role_id"`
	Overrides       map[string]bool            `json:"overrides"`
	ServerOverrides map[string]map[string]bool `json:"server_overrides"`
	TOTPEnabled     bool                       `json:"totp_enabled"`
	CreatedAt       time.Time                  `json:"created_at"`
	LastLogin       time.Time                  `json:"last_login"`
}

func toUserView(u *store.User) userView {
	ov := u.Overrides
	if ov == nil {
		ov = map[string]bool{}
	}
	sov := u.ServerOverrides
	if sov == nil {
		sov = map[string]map[string]bool{}
	}
	return userView{
		ID: u.ID, Username: u.Username, IsAdmin: u.IsAdmin, Disabled: u.Disabled,
		MustChangePW: u.MustChangePW, RoleID: u.RoleID, Overrides: ov,
		ServerOverrides: sov, TOTPEnabled: u.TOTPEnabled,
		CreatedAt: u.CreatedAt, LastLogin: u.LastLogin,
	}
}

func (s *Server) handleUsersList(w http.ResponseWriter, r *http.Request) {
	users := s.db.Users()
	out := make([]userView, 0, len(users))
	for _, u := range users {
		out = append(out, toUserView(u))
	}
	writeJSON(w, 200, out)
}

var errNoAdminLeft = errors.New("this change would leave the panel with no active admin")

// atLeastOneAdmin is the store invariant that keeps at least one enabled admin
// account, evaluated atomically with the mutation to avoid a check-then-act
// race between two concurrent demote/disable requests.
func atLeastOneAdmin(users []*store.User) error {
	for _, u := range users {
		if u.IsAdmin && !u.Disabled {
			return nil
		}
	}
	return errNoAdminLeft
}

// Only admins may create or modify admin accounts; a users.manage user works
// on regular accounts only.
func (s *Server) canManageTarget(actor *store.User, target *store.User) bool {
	if actor.IsAdmin {
		return true
	}
	return !target.IsAdmin
}

// actorHasServerPerm reports whether the actor personally holds perm on server
// sid, used to decide what they are allowed to hand out. Granting on "*" (all
// servers) requires the actor to hold the perm on "*" specifically; granting
// on a concrete server uses the normal resolution (which already honors "*").
func (s *Server) actorHasServerPerm(actor *store.User, sid, perm string) bool {
	if actor.IsAdmin {
		return true
	}
	if sid == "*" {
		if m, ok := actor.ServerOverrides["*"]; ok {
			if v, ok := m[perm]; ok {
				return v
			}
		}
		if r := s.db.RoleByID(actor.RoleID); r != nil {
			if m, ok := r.Servers["*"]; ok {
				if v, ok := m[perm]; ok {
					return v
				}
			}
		}
		return false
	}
	return s.db.HasServer(actor, sid, perm)
}

// validateGrant enforces least privilege: a non-admin may never create or edit
// a user (including themselves) so that the user ends up holding a permission
// the actor does not itself hold. Without this, anyone with users.manage could
// self-assign servers.manage, ai.use, roles.manage, or any per-server
// permission via overrides or by assigning a more powerful role. Denies
// (false) are always allowed. roleID/overrides/serverOverrides describe the
// user's resulting state after the change.
func (s *Server) validateGrant(actor *store.User, roleID string, overrides map[string]bool, serverOverrides map[string]map[string]bool) error {
	if actor.IsAdmin {
		return nil
	}
	role := s.db.RoleByID(roleID)
	for _, k := range store.GlobalPermKeys {
		granted := false
		if role != nil {
			if v, ok := role.Global[k]; ok {
				granted = v
			}
		}
		if v, ok := overrides[k]; ok {
			granted = v
		}
		if granted && !s.db.HasGlobal(actor, k) {
			return fmt.Errorf("you cannot grant a permission you do not have: %s", k)
		}
	}
	sids := map[string]struct{}{}
	if role != nil {
		for sid := range role.Servers {
			sids[sid] = struct{}{}
		}
	}
	for sid := range serverOverrides {
		sids[sid] = struct{}{}
	}
	for sid := range sids {
		for _, k := range store.ServerPermKeys {
			granted := false
			if role != nil {
				if m, ok := role.Servers[sid]; ok {
					if v, ok := m[k]; ok {
						granted = v
					}
				}
			}
			if m, ok := serverOverrides[sid]; ok {
				if v, ok := m[k]; ok {
					granted = v
				}
			}
			if granted && !s.actorHasServerPerm(actor, sid, k) {
				return fmt.Errorf("you cannot grant a server permission you do not have: %s on %s", k, sid)
			}
		}
	}
	return nil
}

// validateRoleGrant is the same least-privilege rule for roles: a non-admin
// with roles.manage may not put a permission into a role that they do not hold
// themselves — otherwise they could self-escalate by editing a role they are
// assigned to.
func (s *Server) validateRoleGrant(actor *store.User, global map[string]bool, servers map[string]map[string]bool) error {
	if actor.IsAdmin {
		return nil
	}
	for _, k := range store.GlobalPermKeys {
		if global[k] && !s.db.HasGlobal(actor, k) {
			return fmt.Errorf("you cannot put a permission you do not have into a role: %s", k)
		}
	}
	for sid, m := range servers {
		for _, k := range store.ServerPermKeys {
			if m[k] && !s.actorHasServerPerm(actor, sid, k) {
				return fmt.Errorf("you cannot put a server permission you do not have into a role: %s on %s", k, sid)
			}
		}
	}
	return nil
}

func (s *Server) handleUserCreate(w http.ResponseWriter, r *http.Request) {
	actor := userFrom(r)
	var body struct {
		Username        string                     `json:"username"`
		Password        string                     `json:"password"`
		IsAdmin         bool                       `json:"is_admin"`
		RoleID          string                     `json:"role_id"`
		Overrides       map[string]bool            `json:"overrides"`
		ServerOverrides map[string]map[string]bool `json:"server_overrides"`
		MustChangePW    bool                       `json:"must_change_pw"`
	}
	if !readBody(w, r, &body, 1<<20) {
		return
	}
	if body.IsAdmin && !actor.IsAdmin {
		writeErr(w, http.StatusForbidden, "only an admin can create admin accounts")
		return
	}
	if body.RoleID != "" && s.db.RoleByID(body.RoleID) == nil {
		writeErr(w, http.StatusBadRequest, "unknown role")
		return
	}
	if err := s.validateGrant(actor, body.RoleID, body.Overrides, body.ServerOverrides); err != nil {
		writeErr(w, http.StatusForbidden, err.Error())
		return
	}
	hash, err := auth.HashPassword(body.Password)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	u := &store.User{
		Username: body.Username, PasswordHash: hash, IsAdmin: body.IsAdmin,
		RoleID: body.RoleID, Overrides: body.Overrides,
		ServerOverrides: body.ServerOverrides, MustChangePW: body.MustChangePW,
	}
	if err := s.db.CreateUser(u); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s.audit(r, "user.create", u.Username, "", "")
	writeJSON(w, 200, toUserView(u))
}

func (s *Server) handleUserUpdate(w http.ResponseWriter, r *http.Request) {
	actor := userFrom(r)
	target := s.db.UserByID(r.PathValue("id"))
	if target == nil {
		writeErr(w, http.StatusNotFound, "no such user")
		return
	}
	if !s.canManageTarget(actor, target) {
		writeErr(w, http.StatusForbidden, "only an admin can modify admin accounts")
		return
	}
	var body struct {
		IsAdmin         *bool                       `json:"is_admin"`
		Disabled        *bool                       `json:"disabled"`
		RoleID          *string                     `json:"role_id"`
		Overrides       *map[string]bool            `json:"overrides"`
		ServerOverrides *map[string]map[string]bool `json:"server_overrides"`
	}
	if !readBody(w, r, &body, 1<<20) {
		return
	}
	if body.IsAdmin != nil && !actor.IsAdmin {
		writeErr(w, http.StatusForbidden, "only an admin can grant or revoke admin")
		return
	}
	if body.RoleID != nil && *body.RoleID != "" && s.db.RoleByID(*body.RoleID) == nil {
		writeErr(w, http.StatusBadRequest, "unknown role")
		return
	}
	// Never let the last admin lock everyone out.
	losingAdmin := (body.IsAdmin != nil && !*body.IsAdmin && target.IsAdmin) ||
		(body.Disabled != nil && *body.Disabled && target.IsAdmin)
	if losingAdmin && s.db.AdminCount() <= 1 {
		writeErr(w, http.StatusBadRequest, "cannot demote or disable the last admin")
		return
	}
	// Enforce least privilege on the resulting permission set (skipped for
	// admin actors). Fields not present in the patch keep the target's
	// current values.
	effRole := target.RoleID
	if body.RoleID != nil {
		effRole = *body.RoleID
	}
	effOverrides := target.Overrides
	if body.Overrides != nil {
		effOverrides = *body.Overrides
	}
	effServer := target.ServerOverrides
	if body.ServerOverrides != nil {
		effServer = *body.ServerOverrides
	}
	if err := s.validateGrant(actor, effRole, effOverrides, effServer); err != nil {
		writeErr(w, http.StatusForbidden, err.Error())
		return
	}
	err := s.db.UpdateUserGuarded(target.ID, func(u *store.User) error {
		if body.IsAdmin != nil {
			u.IsAdmin = *body.IsAdmin
		}
		if body.Disabled != nil {
			u.Disabled = *body.Disabled
		}
		if body.RoleID != nil {
			u.RoleID = *body.RoleID
		}
		if body.Overrides != nil {
			u.Overrides = *body.Overrides
		}
		if body.ServerOverrides != nil {
			u.ServerOverrides = *body.ServerOverrides
		}
		return nil
	}, atLeastOneAdmin)
	if err != nil {
		if errors.Is(err, errNoAdminLeft) {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeErr(w, 500, err.Error())
		return
	}
	if body.Disabled != nil && *body.Disabled {
		s.db.DeleteUserSessions(target.ID)
	}
	s.audit(r, "user.update", target.Username, "", "")
	updated := s.db.UserByID(target.ID)
	if updated == nil {
		// Deleted by a concurrent request between the update and this read.
		writeJSON(w, 200, map[string]string{"status": "ok"})
		return
	}
	writeJSON(w, 200, toUserView(updated))
}

func (s *Server) handleUserDelete(w http.ResponseWriter, r *http.Request) {
	actor := userFrom(r)
	target := s.db.UserByID(r.PathValue("id"))
	if target == nil {
		writeErr(w, http.StatusNotFound, "no such user")
		return
	}
	if target.ID == actor.ID {
		writeErr(w, http.StatusBadRequest, "you cannot delete your own account")
		return
	}
	if !s.canManageTarget(actor, target) {
		writeErr(w, http.StatusForbidden, "only an admin can delete admin accounts")
		return
	}
	if target.IsAdmin && s.db.AdminCount() <= 1 {
		writeErr(w, http.StatusBadRequest, "cannot delete the last admin")
		return
	}
	if err := s.db.DeleteUser(target.ID); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.audit(r, "user.delete", target.Username, "", "")
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

// handleUserSetPassword lets user managers set a temporary password; the
// account is flagged must_change_pw so the person picks their own on first
// login.
func (s *Server) handleUserSetPassword(w http.ResponseWriter, r *http.Request) {
	actor := userFrom(r)
	target := s.db.UserByID(r.PathValue("id"))
	if target == nil {
		writeErr(w, http.StatusNotFound, "no such user")
		return
	}
	if !s.canManageTarget(actor, target) {
		writeErr(w, http.StatusForbidden, "only an admin can reset admin passwords")
		return
	}
	var body struct {
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
	err = s.db.UpdateUser(target.ID, func(u *store.User) error {
		u.PasswordHash = hash
		u.MustChangePW = true
		return nil
	})
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.db.DeleteUserSessions(target.ID)
	s.audit(r, "user.password_reset", target.Username, "", "")
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

func (s *Server) handleUserTOTPReset(w http.ResponseWriter, r *http.Request) {
	actor := userFrom(r)
	target := s.db.UserByID(r.PathValue("id"))
	if target == nil {
		writeErr(w, http.StatusNotFound, "no such user")
		return
	}
	if !s.canManageTarget(actor, target) {
		writeErr(w, http.StatusForbidden, "only an admin can reset admin 2FA")
		return
	}
	err := s.db.UpdateUser(target.ID, func(u *store.User) error {
		u.TOTPSecret = ""
		u.TOTPEnabled = false
		u.LastTOTPCounter = 0
		return nil
	})
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.db.DeleteUserSessions(target.ID)
	s.audit(r, "user.totp_reset", target.Username, "", "")
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

// ---- Roles ----------------------------------------------------------------

func (s *Server) handleRolesList(w http.ResponseWriter, r *http.Request) {
	// Any authenticated user may list role names (needed to render their own
	// info); only managers see them in the admin UI anyway.
	writeJSON(w, 200, s.db.Roles())
}

func (s *Server) handleRoleCreate(w http.ResponseWriter, r *http.Request) {
	actor := userFrom(r)
	var role store.Role
	if !readBody(w, r, &role, 1<<20) {
		return
	}
	role.ID = ""
	if err := s.validateRoleGrant(actor, role.Global, role.Servers); err != nil {
		writeErr(w, http.StatusForbidden, err.Error())
		return
	}
	if err := s.db.CreateRole(&role); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s.audit(r, "role.create", role.Name, "", "")
	writeJSON(w, 200, role)
}

func (s *Server) handleRoleUpdate(w http.ResponseWriter, r *http.Request) {
	actor := userFrom(r)
	var body struct {
		Name    *string                     `json:"name"`
		Global  *map[string]bool            `json:"global"`
		Servers *map[string]map[string]bool `json:"servers"`
	}
	if !readBody(w, r, &body, 1<<20) {
		return
	}
	// Validate the resulting role against the actor's own permissions.
	existing := s.db.RoleByID(r.PathValue("id"))
	if existing == nil {
		writeErr(w, http.StatusNotFound, "no such role")
		return
	}
	effGlobal := existing.Global
	if body.Global != nil {
		effGlobal = *body.Global
	}
	effServers := existing.Servers
	if body.Servers != nil {
		effServers = *body.Servers
	}
	if err := s.validateRoleGrant(actor, effGlobal, effServers); err != nil {
		writeErr(w, http.StatusForbidden, err.Error())
		return
	}
	err := s.db.UpdateRole(r.PathValue("id"), func(role *store.Role) error {
		if body.Name != nil && *body.Name != "" {
			role.Name = *body.Name
		}
		if body.Global != nil {
			role.Global = *body.Global
		}
		if body.Servers != nil {
			role.Servers = *body.Servers
		}
		return nil
	})
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s.audit(r, "role.update", r.PathValue("id"), "", "")
	writeJSON(w, 200, s.db.RoleByID(r.PathValue("id")))
}

func (s *Server) handleRoleDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.db.DeleteRole(r.PathValue("id")); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s.audit(r, "role.delete", r.PathValue("id"), "", "")
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

func (s *Server) handlePermKeys(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string][]string{
		"global": store.GlobalPermKeys,
		"server": store.ServerPermKeys,
	})
}
