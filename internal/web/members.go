package web

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"bookclub/internal/store"
	"bookclub/internal/views"
)

const (
	inviteLifetime = 14 * 24 * time.Hour
	resetLifetime  = 48 * time.Hour
)

func (s *Server) membersData(r *http.Request) (views.MembersData, error) {
	d := views.MembersData{BaseURL: s.baseURL(r)}
	var err error
	if d.Members, err = s.st.ListUsers(r.Context()); err != nil {
		return d, err
	}
	d.Invites, err = s.st.ListInvites(r.Context())
	return d, err
}

func (s *Server) listMembers(w http.ResponseWriter, r *http.Request) error {
	d, err := s.membersData(r)
	if err != nil {
		return err
	}
	// Highlight a just-created invite.
	if id, _ := strconv.ParseInt(r.URL.Query().Get("new"), 10, 64); id > 0 {
		if inv, err := s.st.InviteByID(r.Context(), id); err == nil && inv.Usable() {
			d.NewLink = d.BaseURL + "/join/" + inv.Token
			d.NewLinkLabel = tr(r, "New invite link")
			if inv.Note != "" {
				d.NewLinkLabel = trf(r, "New invite link for %s", inv.Note)
			}
		}
	}
	return ok(w, r, views.MembersPage(d))
}

func (s *Server) createInvite(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	u := user(r)
	inv := &store.Invite{Token: randomToken(18), Note: formStr(r, "note"), CreatedBy: &u.ID, ExpiresAt: time.Now().Add(inviteLifetime)}
	if len([]rune(inv.Note)) > 100 {
		inv.Note = string([]rune(inv.Note)[:100])
	}
	err := s.st.Tx(ctx, func(q store.Querier) error {
		if err := s.st.CreateInvite(ctx, q, inv); err != nil {
			return err
		}
		msg := store.M("created an invite link")
		if inv.Note != "" {
			msg = store.M("created an invite link for “%s”", inv.Note)
		}
		return s.st.Log(ctx, q, u.ID, "create", "user", inv.ID, msg, nil)
	})
	if err != nil {
		return err
	}
	return redirect(w, r, fmt.Sprintf("/members?new=%d", inv.ID))
}

func (s *Server) revokeInvite(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	inv, err := s.st.InviteByID(ctx, id)
	if err != nil {
		return err
	}
	err = s.st.Tx(ctx, func(q store.Querier) error {
		if err := s.st.RevokeInvite(ctx, q, id); err != nil {
			return err
		}
		msg := store.M("revoked an invite link")
		if inv.Note != "" {
			msg = store.M("revoked an invite link for “%s”", inv.Note)
		}
		return s.st.Log(ctx, q, user(r).ID, "update", "user", id, msg, nil)
	})
	if err != nil {
		return err
	}
	s.flash(w, r, "ok", "Invite revoked.")
	return redirect(w, r, "/members")
}

func (s *Server) createResetLink(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	u := user(r)
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	target, err := s.st.UserByID(ctx, id)
	if err != nil {
		return err
	}
	if !target.Active() {
		return errStatus(http.StatusConflict, "That account is deactivated.")
	}
	rs := &store.PasswordReset{Token: randomToken(18), UserID: id, CreatedBy: &u.ID, ExpiresAt: time.Now().Add(resetLifetime)}
	err = s.st.Tx(ctx, func(q store.Querier) error {
		if err := s.st.CreateReset(ctx, q, rs); err != nil {
			return err
		}
		return s.st.Log(ctx, q, u.ID, "update", "user", id, store.M("created a password reset link for %s", target.DisplayName), nil)
	})
	if err != nil {
		return err
	}
	d, err := s.membersData(r)
	if err != nil {
		return err
	}
	d.NewLink = d.BaseURL + "/reset/" + rs.Token
	d.NewLinkLabel = trf(r, "Password reset link for %s (valid 48 hours)", target.DisplayName)
	return ok(w, r, views.MembersPage(d))
}

func (s *Server) requireAdmin(r *http.Request) error {
	if !user(r).IsAdmin {
		return errForbidden
	}
	return nil
}

func (s *Server) setMemberAdmin(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	if err := s.requireAdmin(r); err != nil {
		return err
	}
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	if id == user(r).ID {
		return errStatus(http.StatusConflict, "You can't change your own admin status.")
	}
	target, err := s.st.UserByID(ctx, id)
	if err != nil {
		return err
	}
	admin := formBool(r, "admin")
	err = s.st.Tx(ctx, func(q store.Querier) error {
		if err := s.st.SetAdmin(ctx, q, id, admin); err != nil {
			return err
		}
		msg := store.M("made %s an admin", target.DisplayName)
		if !admin {
			msg = store.M("removed admin rights from %s", target.DisplayName)
		}
		return s.st.Log(ctx, q, user(r).ID, "update", "user", id, msg, nil)
	})
	if err != nil {
		return err
	}
	return redirect(w, r, "/members")
}

func (s *Server) setMemberActive(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	if err := s.requireAdmin(r); err != nil {
		return err
	}
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	if id == user(r).ID {
		return errStatus(http.StatusConflict, "You can't deactivate yourself.")
	}
	target, err := s.st.UserByID(ctx, id)
	if err != nil {
		return err
	}
	active := formBool(r, "active")
	err = s.st.Tx(ctx, func(q store.Querier) error {
		if err := s.st.SetDeactivated(ctx, q, id, !active); err != nil {
			return err
		}
		if !active && target.IsAdmin {
			if err := s.st.SetAdmin(ctx, q, id, false); err != nil {
				return err
			}
		}
		msg := store.M("reactivated %s", target.DisplayName)
		if !active {
			msg = store.M("deactivated %s", target.DisplayName)
		}
		return s.st.Log(ctx, q, user(r).ID, "update", "user", id, msg, nil)
	})
	if err != nil {
		return err
	}
	return redirect(w, r, "/members")
}

func (s *Server) renameClub(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	name := formStr(r, "club_name")
	if name == "" || len([]rune(name)) > 80 {
		s.flash(w, r, "error", "Enter a club name (up to 80 characters).")
		return redirect(w, r, "/members")
	}
	old := s.club(ctx)
	if name != old {
		err := s.st.Tx(ctx, func(q store.Querier) error {
			if err := s.st.SetSetting(ctx, q, "club_name", name); err != nil {
				return err
			}
			return s.st.Log(ctx, q, user(r).ID, "update", "club", 0, store.M("renamed the club"),
				store.Diff([][3]string{{"name", old, name}}))
		})
		if err != nil {
			return err
		}
		s.setClubName(name)
	}
	s.flash(w, r, "ok", "Club renamed.")
	return redirect(w, r, "/members")
}
