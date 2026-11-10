package web

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/a-h/templ"

	"bookclub/internal/appctx"
	"bookclub/internal/i18n"
	"bookclub/internal/store"
	"bookclub/internal/views"
	"bookclub/internal/voting"
)

// httpError is an error with a status code and a message safe to show users.
// msg is an English format string, translated when shown.
type httpError struct {
	status int
	msg    string
	args   []any
}

func (e httpError) Error() string { return fmt.Sprintf(e.msg, e.args...) }

func errStatus(status int, msg string, args ...any) error { return httpError{status, msg, args} }

// ---- Translation helpers ----

func lang(r *http.Request) i18n.Lang { return appctx.Get(r.Context()).Lang }

func tr(r *http.Request, key string) string { return i18n.T(lang(r), key) }

func trf(r *http.Request, key string, args ...any) string { return i18n.Tf(lang(r), key, args...) }

// trKey marks a string as a translation key that's translated later.
func trKey(key string) string { return key }

// errText shows an error in the user's language when it's one of ours.
func errText(r *http.Request, err error) string {
	var he httpError
	if errors.As(err, &he) {
		return i18n.Tf(lang(r), he.msg, he.args...)
	}
	var re *voting.RuleError
	if errors.As(err, &re) {
		return i18n.Tf(lang(r), re.Format, re.Args...)
	}
	return err.Error()
}

var (
	errNotFound  = errStatus(http.StatusNotFound, "That page doesn't exist (or was deleted).")
	errForbidden = errStatus(http.StatusForbidden, "You're not allowed to do that.")
)

type handlerFunc func(w http.ResponseWriter, r *http.Request) error

var timeNow = time.Now

// h adapts a handler that returns an error into an http.HandlerFunc.
func (s *Server) h(fn handlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		err := fn(w, r)
		if err == nil {
			return
		}
		var he httpError
		switch {
		case errors.As(err, &he):
		case errors.Is(err, store.ErrNotFound):
			he = errNotFound.(httpError)
		default:
			slog.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
			he = errStatus(http.StatusInternalServerError, "Something went wrong on our side. Please try again.").(httpError)
		}
		msg := errText(r, he)
		if isHTMX(r) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(he.status)
			fmt.Fprint(w, msg)
			return
		}
		render(w, r, he.status, views.ErrorPage(he.status, msg))
	}
}

func render(w http.ResponseWriter, r *http.Request, status int, c templ.Component) error {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	return c.Render(r.Context(), w)
}

func ok(w http.ResponseWriter, r *http.Request, c templ.Component) error {
	return render(w, r, http.StatusOK, c)
}

func isHTMX(r *http.Request) bool { return r.Header.Get("HX-Request") == "true" }

// redirect sends the browser to a new page after a form post.
func redirect(w http.ResponseWriter, r *http.Request, to string) error {
	if isHTMX(r) {
		w.Header().Set("HX-Redirect", to)
		w.WriteHeader(http.StatusOK)
		return nil
	}
	http.Redirect(w, r, to, http.StatusSeeOther)
	return nil
}

const flashCookie = "bookclub_flash"

// flash shows a one-off message on the next page. key is an English format
// string, translated into the user's language here.
func (s *Server) flash(w http.ResponseWriter, r *http.Request, kind, key string, args ...any) {
	msg := trf(r, key, args...)
	http.SetCookie(w, &http.Cookie{
		Name: flashCookie, Value: url.QueryEscape(kind + "|" + msg), Path: "/",
		HttpOnly: true, Secure: s.cfg.CookieSecure, SameSite: http.SameSiteLaxMode, MaxAge: 60,
	})
}

func (s *Server) readFlash(w http.ResponseWriter, r *http.Request) *appctx.Flash {
	c, err := r.Cookie(flashCookie)
	if err != nil {
		return nil
	}
	http.SetCookie(w, &http.Cookie{Name: flashCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: s.cfg.CookieSecure, SameSite: http.SameSiteLaxMode})
	v, err := url.QueryUnescape(c.Value)
	if err != nil {
		return nil
	}
	kind, msg, found := strings.Cut(v, "|")
	if !found || msg == "" {
		return nil
	}
	return &appctx.Flash{Kind: kind, Message: msg}
}

func user(r *http.Request) *store.User { return appctx.Get(r.Context()).User }

func pathID(r *http.Request, name string) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	if err != nil || id <= 0 {
		return 0, errNotFound
	}
	return id, nil
}

func formStr(r *http.Request, name string) string { return strings.TrimSpace(r.FormValue(name)) }

// formInt parses a non-negative integer; blank or invalid gives 0.
func formInt(r *http.Request, name string) int {
	n, err := strconv.Atoi(formStr(r, name))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

func formInt64(r *http.Request, name string) int64 {
	n, err := strconv.ParseInt(formStr(r, name), 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

func formBool(r *http.Request, name string) bool {
	v := r.FormValue(name)
	return v == "1" || v == "on" || v == "true"
}

func optionalID(n int64) *int64 {
	if n == 0 {
		return nil
	}
	return &n
}

func randomToken(nBytes int) string {
	b := make([]byte, nBytes)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// parseLocalTime parses <input type="datetime-local"> in the club's time zone.
func (s *Server) parseLocalTime(v string) (*time.Time, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil, nil
	}
	for _, layout := range []string{"2006-01-02T15:04", "2006-01-02T15:04:05", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, v, s.cfg.Location); err == nil {
			return &t, nil
		}
	}
	return nil, fmt.Errorf("not a valid date and time")
}

func validHTTPURL(v string) bool {
	if v == "" {
		return true
	}
	u, err := url.Parse(v)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

func (s *Server) baseURL(r *http.Request) string {
	if s.cfg.BaseURL != "" {
		return s.cfg.BaseURL
	}
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// clientIP returns the address of the person making the request, used to
// rate-limit logins. Behind a reverse proxy every connection comes from the
// proxy, so when CLIENT_IP_HEADER is configured the address is read from that
// header instead (only set it for a proxy you trust to overwrite the header).
func (s *Server) clientIP(r *http.Request) string {
	if h := s.cfg.ClientIPHeader; h != "" {
		if ip, err := netip.ParseAddr(strings.TrimSpace(r.Header.Get(h))); err == nil {
			return ip.String()
		}
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// meetingArg names a meeting in a history message.
func meetingArg(m *store.Meeting) string { return i18n.ArgMeeting(m.Title, m.StartsAt) }

func timeStr(t *time.Time, loc *time.Location) string {
	if t == nil {
		return ""
	}
	return t.In(loc).Format("2006-01-02 15:04")
}

func ptrIntStr(p *int) string {
	if p == nil {
		return ""
	}
	return strconv.Itoa(*p)
}

func boolStr(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
