package web

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"bookclub/internal/store"
	"bookclub/internal/views"
)

const sessionLifetime = 60 * 24 * time.Hour

var usernameRe = regexp.MustCompile(`^[A-Za-z0-9_.\-]{2,40}$`)

func hashPassword(pw string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	return string(b), err
}

func checkPassword(hash, pw string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw)) == nil
}

// A dummy hash so failed logins for unknown usernames take as long as real ones.
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("not-a-real-password"), bcrypt.DefaultCost)

func validatePassword(r *http.Request, errs map[string]string) string {
	pw := r.FormValue("password")
	switch {
	case len(pw) < 8:
		errs["password"] = "Use at least 8 characters."
	case len(pw) > 72:
		errs["password"] = "That's too long (max 72 characters)."
	case pw != r.FormValue("password2"):
		errs["password2"] = "The passwords don't match."
	}
	return pw
}

func (s *Server) readAccountForm(ctx context.Context, r *http.Request) (views.AccountForm, string) {
	f := views.AccountForm{
		DisplayName: formStr(r, "display_name"),
		Username:    formStr(r, "username"),
		Errors:      map[string]string{},
	}
	if f.DisplayName == "" || len([]rune(f.DisplayName)) > 60 {
		f.Errors["display_name"] = "Enter a name (up to 60 characters)."
	}
	if !usernameRe.MatchString(f.Username) {
		f.Errors["username"] = "2–40 letters, numbers, dots, dashes or underscores."
	} else if _, err := s.st.UserByUsername(ctx, f.Username); err == nil {
		f.Errors["username"] = "That username is taken."
	}
	pw := validatePassword(r, f.Errors)
	return f, pw
}

func (s *Server) startSession(w http.ResponseWriter, r *http.Request, userID int64) error {
	token := randomToken(32)
	if err := s.st.CreateSession(r.Context(), userID, hashToken(token), randomToken(24), time.Now().Add(sessionLifetime)); err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: token, Path: "/", HttpOnly: true, Secure: s.cfg.CookieSecure,
		SameSite: http.SameSiteLaxMode, MaxAge: int(sessionLifetime.Seconds()),
	})
	return nil
}

func safeNext(next string) string {
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.HasPrefix(next, "/\\") {
		return "/"
	}
	return next
}

// ---- Login / logout ----

func (s *Server) loginPage(w http.ResponseWriter, r *http.Request) error {
	if user(r) != nil {
		return redirect(w, r, "/")
	}
	if n, err := s.st.CountUsers(r.Context()); err == nil && n == 0 {
		return redirect(w, r, "/setup")
	}
	return ok(w, r, views.LoginPage("", r.URL.Query().Get("next"), ""))
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) error {
	username := formStr(r, "username")
	next := r.FormValue("next")
	key := clientIP(r)
	if s.limiter.blocked(key) {
		return render(w, r, http.StatusTooManyRequests, views.LoginPage(username, next, "Too many failed attempts. Wait 15 minutes and try again."))
	}
	u, err := s.st.UserByUsername(r.Context(), username)
	if err != nil {
		bcrypt.CompareHashAndPassword(dummyHash, []byte(r.FormValue("password")))
	}
	if err != nil || !checkPassword(u.PasswordHash, r.FormValue("password")) || !u.Active() {
		s.limiter.fail(key)
		msg := "Wrong username or password."
		if err == nil && !u.Active() && checkPassword(u.PasswordHash, r.FormValue("password")) {
			msg = "This account has been deactivated."
		}
		return render(w, r, http.StatusUnauthorized, views.LoginPage(username, next, msg))
	}
	s.limiter.reset(key)
	if err := s.startSession(w, r, u.ID); err != nil {
		return err
	}
	return redirect(w, r, safeNext(next))
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) error {
	if c, err := r.Cookie(sessionCookie); err == nil {
		_ = s.st.DeleteSession(r.Context(), hashToken(c.Value))
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: s.cfg.CookieSecure})
	return redirect(w, r, "/login")
}

// ---- First-run setup ----

func (s *Server) setupAllowed(ctx context.Context, token string) bool {
	if s.setupToken == "" || subtle.ConstantTimeCompare([]byte(token), []byte(s.setupToken)) != 1 {
		return false
	}
	n, err := s.st.CountUsers(ctx)
	return err == nil && n == 0
}

func (s *Server) setupPage(w http.ResponseWriter, r *http.Request) error {
	if n, _ := s.st.CountUsers(r.Context()); n > 0 {
		return redirect(w, r, "/login")
	}
	token := r.URL.Query().Get("token")
	return ok(w, r, views.SetupPage(token, s.setupAllowed(r.Context(), token), views.AccountForm{Errors: map[string]string{}}))
}

func (s *Server) setup(w http.ResponseWriter, r *http.Request) error {
	token := r.FormValue("token")
	if !s.setupAllowed(r.Context(), token) {
		return errStatus(http.StatusForbidden, "This setup link isn't valid. Check the server log for the current one.")
	}
	f, pw := s.readAccountForm(r.Context(), r)
	clubName := formStr(r, "club_name")
	if clubName == "" {
		clubName = "Book Club"
	}
	if len(f.Errors) > 0 {
		return render(w, r, http.StatusUnprocessableEntity, views.SetupPage(token, true, f))
	}
	hash, err := hashPassword(pw)
	if err != nil {
		return err
	}
	u := &store.User{Username: f.Username, DisplayName: f.DisplayName, PasswordHash: hash, IsAdmin: true}
	err = s.st.Tx(r.Context(), func(q store.Querier) error {
		if err := s.st.CreateUser(r.Context(), q, u); err != nil {
			return err
		}
		if err := s.st.SetSetting(r.Context(), q, "club_name", clubName); err != nil {
			return err
		}
		return s.st.Log(r.Context(), q, u.ID, "create", "user", u.ID, "set up the club and created the first account", nil)
	})
	if err != nil {
		return err
	}
	s.setupToken = ""
	s.setClubName(clubName)
	if err := s.startSession(w, r, u.ID); err != nil {
		return err
	}
	s.flash(w, "ok", "Welcome! Invite your friends from the Members page.")
	return redirect(w, r, "/members")
}

// ---- Joining with an invite ----

func (s *Server) joinPage(w http.ResponseWriter, r *http.Request) error {
	token := r.PathValue("token")
	inv, _ := s.st.InviteByToken(r.Context(), s.st.Pool, token, false)
	return ok(w, r, views.JoinPage(token, inv, views.AccountForm{Errors: map[string]string{}}))
}

func (s *Server) join(w http.ResponseWriter, r *http.Request) error {
	token := r.PathValue("token")
	inv, err := s.st.InviteByToken(r.Context(), s.st.Pool, token, false)
	if err != nil || !inv.Usable() {
		return ok(w, r, views.JoinPage(token, nil, views.AccountForm{}))
	}
	f, pw := s.readAccountForm(r.Context(), r)
	if len(f.Errors) > 0 {
		return render(w, r, http.StatusUnprocessableEntity, views.JoinPage(token, inv, f))
	}
	hash, err := hashPassword(pw)
	if err != nil {
		return err
	}
	u := &store.User{Username: f.Username, DisplayName: f.DisplayName, PasswordHash: hash, InvitedBy: inv.CreatedBy}
	err = s.st.Tx(r.Context(), func(q store.Querier) error {
		locked, err := s.st.InviteByToken(r.Context(), q, token, true)
		if err != nil || !locked.Usable() {
			return errStatus(http.StatusConflict, "This invite was just used or revoked.")
		}
		if err := s.st.CreateUser(r.Context(), q, u); err != nil {
			if errors.Is(err, store.ErrDuplicate) {
				return errStatus(http.StatusConflict, "That username is taken.")
			}
			return err
		}
		if err := s.st.UseInvite(r.Context(), q, locked.ID, u.ID); err != nil {
			return err
		}
		return s.st.Log(r.Context(), q, u.ID, "create", "user", u.ID, "joined the club", nil)
	})
	if err != nil {
		return err
	}
	if err := s.startSession(w, r, u.ID); err != nil {
		return err
	}
	s.flash(w, "ok", "Welcome to "+s.club(r.Context())+"!")
	return redirect(w, r, "/")
}

// ---- Password reset links ----

func (s *Server) resetPage(w http.ResponseWriter, r *http.Request) error {
	token := r.PathValue("token")
	var target *store.User
	valid := false
	err := s.st.Tx(r.Context(), func(q store.Querier) error {
		rs, err := s.st.ResetByToken(r.Context(), q, token)
		if err != nil || rs.UsedAt != nil || time.Now().After(rs.ExpiresAt) {
			return nil
		}
		target, err = s.st.UserByID(r.Context(), rs.UserID)
		valid = err == nil && target.Active()
		return nil
	})
	if err != nil {
		return err
	}
	return ok(w, r, views.ResetPage(token, target, valid, map[string]string{}))
}

func (s *Server) reset(w http.ResponseWriter, r *http.Request) error {
	token := r.PathValue("token")
	errs := map[string]string{}
	pw := validatePassword(r, errs)
	var target *store.User
	err := s.st.Tx(r.Context(), func(q store.Querier) error {
		rs, err := s.st.ResetByToken(r.Context(), q, token)
		if err != nil || rs.UsedAt != nil || time.Now().After(rs.ExpiresAt) {
			return errStatus(http.StatusGone, "This reset link has expired or was already used.")
		}
		target, err = s.st.UserByID(r.Context(), rs.UserID)
		if err != nil || !target.Active() {
			return errStatus(http.StatusGone, "This account isn't active.")
		}
		if len(errs) > 0 {
			return nil
		}
		hash, err := hashPassword(pw)
		if err != nil {
			return err
		}
		if err := s.st.UpdatePassword(r.Context(), q, target.ID, hash); err != nil {
			return err
		}
		if err := s.st.UseReset(r.Context(), q, rs.ID); err != nil {
			return err
		}
		if err := s.st.DeleteUserSessions(r.Context(), q, target.ID); err != nil {
			return err
		}
		return s.st.Log(r.Context(), q, target.ID, "update", "user", target.ID, "set a new password with a reset link", nil)
	})
	if err != nil {
		return err
	}
	if len(errs) > 0 {
		return render(w, r, http.StatusUnprocessableEntity, views.ResetPage(token, target, true, errs))
	}
	if err := s.startSession(w, r, target.ID); err != nil {
		return err
	}
	s.flash(w, "ok", "Password changed. You're logged in.")
	return redirect(w, r, "/")
}

// ---- Own account ----

func (s *Server) mePage(w http.ResponseWriter, r *http.Request) error {
	return ok(w, r, views.MePage(user(r), "", map[string]string{}))
}

func (s *Server) updateMe(w http.ResponseWriter, r *http.Request) error {
	u := user(r)
	name := formStr(r, "display_name")
	if name == "" || len([]rune(name)) > 60 {
		return render(w, r, http.StatusUnprocessableEntity, views.MePage(u, "Enter a name (up to 60 characters).", map[string]string{}))
	}
	if name != u.DisplayName {
		err := s.st.Tx(r.Context(), func(q store.Querier) error {
			if err := s.st.UpdateDisplayName(r.Context(), q, u.ID, name); err != nil {
				return err
			}
			return s.st.Log(r.Context(), q, u.ID, "update", "user", u.ID, "changed their name",
				store.Diff([][3]string{{"name", u.DisplayName, name}}))
		})
		if err != nil {
			return err
		}
	}
	s.flash(w, "ok", "Saved.")
	return redirect(w, r, "/me")
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) error {
	u := user(r)
	errs := map[string]string{}
	if !checkPassword(u.PasswordHash, r.FormValue("current")) {
		errs["current"] = "That's not your current password."
	}
	pw := validatePassword(r, errs)
	if len(errs) > 0 {
		return render(w, r, http.StatusUnprocessableEntity, views.MePage(u, "", errs))
	}
	hash, err := hashPassword(pw)
	if err != nil {
		return err
	}
	err = s.st.Tx(r.Context(), func(q store.Querier) error {
		if err := s.st.UpdatePassword(r.Context(), q, u.ID, hash); err != nil {
			return err
		}
		// Log out other devices, then start a fresh session here.
		return s.st.DeleteUserSessions(r.Context(), q, u.ID)
	})
	if err != nil {
		return err
	}
	if err := s.startSession(w, r, u.ID); err != nil {
		return err
	}
	s.flash(w, "ok", "Password changed. Other devices have been logged out.")
	return redirect(w, r, "/me")
}

// ---- Login rate limiting ----

type loginLimiter struct {
	mu   sync.Mutex
	hits map[string][]time.Time
}

func newLoginLimiter() *loginLimiter { return &loginLimiter{hits: map[string][]time.Time{}} }

const (
	limiterWindow = 15 * time.Minute
	limiterMax    = 10
)

func (l *loginLimiter) recent(key string) []time.Time {
	cut := time.Now().Add(-limiterWindow)
	var keep []time.Time
	for _, t := range l.hits[key] {
		if t.After(cut) {
			keep = append(keep, t)
		}
	}
	if len(keep) == 0 {
		delete(l.hits, key)
	} else {
		l.hits[key] = keep
	}
	return keep
}

func (l *loginLimiter) blocked(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.recent(key)) >= limiterMax
}

func (l *loginLimiter) fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.hits[key] = append(l.recent(key), time.Now())
}

func (l *loginLimiter) reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.hits, key)
}
