// Package i18n translates the interface between English and Icelandic.
//
// English text is the key: T(IS, "Add a book") looks the phrase up in the
// Icelandic catalog (is.go) and falls back to English when it's missing.
// TestCatalogComplete makes sure nothing is missing.
package i18n

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type Lang string

const (
	EN Lang = "en"
	IS Lang = "is"
)

// Loc is the club's time zone, set at startup.
var Loc = time.Local

func Parse(s string) (Lang, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "en":
		return EN, true
	case "is":
		return IS, true
	}
	return "", false
}

// FromAcceptLanguage picks Icelandic when the browser lists it before English.
func FromAcceptLanguage(header string) Lang {
	for _, part := range strings.Split(header, ",") {
		tag := strings.ToLower(strings.TrimSpace(strings.SplitN(part, ";", 2)[0]))
		switch {
		case tag == "is" || strings.HasPrefix(tag, "is-"):
			return IS
		case tag == "en" || strings.HasPrefix(tag, "en-"):
			return EN
		}
	}
	return EN
}

// T translates a phrase.
func T(l Lang, key string) string {
	if l == IS {
		if v, ok := is[key]; ok && v != "" {
			return v
		}
	}
	return key
}

// Tf translates a format string, then fills it in.
func Tf(l Lang, key string, args ...any) string {
	return fmt.Sprintf(T(l, key), args...)
}

// isSingular is Icelandic's plural rule: 1, 21, 31… are singular, 11 isn't.
func isSingular(n int) bool {
	if n < 0 {
		n = -n
	}
	return n%10 == 1 && n%100 != 11
}

// N picks the singular or plural form. Both forms must contain %d.
// The Icelandic forms are looked up by the English singular.
func N(l Lang, n int, one, many string) string {
	if l == IS {
		if forms, ok := isPlural[one]; ok {
			if isSingular(n) {
				return fmt.Sprintf(forms[0], n)
			}
			return fmt.Sprintf(forms[1], n)
		}
	}
	if n == 1 {
		return fmt.Sprintf(one, n)
	}
	return fmt.Sprintf(many, n)
}

// Quote wraps text in the language's quotation marks.
func Quote(l Lang, s string) string {
	if l == IS {
		return "„" + s + "“"
	}
	return "“" + s + "”"
}

func QuoteList(l Lang, items []string) string {
	out := make([]string, len(items))
	for i, s := range items {
		out[i] = Quote(l, s)
	}
	return strings.Join(out, ", ")
}

// ---- Dates ----

var (
	isDays        = [...]string{"sunnudagur", "mánudagur", "þriðjudagur", "miðvikudagur", "fimmtudagur", "föstudagur", "laugardagur"}
	isDaysShort   = [...]string{"sun.", "mán.", "þri.", "mið.", "fim.", "fös.", "lau."}
	isMonths      = [...]string{"janúar", "febrúar", "mars", "apríl", "maí", "júní", "júlí", "ágúst", "september", "október", "nóvember", "desember"}
	isMonthsShort = [...]string{"jan.", "feb.", "mar.", "apr.", "maí", "jún.", "júl.", "ágú.", "sep.", "okt.", "nóv.", "des."}
)

// Date: "Mon 2 Jan 2006" / "mán. 2. jan. 2006".
func Date(l Lang, t time.Time) string {
	t = t.In(Loc)
	if l == IS {
		return fmt.Sprintf("%s %d. %s %d", isDaysShort[t.Weekday()], t.Day(), isMonthsShort[t.Month()-1], t.Year())
	}
	return t.Format("Mon 2 Jan 2006")
}

// DateTime: "Mon 2 Jan 2006, 15:04" / "mán. 2. jan. 2006, kl. 15:04".
func DateTime(l Lang, t time.Time) string {
	t = t.In(Loc)
	if l == IS {
		return Date(l, t) + ", kl. " + t.Format("15:04")
	}
	return t.Format("Mon 2 Jan 2006, 15:04")
}

// LongDate: "Monday 2 January 2006" / "mánudagur 2. janúar 2006".
func LongDate(l Lang, t time.Time) string {
	t = t.In(Loc)
	if l == IS {
		return fmt.Sprintf("%s %d. %s %d", isDays[t.Weekday()], t.Day(), isMonths[t.Month()-1], t.Year())
	}
	return t.Format("Monday 2 January 2006")
}

// MeetingTime shows a meeting's date, with the time unless it's midnight.
func MeetingTime(l Lang, t *time.Time) string {
	if t == nil {
		return T(l, "Date to be decided")
	}
	lt := t.In(Loc)
	if lt.Hour() == 0 && lt.Minute() == 0 {
		return LongDate(l, lt)
	}
	if l == IS {
		return LongDate(l, lt) + ", kl. " + lt.Format("15:04")
	}
	return LongDate(l, lt) + ", " + lt.Format("15:04")
}

// MeetingName gives a meeting a readable name even without a title.
func MeetingName(l Lang, title string, startsAt *time.Time) string {
	if title != "" {
		return title
	}
	if startsAt != nil {
		return Tf(l, "Meeting on %s", Date(l, *startsAt))
	}
	return T(l, "Meeting (date to be decided)")
}

func pick(n int, one, many string) string {
	if isSingular(n) {
		return fmt.Sprintf(one, n)
	}
	return fmt.Sprintf(many, n)
}

func enPlural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return strconv.Itoa(n) + " " + word + "s"
}

// Ago: "5 minutes ago" / "fyrir 5 mínútum".
func Ago(l Lang, t time.Time) string {
	d := time.Since(t)
	if d < 0 || d >= 7*24*time.Hour {
		return Date(l, t)
	}
	if l == IS {
		switch {
		case d < time.Minute:
			return "rétt í þessu"
		case d < time.Hour:
			return pick(int(d.Minutes()), "fyrir %d mínútu", "fyrir %d mínútum")
		case d < 24*time.Hour:
			return pick(int(d.Hours()), "fyrir %d klukkustund", "fyrir %d klukkustundum")
		}
		return pick(int(d.Hours()/24), "fyrir %d degi", "fyrir %d dögum")
	}
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return enPlural(int(d.Minutes()), "minute") + " ago"
	case d < 24*time.Hour:
		return enPlural(int(d.Hours()), "hour") + " ago"
	}
	return enPlural(int(d.Hours()/24), "day") + " ago"
}

// Until: "in 2 days" / "eftir 2 daga".
func Until(l Lang, t time.Time) string {
	d := time.Until(t)
	var unit string
	var n int
	switch {
	case d <= 0:
		if l == IS {
			return "núna"
		}
		return "now"
	case d < time.Hour:
		unit, n = "minute", int(d.Minutes())+1
	case d < 48*time.Hour:
		unit, n = "hour", int(d.Hours()+0.5)
	default:
		unit, n = "day", int(d.Hours()/24+0.5)
	}
	if l == IS {
		switch unit {
		case "minute":
			return pick(n, "eftir %d mínútu", "eftir %d mínútur")
		case "hour":
			return pick(n, "eftir %d klukkustund", "eftir %d klukkustundir")
		}
		return pick(n, "eftir %d dag", "eftir %d daga")
	}
	return "in " + enPlural(n, unit)
}

// Ordinal: "1st" / "1.".
func Ordinal(l Lang, n int) string {
	if l == IS {
		return strconv.Itoa(n) + "."
	}
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

// MethodLabel names a voting method.
func MethodLabel(l Lang, method string) string {
	switch method {
	case "single":
		return T(l, "One vote each")
	case "multi":
		return T(l, "Several votes each")
	case "points":
		return T(l, "Points to spread")
	case "ranked":
		return T(l, "Ranked choice")
	case "informal":
		return T(l, "Informal decision")
	}
	return method
}

// ---- History messages ----
//
// History entries are stored as an English format string plus arguments,
// and rendered in the reader's language. Arguments that themselves depend on
// the language (an untitled meeting's date, a voting method, a count…) are
// stored as tokens and rendered here.

const tok = "\x1f"

// ArgMeeting is a meeting's title, or a token for its date if it has none.
func ArgMeeting(title string, startsAt *time.Time) string {
	if title != "" {
		return title
	}
	if startsAt == nil {
		return tok + "meeting:"
	}
	return tok + "meeting:" + startsAt.UTC().Format(time.RFC3339)
}

func ArgMethod(method string) string { return tok + "method:" + method }
func ArgBallots(n int) string        { return tok + "ballots:" + strconv.Itoa(n) }

func ArgList(items []string) string {
	b, _ := json.Marshal(items)
	return tok + "list:" + string(b)
}

// ArgSubject is "the book “X”", "the vote “X”" or a quoted meeting name.
func ArgSubject(kind, name string) string { return tok + "subject:" + kind + ":" + name }

func renderArg(l Lang, a string) string {
	if !strings.HasPrefix(a, tok) {
		return a
	}
	kind, val, _ := strings.Cut(a[len(tok):], ":")
	switch kind {
	case "meeting":
		if val == "" {
			return T(l, "Meeting (date to be decided)")
		}
		t, err := time.Parse(time.RFC3339, val)
		if err != nil {
			return val
		}
		return Tf(l, "Meeting on %s", Date(l, t))
	case "method":
		return strings.ToLower(MethodLabel(l, val))
	case "ballots":
		n, _ := strconv.Atoi(val)
		return N(l, n, "%d ballot", "%d ballots")
	case "list":
		var items []string
		_ = json.Unmarshal([]byte(val), &items)
		return QuoteList(l, items)
	case "subject":
		sk, inner, _ := strings.Cut(val, ":")
		name := renderArg(l, inner)
		switch sk {
		case "book":
			return Tf(l, "the book “%s”", name)
		case "poll":
			return Tf(l, "the vote “%s”", name)
		}
		return Quote(l, name)
	}
	return val
}

// RenderMsg renders a stored history message in the given language.
func RenderMsg(l Lang, key string, args []string) string {
	vals := make([]any, len(args))
	for i, a := range args {
		vals[i] = renderArg(l, a)
	}
	return Tf(l, key, vals...)
}
