package views

import (
	"context"
	"encoding/json"
	"fmt"
	neturl "net/url"
	"strconv"
	"time"

	"github.com/a-h/templ"

	"bookclub/internal/appctx"
	"bookclub/internal/i18n"
	"bookclub/internal/openlibrary"
	"bookclub/internal/store"
	"bookclub/internal/voting"
)

// AssetVersion is appended to static asset URLs for cache busting.
var AssetVersion = "1"

// allowEval is off so htmx never needs 'unsafe-eval' under our CSP.
const htmxConfig = `{"includeIndicatorStyles":false,"historyCacheSize":0,"allowEval":false}`

func req(ctx context.Context) *appctx.Request     { return appctx.Get(ctx) }
func currentUser(ctx context.Context) *store.User { return appctx.Get(ctx).User }
func clubName(ctx context.Context) string         { return appctx.Get(ctx).ClubName }

func myID(ctx context.Context) int64 {
	if u := currentUser(ctx); u != nil {
		return u.ID
	}
	return 0
}

func isMe(ctx context.Context, id *int64) bool {
	return id != nil && *id == myID(ctx)
}

func csrfHeaders(ctx context.Context) string {
	b, _ := json.Marshal(map[string]string{"X-CSRF-Token": appctx.Get(ctx).CSRF})
	return string(b)
}

func asset(path string) string { return path + "?v=" + AssetVersion }

func itoa[T ~int | ~int64](n T) string { return strconv.FormatInt(int64(n), 10) }

func url(format string, args ...any) templ.SafeURL {
	return templ.SafeURL(fmt.Sprintf(format, args...))
}

func path(format string, args ...any) string { return fmt.Sprintf(format, args...) }

// ---- Translation ----

func lang(ctx context.Context) i18n.Lang { return appctx.Get(ctx).Lang }

// t translates a phrase into the reader's language.
func t(ctx context.Context, key string) string { return i18n.T(lang(ctx), key) }

// tf translates a format string and fills it in.
func tf(ctx context.Context, key string, args ...any) string { return i18n.Tf(lang(ctx), key, args...) }

// tn picks singular/plural; both forms contain %d.
func tn(ctx context.Context, n int, one, many string) string { return i18n.N(lang(ctx), n, one, many) }

func q(ctx context.Context, s string) string { return i18n.Quote(lang(ctx), s) }

// langURL switches language and comes back to the current page.
func langURL(ctx context.Context, l i18n.Lang) templ.SafeURL {
	next := req(ctx).URI
	if next == "" {
		next = "/"
	}
	return templ.SafeURL("/lang/" + string(l) + "?next=" + neturl.QueryEscape(next))
}

// ---- Themes ----

// currentTheme is "auto", "light", "dark" or "parchment".
func currentTheme(ctx context.Context) string {
	if th := req(ctx).Theme; th != "" {
		return th
	}
	return "auto"
}

// themeURL switches theme and comes back to the current page (the no-JS path;
// app.js switches instantly instead).
func themeURL(ctx context.Context, name string) templ.SafeURL {
	next := req(ctx).URI
	if next == "" {
		next = "/"
	}
	return templ.SafeURL("/theme/" + name + "?next=" + neturl.QueryEscape(next))
}

type themeOption struct{ Name, Label string }

func themeOptions(ctx context.Context) []themeOption {
	return []themeOption{
		{"auto", t(ctx, "Automatic (follow device)")},
		{"light", t(ctx, "Light")},
		{"dark", t(ctx, "Dark")},
		{"parchment", t(ctx, "Parchment")},
	}
}

func ariaCurrent(on bool) string {
	if on {
		return "true"
	}
	return "false"
}

// ---- Time formatting ----

func fmtDate(ctx context.Context, t time.Time) string         { return i18n.Date(lang(ctx), t) }
func fmtDateTime(ctx context.Context, t time.Time) string     { return i18n.DateTime(lang(ctx), t) }
func fmtMeetingTime(ctx context.Context, t *time.Time) string { return i18n.MeetingTime(lang(ctx), t) }
func ago(ctx context.Context, t time.Time) string             { return i18n.Ago(lang(ctx), t) }
func until(ctx context.Context, t time.Time) string           { return i18n.Until(lang(ctx), t) }
func ordinal(ctx context.Context, n int) string               { return i18n.Ordinal(lang(ctx), n) }
func isoTime(t time.Time) string                              { return t.UTC().Format(time.RFC3339) }
func methodLabel(ctx context.Context, m voting.Method) string {
	return i18n.MethodLabel(lang(ctx), string(m))
}
func meetingName(ctx context.Context, m *store.Meeting) string {
	return i18n.MeetingName(lang(ctx), m.Title, m.StartsAt)
}

// InputDateTime formats a time for <input type="datetime-local">.
func InputDateTime(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.In(i18n.Loc).Format("2006-01-02T15:04")
}

// ---- Domain helpers ----

func pollMeetingName(ctx context.Context, p *store.Poll) string {
	if p.MeetingID == nil {
		return ""
	}
	return i18n.MeetingName(lang(ctx), p.MeetingTitle, p.MeetingStartsAt)
}

func statusLabel(ctx context.Context, p *store.Poll) string {
	switch p.Status {
	case store.StatusNominating:
		if p.Method == voting.Informal {
			return t(ctx, "Open")
		}
		return t(ctx, "Collecting suggestions")
	case store.StatusVoting:
		return t(ctx, "Voting open")
	case store.StatusClosed:
		if p.Method == voting.Informal {
			return t(ctx, "Decided")
		}
		return t(ctx, "Closed")
	}
	return p.Status
}

func statusClass(p *store.Poll) string { return "badge status-" + p.Status }

func yearStr(y *int) string {
	if y == nil {
		return ""
	}
	return strconv.Itoa(*y)
}

func bookByline(b *store.Book) string {
	switch {
	case b.Authors != "" && b.Year != nil:
		return b.Authors + " · " + strconv.Itoa(*b.Year)
	case b.Year != nil:
		return strconv.Itoa(*b.Year)
	}
	return b.Authors
}

func initials(title string) string {
	out := ""
	n := 0
	inWord := false
	for _, r := range title {
		if r == ' ' || r == '\t' {
			inWord = false
			continue
		}
		if !inWord {
			out += string(r)
			n++
			if n == 2 {
				break
			}
		}
		inWord = true
	}
	return out
}

func subjectURL(subjectType string, id int64) string {
	switch subjectType {
	case "book":
		return fmt.Sprintf("/books/%d", id)
	case "meeting":
		return fmt.Sprintf("/meetings/%d", id)
	case "poll":
		return fmt.Sprintf("/polls/%d", id)
	case "user":
		return "/members"
	}
	return ""
}

func methodOptions() []voting.Method { return voting.Methods }

func methodHint(ctx context.Context, m voting.Method) string {
	switch m {
	case voting.Single:
		return t(ctx, "Everyone picks one favourite.")
	case voting.Multi:
		return t(ctx, "Vote for several (or all you like).")
	case voting.Points:
		return t(ctx, "Spread a budget of points.")
	case voting.Ranked:
		return t(ctx, "Rank in order of preference.")
	case voting.Informal:
		return t(ctx, "No ballots — just record what you decided.")
	}
	return ""
}

func intOrBlank(n int) string {
	if n == 0 {
		return ""
	}
	return strconv.Itoa(n)
}

func pointsFor(b *store.BallotRow, optID int64) string {
	if b == nil || b.Data.Points == nil {
		return ""
	}
	if p := b.Data.Points[optID]; p > 0 {
		return strconv.Itoa(p)
	}
	return ""
}

func choiceIn(b *store.BallotRow, optID int64) bool {
	if b == nil {
		return false
	}
	for _, c := range b.Data.Choices {
		if c == optID {
			return true
		}
	}
	return false
}

func rankOf(b *store.BallotRow, optID int64) int {
	if b == nil {
		return 0
	}
	for i, id := range b.Data.Ranking {
		if id == optID {
			return i + 1
		}
	}
	return 0
}

func seq(n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = i + 1
	}
	return out
}

func maxValue(ts []voting.Total) int {
	m := 0
	for _, t := range ts {
		if t.Value > m {
			m = t.Value
		}
	}
	return m
}

// unitCount is "3 votes" / "1 point" in the reader's language.
func unitCount(ctx context.Context, n int, unit string) string {
	if unit == "points" {
		return tn(ctx, n, "%d point", "%d points")
	}
	return tn(ctx, n, "%d vote", "%d votes")
}

func goodreadsSearch(b *store.Book) string { return openlibrary.GoodreadsSearchURL(b.Title, b.Authors) }

func activityUser(ctx context.Context, a store.Activity) string {
	if a.UserID == nil {
		return t(ctx, "System")
	}
	if a.UserName == "" {
		return t(ctx, "Former member")
	}
	return a.UserName
}

func activitySummary(ctx context.Context, a store.Activity) string {
	if a.MsgKey == "" {
		return a.Summary
	}
	return i18n.RenderMsg(lang(ctx), a.MsgKey, a.MsgArgs)
}

// fieldLabel names an edited field in the history.
func fieldLabel(ctx context.Context, field string) string {
	switch field {
	case "title":
		return t(ctx, "title")
	case "authors":
		return t(ctx, "authors")
	case "year":
		return t(ctx, "year")
	case "Goodreads link":
		return t(ctx, "Goodreads link")
	case "cover":
		return t(ctx, "cover")
	case "notes":
		return t(ctx, "notes")
	case "on want-to-read list":
		return t(ctx, "on want-to-read list")
	case "date":
		return t(ctx, "date")
	case "place":
		return t(ctx, "place")
	case "description":
		return t(ctx, "description")
	case "details":
		return t(ctx, "details")
	case "meeting":
		return t(ctx, "meeting")
	case "closes":
		return t(ctx, "closes")
	case "method":
		return t(ctx, "method")
	case "rules":
		return t(ctx, "rules")
	case "name":
		return t(ctx, "name")
	case "note":
		return t(ctx, "note")
	}
	return field
}

// fieldValue shows an edited value; a few stored codes are translated.
func fieldValue(ctx context.Context, field, value string) string {
	if value == "" {
		return "—"
	}
	switch field {
	case "on want-to-read list":
		if value == "yes" {
			return t(ctx, "yes")
		}
		return t(ctx, "no")
	case "method":
		if voting.Method(value).Valid() {
			return methodLabel(ctx, voting.Method(value))
		}
	}
	return value
}

func commentAuthor(ctx context.Context, c *store.Comment) string {
	if c.UserName == "" {
		return t(ctx, "Former member")
	}
	return c.UserName
}

func emptyDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func bookJSON(title, authors string, year int, cover, goodreads, key string) string {
	b, _ := json.Marshal(map[string]any{
		"title": title, "authors": authors, "year": year, "cover_url": cover,
		"goodreads_url": goodreads, "openlibrary_key": key,
	})
	return string(b)
}
