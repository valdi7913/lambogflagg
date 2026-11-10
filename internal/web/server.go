// Package web contains the HTTP server: routing, middleware and handlers.
package web

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"bookclub/internal/appctx"
	"bookclub/internal/config"
	"bookclub/internal/i18n"
	"bookclub/internal/openlibrary"
	"bookclub/internal/store"
	"bookclub/internal/views"
)

//go:embed static
var staticFS embed.FS

type Server struct {
	cfg        config.Config
	st         *store.Store
	ol         *openlibrary.Client
	setupToken string
	limiter    *loginLimiter
	lastActive atomic.Int64 // unix nanos of the last visitor request

	clubMu   sync.RWMutex
	clubName string
}

func New(cfg config.Config, st *store.Store) *Server {
	s := &Server{
		cfg:     cfg,
		st:      st,
		ol:      openlibrary.New(),
		limiter: newLoginLimiter(),
	}
	i18n.Loc = cfg.Location
	views.AssetVersion = assetVersion()
	return s
}

// PrepareSetup generates a one-time setup link if no accounts exist yet.
func (s *Server) PrepareSetup(ctx context.Context) error {
	n, err := s.st.CountUsers(ctx)
	if err != nil {
		return err
	}
	if n == 0 {
		s.setupToken = randomToken(18)
		base := s.cfg.BaseURL
		if base == "" {
			base = "http://localhost" + s.cfg.Addr
		}
		slog.Warn("no accounts yet — open this link to create the first one", "url", base+"/setup?token="+s.setupToken)
	}
	return nil
}

func (s *Server) club(ctx context.Context) string {
	s.clubMu.RLock()
	name := s.clubName
	s.clubMu.RUnlock()
	if name != "" {
		return name
	}
	name = s.st.Setting(ctx, "club_name", "Book Club")
	s.clubMu.Lock()
	s.clubName = name
	s.clubMu.Unlock()
	return name
}

func (s *Server) setClubName(name string) {
	s.clubMu.Lock()
	s.clubName = name
	s.clubMu.Unlock()
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	static, _ := fs.Sub(staticFS, "static")
	fileServer := http.FileServerFS(static)
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("v") != "" || strings.HasPrefix(r.URL.Path, "fonts/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		fileServer.ServeHTTP(w, r)
	})))
	mux.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		b, _ := staticFS.ReadFile("static/favicon.png")
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.Write(b)
	})
	// Liveness only (no database), for the host's health check: checking the
	// database every few seconds would keep a scale-to-zero database awake.
	mux.HandleFunc("GET /livez", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := s.st.Pool.Ping(r.Context()); err != nil {
			http.Error(w, "db unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte("ok"))
	})

	// Public
	mux.HandleFunc("GET /lang/{lang}", s.h(s.setLanguage))
	mux.HandleFunc("GET /theme/{theme}", s.h(s.setTheme))
	mux.HandleFunc("GET /login", s.h(s.loginPage))
	mux.HandleFunc("POST /login", s.h(s.login))
	mux.HandleFunc("POST /logout", s.h(s.logout))
	mux.HandleFunc("GET /setup", s.h(s.setupPage))
	mux.HandleFunc("POST /setup", s.h(s.setup))
	mux.HandleFunc("GET /join/{token}", s.h(s.joinPage))
	mux.HandleFunc("POST /join/{token}", s.h(s.join))
	mux.HandleFunc("GET /reset/{token}", s.h(s.resetPage))
	mux.HandleFunc("POST /reset/{token}", s.h(s.reset))

	// Members only
	a := func(pattern string, fn handlerFunc) { mux.HandleFunc(pattern, s.requireUser(s.h(fn))) }

	a("GET /{$}", s.home)
	a("GET /me", s.mePage)
	a("POST /me", s.updateMe)
	a("POST /me/password", s.changePassword)

	a("GET /books", s.listBooks)
	a("GET /books/new", s.newBook)
	a("POST /books", s.createBook)
	a("GET /books/lookup", s.lookupBooks)
	a("GET /books/{id}", s.showBook)
	a("GET /books/{id}/edit", s.editBook)
	a("POST /books/{id}", s.updateBook)
	a("POST /books/{id}/wishlist", s.toggleWishlist)
	a("POST /books/{id}/delete", s.deleteBook)

	a("GET /meetings", s.listMeetings)
	a("GET /meetings/new", s.newMeeting)
	a("POST /meetings", s.createMeeting)
	a("GET /meetings/{id}", s.showMeeting)
	a("GET /meetings/{id}/edit", s.editMeeting)
	a("POST /meetings/{id}", s.updateMeeting)
	a("POST /meetings/{id}/delete", s.deleteMeeting)
	a("POST /meetings/{id}/books", s.addMeetingBook)
	a("POST /meetings/{id}/books/{bookID}/remove", s.removeMeetingBook)

	a("GET /polls", s.listPolls)
	a("GET /polls/new", s.newPoll)
	a("POST /polls", s.createPoll)
	a("GET /polls/{id}", s.showPoll)
	a("GET /polls/{id}/edit", s.editPoll)
	a("POST /polls/{id}", s.updatePoll)
	a("POST /polls/{id}/delete", s.deletePoll)
	a("POST /polls/{id}/duplicate", s.duplicatePoll)
	a("POST /polls/{id}/status", s.setPollStatus)
	a("POST /polls/{id}/options", s.nominate)
	a("POST /polls/{id}/options/{optionID}/remove", s.removeOption)
	a("POST /polls/{id}/ballot", s.castBallot)
	a("POST /polls/{id}/ballot/withdraw", s.withdrawBallot)
	a("POST /polls/{id}/decision", s.recordDecision)
	a("GET /polls/{id}/ballots.json", s.ballotsJSON)
	a("GET /polls/{id}/ballots.csv", s.ballotsCSV)

	a("POST /comments", s.createComment)
	a("GET /comments/{id}", s.showComment)
	a("GET /comments/{id}/edit", s.editComment)
	a("POST /comments/{id}", s.updateComment)
	a("POST /comments/{id}/delete", s.deleteComment)

	a("GET /members", s.listMembers)
	a("POST /invites", s.createInvite)
	a("POST /invites/{id}/revoke", s.revokeInvite)
	a("POST /members/{id}/reset-link", s.createResetLink)
	a("POST /members/{id}/admin", s.setMemberAdmin)
	a("POST /members/{id}/active", s.setMemberActive)
	a("POST /settings/club-name", s.renameClub)

	a("GET /activity", s.activity)

	mux.HandleFunc("/", s.h(func(w http.ResponseWriter, r *http.Request) error { return errNotFound }))

	var h http.Handler = mux
	h = s.csrf(h)
	h = s.loadSession(h)
	h = securityHeaders(h)
	h = s.trackActivity(h)
	h = logRequests(h)
	return h
}

// activeWindow is how long after the last visitor request the background loop
// keeps working. Outside it the loop does nothing, so an idle app doesn't keep
// a scale-to-zero database awake. Nothing is missed: a vote past its deadline
// is also closed when someone opens it, the vote list or the home page, and
// at startup.
const activeWindow = 2 * time.Minute

// trackActivity remembers when a visitor last made a request. Health checks
// and static files don't count.
func (s *Server) trackActivity(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p := r.URL.Path; p != "/livez" && p != "/healthz" && !strings.HasPrefix(p, "/static/") {
			s.lastActive.Store(time.Now().UnixNano())
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) recentlyActive() bool {
	return time.Since(time.Unix(0, s.lastActive.Load())) < activeWindow
}

// RunBackground closes votes whose deadline has passed and prunes sessions.
// It runs once at startup, then every 30 seconds while people are using the app.
func (s *Server) RunBackground(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	lastPrune := time.Time{}
	for {
		if lastPrune.IsZero() || s.recentlyActive() {
			s.closeDuePolls(ctx)
			if time.Since(lastPrune) > time.Hour {
				if err := s.st.DeleteExpiredSessions(ctx); err != nil {
					slog.Error("prune sessions", "err", err)
				}
				lastPrune = time.Now()
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// ---- Middleware ----

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: 200}
		defer func() {
			if p := recover(); p != nil {
				slog.Error("panic", "path", r.URL.Path, "panic", p)
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
			if !strings.HasPrefix(r.URL.Path, "/static/") {
				slog.Info("request", "method", r.Method, "path", r.URL.Path, "status", rec.status, "dur", time.Since(start).Round(time.Millisecond))
			}
		}()
		next.ServeHTTP(rec, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' https: data:; style-src 'self'; script-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

const sessionCookie = "bookclub_session"

func hashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

func (s *Server) loadSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/static/") {
			next.ServeHTTP(w, r)
			return
		}
		rc := &appctx.Request{Path: r.URL.Path, ClubName: s.club(r.Context()), Lang: requestLang(r), Theme: requestTheme(r)}
		if r.Method == http.MethodGet && !isHTMX(r) {
			rc.URI = r.URL.RequestURI()
		}
		if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
			if u, csrf, err := s.st.SessionUser(r.Context(), hashToken(c.Value)); err == nil {
				rc.User = u
				rc.CSRF = csrf
			}
		}
		if r.Method == http.MethodGet && !isHTMX(r) {
			rc.Flash = s.readFlash(w, r)
		}
		next.ServeHTTP(w, r.WithContext(appctx.With(r.Context(), rc)))
	})
}

const langCookie = "bookclub_lang"

// requestLang uses the language the person picked, else their browser's.
func requestLang(r *http.Request) i18n.Lang {
	if c, err := r.Cookie(langCookie); err == nil {
		if l, ok := i18n.Parse(c.Value); ok {
			return l
		}
	}
	return i18n.FromAcceptLanguage(r.Header.Get("Accept-Language"))
}

func (s *Server) setLanguage(w http.ResponseWriter, r *http.Request) error {
	l, ok := i18n.Parse(r.PathValue("lang"))
	if !ok {
		return errNotFound
	}
	http.SetCookie(w, &http.Cookie{
		Name: langCookie, Value: string(l), Path: "/", MaxAge: 400 * 24 * 3600,
		HttpOnly: true, Secure: s.cfg.CookieSecure, SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, safeNext(r.URL.Query().Get("next")), http.StatusSeeOther)
	return nil
}

// Theme cookie. It isn't HttpOnly so app.js can switch themes instantly
// without a page load; the value is only ever one of the names below.
const themeCookie = "bookclub_theme"

var themes = map[string]bool{"light": true, "dark": true, "parchment": true}

// requestTheme is the theme the person picked, or "" to follow the device.
func requestTheme(r *http.Request) string {
	if c, err := r.Cookie(themeCookie); err == nil && themes[c.Value] {
		return c.Value
	}
	return ""
}

func (s *Server) setTheme(w http.ResponseWriter, r *http.Request) error {
	name := r.PathValue("theme")
	switch {
	case name == "auto":
		http.SetCookie(w, &http.Cookie{Name: themeCookie, Value: "", Path: "/", MaxAge: -1, SameSite: http.SameSiteLaxMode})
	case themes[name]:
		http.SetCookie(w, &http.Cookie{
			Name: themeCookie, Value: name, Path: "/", MaxAge: 400 * 24 * 3600,
			Secure: s.cfg.CookieSecure, SameSite: http.SameSiteLaxMode,
		})
	default:
		return errNotFound
	}
	http.Redirect(w, r, safeNext(r.URL.Query().Get("next")), http.StatusSeeOther)
	return nil
}

// csrf rejects cross-site form posts: the Origin header (sent by browsers on
// POST) must match, and logged-in users must present their session's token.
func (s *Server) csrf(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && origin != "null" {
			if u, err := url.Parse(origin); err != nil || u.Host != r.Host {
				http.Error(w, tr(r, "Cross-site request blocked."), http.StatusForbidden)
				return
			}
		}
		rc := appctx.Get(r.Context())
		if rc.User != nil {
			token := r.Header.Get("X-CSRF-Token")
			if token == "" {
				token = r.FormValue("csrf")
			}
			if token == "" || token != rc.CSRF {
				http.Error(w, tr(r, "Your session has expired or the form is stale. Reload the page and try again."), http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) requireUser(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if user(r) == nil {
			if isHTMX(r) {
				w.Header().Set("HX-Redirect", "/login")
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			n, _ := s.st.CountUsers(r.Context())
			if n == 0 {
				http.Redirect(w, r, "/setup", http.StatusSeeOther)
				return
			}
			target := "/login"
			if r.Method == http.MethodGet && r.URL.Path != "/" {
				target += "?next=" + url.QueryEscape(r.URL.RequestURI())
			}
			http.Redirect(w, r, target, http.StatusSeeOther)
			return
		}
		next(w, r)
	}
}

func assetVersion() string {
	h := sha256.New()
	var paths []string
	fs.WalkDir(staticFS, "static", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			paths = append(paths, p)
		}
		return nil
	})
	sort.Strings(paths)
	for _, p := range paths {
		b, _ := staticFS.ReadFile(p)
		h.Write([]byte(p))
		h.Write(b)
	}
	return hex.EncodeToString(h.Sum(nil))[:10]
}
