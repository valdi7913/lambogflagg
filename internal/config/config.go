// Package config reads settings from environment variables (and an optional
// .env file for local development).
package config

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"time"
)

type Config struct {
	DatabaseURL  string
	Addr         string
	BaseURL      string // e.g. https://club.example.com — used to build invite links
	CookieSecure bool
	Location     *time.Location

	// ClientIPHeader names a request header that carries the real client IP,
	// set by a trusted reverse proxy (Fly.io: "Fly-Client-IP"). Leave empty when
	// the app is reachable directly, otherwise clients could spoof it.
	ClientIPHeader string
}

// Load reads configuration. Values already present in the environment win
// over values from the .env file.
func Load() (Config, error) {
	loadDotEnv(".env")

	c := Config{
		DatabaseURL: os.Getenv("DATABASE_URL"),
		Addr:        os.Getenv("ADDR"),
		BaseURL:     strings.TrimRight(os.Getenv("BASE_URL"), "/"),

		ClientIPHeader: strings.TrimSpace(os.Getenv("CLIENT_IP_HEADER")),
	}
	if c.DatabaseURL == "" {
		return c, fmt.Errorf("DATABASE_URL is not set (see .env.example)")
	}
	if c.Addr == "" {
		if port := os.Getenv("PORT"); port != "" { // common on PaaS hosts
			c.Addr = ":" + port
		} else {
			c.Addr = ":8080"
		}
	}

	switch strings.ToLower(os.Getenv("COOKIE_SECURE")) {
	case "1", "true", "yes":
		c.CookieSecure = true
	case "0", "false", "no":
		c.CookieSecure = false
	default:
		c.CookieSecure = strings.HasPrefix(c.BaseURL, "https://")
	}

	c.Location = time.Local
	if tz := os.Getenv("TIMEZONE"); tz != "" {
		loc, err := time.LoadLocation(tz)
		if err != nil {
			return c, fmt.Errorf("TIMEZONE %q: %w", tz, err)
		}
		c.Location = loc
	}
	return c, nil
}

func loadDotEnv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	first := true
	for sc.Scan() {
		line := sc.Text()
		if first {
			line = strings.TrimPrefix(line, "\xef\xbb\xbf") // tolerate a UTF-8 BOM
			first = false
		}
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.Trim(strings.TrimSpace(val), `"'`)
		if _, exists := os.LookupEnv(key); !exists {
			os.Setenv(key, val)
		}
	}
}
