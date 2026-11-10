// Package appctx carries per-request data (current user, language, CSRF
// token, flash message) from the HTTP layer to the templates.
package appctx

import (
	"context"

	"bookclub/internal/i18n"
	"bookclub/internal/store"
)

type Flash struct {
	Kind    string // "ok" or "error"
	Message string
}

type Request struct {
	User     *store.User
	Lang     i18n.Lang
	Theme    string // "light", "dark", "parchment", or "" to follow the device
	CSRF     string
	Flash    *Flash
	Path     string
	URI      string // path and query of a GET request, for "come back here" links
	ClubName string
}

type key struct{}

func With(ctx context.Context, r *Request) context.Context {
	return context.WithValue(ctx, key{}, r)
}

// Get never returns nil.
func Get(ctx context.Context) *Request {
	if r, ok := ctx.Value(key{}).(*Request); ok && r != nil {
		return r
	}
	return &Request{ClubName: "Book Club", Lang: i18n.EN}
}
