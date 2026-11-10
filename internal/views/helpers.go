package views

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/a-h/templ"

	"bookclub/internal/appctx"
	"bookclub/internal/openlibrary"
	"bookclub/internal/store"
	"bookclub/internal/voting"
)

// Loc is the club's time zone, set at startup.
var Loc = time.Local

// AssetVersion is appended to static asset URLs for cache busting.
var AssetVersion = "1"

// allowEval is off so htmx never needs 'unsafe-eval' under our CSP.
const htmxConfig = `{"includeIndicatorStyles":false,"historyCacheSize":0,"allowEval":false}`

func req(ctx context.Context) *appctx.Request   { return appctx.Get(ctx) }
func currentUser(ctx context.Context) *store.User { return appctx.Get(ctx).User }
func clubName(ctx context.Context) string        { return appctx.Get(ctx).ClubName }

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

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

func url(format string, args ...any) templ.SafeURL {
	return templ.SafeURL(fmt.Sprintf(format, args...))
}

func path(format string, args ...any) string { return fmt.Sprintf(format, args...) }

// ---- Time formatting ----

func fmtDate(t time.Time) string     { return t.In(Loc).Format("Mon 2 Jan 2006") }
func fmtDateTime(t time.Time) string { return t.In(Loc).Format("Mon 2 Jan 2006, 15:04") }
func isoTime(t time.Time) string     { return t.UTC().Format(time.RFC3339) }

func fmtMeetingTime(t *time.Time) string {
	if t == nil {
		return "Date to be decided"
	}
	lt := t.In(Loc)
	if lt.Hour() == 0 && lt.Minute() == 0 {
		return lt.Format("Monday 2 January 2006")
	}
	return lt.Format("Monday 2 January 2006, 15:04")
}

func ago(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < 0:
		return fmtDateTime(t)
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return plural(int(d.Minutes()), "minute", "minutes") + " ago"
	case d < 24*time.Hour:
		return plural(int(d.Hours()), "hour", "hours") + " ago"
	case d < 7*24*time.Hour:
		return plural(int(d.Hours()/24), "day", "days") + " ago"
	}
	return fmtDate(t)
}

func until(t time.Time) string {
	d := time.Until(t)
	switch {
	case d <= 0:
		return "now"
	case d < time.Hour:
		return "in " + plural(int(d.Minutes())+1, "minute", "minutes")
	case d < 48*time.Hour:
		return "in " + plural(int(d.Hours()+0.5), "hour", "hours")
	}
	return "in " + plural(int(d.Hours()/24+0.5), "day", "days")
}

// unit returns "1 vote" style singular units for result tables.
func unit(value int, u string) string {
	if value == 1 {
		return strings.TrimSuffix(u, "s")
	}
	return u
}

// InputDateTime formats a time for <input type="datetime-local">.
func InputDateTime(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.In(Loc).Format("2006-01-02T15:04")
}

// ---- Domain helpers ----

func meetingName(m *store.Meeting) string { return store.MeetingDisplayName(m, Loc) }

func pollMeetingName(p *store.Poll) string {
	if p.MeetingID == nil {
		return ""
	}
	return store.MeetingDisplayName(&store.Meeting{Title: p.MeetingTitle, StartsAt: p.MeetingStartsAt}, Loc)
}

func statusLabel(p *store.Poll) string {
	switch p.Status {
	case store.StatusNominating:
		if p.Method == voting.Informal {
			return "Open"
		}
		return "Collecting suggestions"
	case store.StatusVoting:
		return "Voting open"
	case store.StatusClosed:
		if p.Method == voting.Informal {
			return "Decided"
		}
		return "Closed"
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
	parts := []string{}
	if b.Authors != "" {
		parts = append(parts, b.Authors)
	}
	if b.Year != nil {
		parts = append(parts, strconv.Itoa(*b.Year))
	}
	return strings.Join(parts, " · ")
}

func initials(title string) string {
	words := strings.Fields(title)
	out := ""
	for _, w := range words {
		r := []rune(w)
		if len(r) > 0 {
			out += strings.ToUpper(string(r[0]))
		}
		if len([]rune(out)) == 2 {
			break
		}
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

func short(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func methodOptions() []voting.Method { return voting.Methods }

func methodHint(m voting.Method) string {
	switch m {
	case voting.Single:
		return "Everyone picks one favourite."
	case voting.Multi:
		return "Vote for several (or all you like)."
	case voting.Points:
		return "Spread a budget of points."
	case voting.Ranked:
		return "Rank in order of preference."
	case voting.Informal:
		return "No ballots — just record what you decided."
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

func ordinal(n int) string {
	suffix := "th"
	switch {
	case n%100 >= 11 && n%100 <= 13:
	case n%10 == 1:
		suffix = "st"
	case n%10 == 2:
		suffix = "nd"
	case n%10 == 3:
		suffix = "rd"
	}
	return strconv.Itoa(n) + suffix
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

func goodreadsSearch(b *store.Book) string { return openlibrary.GoodreadsSearchURL(b.Title, b.Authors) }

func activityUser(a store.Activity) string {
	if a.UserID == nil {
		return "System"
	}
	if a.UserName == "" {
		return "Former member"
	}
	return a.UserName
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
