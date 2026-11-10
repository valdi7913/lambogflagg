package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"bookclub/internal/config"
)

func TestClientIP(t *testing.T) {
	tests := []struct {
		name   string
		header string // configured CLIENT_IP_HEADER
		remote string
		sent   map[string]string
		want   string
	}{
		{"direct ipv4", "", "203.0.113.7:51234", nil, "203.0.113.7"},
		{"direct ipv6", "", "[2001:db8::1]:51234", nil, "2001:db8::1"},
		{"header ignored unless configured", "", "10.0.0.1:1", map[string]string{"Fly-Client-IP": "198.51.100.9"}, "10.0.0.1"},
		{"configured header wins", "Fly-Client-IP", "10.0.0.1:1", map[string]string{"Fly-Client-IP": "198.51.100.9"}, "198.51.100.9"},
		{"configured header ipv6", "Fly-Client-IP", "10.0.0.1:1", map[string]string{"Fly-Client-IP": " 2001:db8::2 "}, "2001:db8::2"},
		{"missing header falls back", "Fly-Client-IP", "10.0.0.1:1", nil, "10.0.0.1"},
		{"garbage header falls back", "Fly-Client-IP", "10.0.0.1:1", map[string]string{"Fly-Client-IP": "not-an-ip"}, "10.0.0.1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := &Server{cfg: config.Config{ClientIPHeader: tc.header}}
			r := httptest.NewRequest("POST", "/login", nil)
			r.RemoteAddr = tc.remote
			for k, v := range tc.sent {
				r.Header.Set(k, v)
			}
			if got := s.clientIP(r); got != tc.want {
				t.Errorf("clientIP = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestActivityTracking(t *testing.T) {
	s := &Server{}
	if s.recentlyActive() {
		t.Fatal("a fresh server should not count as active")
	}
	okHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})

	for _, path := range []string{"/livez", "/healthz", "/static/css/app.css"} {
		s.trackActivity(okHandler).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", path, nil))
		if s.recentlyActive() {
			t.Fatalf("%s must not count as visitor activity", path)
		}
	}
	s.trackActivity(okHandler).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/books", nil))
	if !s.recentlyActive() {
		t.Fatal("a page request should count as activity")
	}
	s.lastActive.Store(time.Now().Add(-activeWindow - time.Second).UnixNano())
	if s.recentlyActive() {
		t.Fatal("activity should expire after the window")
	}
}
