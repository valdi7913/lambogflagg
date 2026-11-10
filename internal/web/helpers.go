package web

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/a-h/templ"

	"bookclub/internal/appctx"
	"bookclub/internal/store"
	"bookclub/internal/views"
)

// httpError is an error with a status code and a message safe to show users.
type httpError struct {
	status int
	msg    string
}

func (e httpError) Error() string { return e.msg }

func errStatus(status int, msg string) error { return httpError{status, msg} }

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
			he = httpError{http.StatusInternalServerError, "Something went wrong on our side. Please try again."}
		}
		if isHTMX(r) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(he.status)
			fmt.Fprint(w, he.msg)
			return
		}
		render(w, r, he.status, views.ErrorPage(he.status, he.msg))
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

func (s *Server) flash(w http.ResponseWriter, kind, msg string) {
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

func clientIP(r *http.Request) string {
	host := r.RemoteAddr
	if i := strings.LastIndex(host, ":"); i > 0 {
		host = host[:i]
	}
	return host
}

func quote(s string) string { return "“" + s + "”" }

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
