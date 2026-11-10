package i18n

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

const lit = `"((?:[^"\\]|\\.)*)"`

// Calls whose first string literal is a translation key.
var keyPatterns = []*regexp.Regexp{
	regexp.MustCompile(`\b(?:t|tf|tr|trf|T|Tf)\((?:ctx|r|l), ` + lit),
	regexp.MustCompile(`\btrKey\(` + lit),
	regexp.MustCompile(`\bstore\.M\(` + lit),
	regexp.MustCompile(`\berrStatus\([\w.]+, ` + lit),
	regexp.MustCompile(`\bs\.flash\(w, r, "\w+", ` + lit),
	regexp.MustCompile(`\bErrf\(` + lit),
}

// Plural calls: tn(ctx, n, "one", "many") and N(l, n, "one", "many").
var pluralPattern = regexp.MustCompile(`\b(?:tn|N)\((?:ctx|l), [^,()]+(?:\([^()]*\))?, ` + lit + `, ` + lit)

var hasLetter = regexp.MustCompile(`\pL`)

type found struct{ singular, plural map[string]string } // key -> file

func scanSources(t *testing.T) found {
	t.Helper()
	f := found{singular: map[string]string{}, plural: map[string]string{}}
	root := filepath.Join("..")
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() || strings.HasSuffix(name, "_test.go") || strings.HasSuffix(name, "_templ.go") {
			return nil
		}
		if !strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, ".templ") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		src := string(b)
		for _, m := range pluralPattern.FindAllStringSubmatch(src, -1) {
			key, err := strconv.Unquote(`"` + m[1] + `"`)
			if err != nil {
				t.Fatalf("%s: bad literal %q", path, m[1])
			}
			f.plural[key] = path
		}
		for _, re := range keyPatterns {
			for _, m := range re.FindAllStringSubmatch(src, -1) {
				key, err := strconv.Unquote(`"` + m[1] + `"`)
				if err != nil {
					t.Fatalf("%s: bad literal %q", path, m[1])
				}
				if hasLetter.MatchString(verbRe.ReplaceAllString(key, "")) {
					f.singular[key] = path
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.singular) < 100 {
		t.Fatalf("found only %d keys; is the scanner broken?", len(f.singular))
	}
	return f
}

var verbRe = regexp.MustCompile(`%[sdv]`)

func verbs(s string) string { return strings.Join(verbRe.FindAllString(s, -1), "") }

// TestCatalogComplete fails if any text in the app lacks an Icelandic
// translation, or a translation has different %s/%d placeholders.
func TestCatalogComplete(t *testing.T) {
	f := scanSources(t)
	var missing []string
	for key, file := range f.singular {
		v, ok := is[key]
		if !ok || v == "" {
			missing = append(missing, file+": "+strconv.Quote(key))
			continue
		}
		if verbs(v) != verbs(key) {
			t.Errorf("placeholders differ for %q: %q", key, v)
		}
	}
	for key, file := range f.plural {
		forms, ok := isPlural[key]
		if !ok {
			missing = append(missing, file+": plural "+strconv.Quote(key))
			continue
		}
		for _, form := range forms {
			if verbs(form) != verbs(key) {
				t.Errorf("placeholders differ for plural %q: %q", key, form)
			}
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("%d phrases have no Icelandic translation:\n%s", len(missing), strings.Join(missing, "\n"))
	}

	// Unused entries aren't errors, but worth knowing about.
	for key := range is {
		if _, ok := f.singular[key]; !ok {
			t.Logf("unused translation: %q", key)
		}
	}
}

func TestPlurals(t *testing.T) {
	cases := map[int]bool{1: true, 2: false, 11: false, 21: true, 101: true, 111: false, 0: false}
	for n, want := range cases {
		if isSingular(n) != want {
			t.Errorf("isSingular(%d) = %v", n, !want)
		}
	}
	if got := N(EN, 1, "%d book", "%d books"); got != "1 book" {
		t.Errorf("EN singular: %q", got)
	}
	if got := N(EN, 3, "%d book", "%d books"); got != "3 books" {
		t.Errorf("EN plural: %q", got)
	}
}

func TestDatesAndMessages(t *testing.T) {
	Loc = time.UTC
	d := time.Date(2026, 12, 3, 19, 30, 0, 0, time.UTC)
	if got := LongDate(IS, d); got != "fimmtudagur 3. desember 2026" {
		t.Errorf("LongDate IS = %q", got)
	}
	if got := DateTime(IS, d); got != "fim. 3. des. 2026, kl. 19:30" {
		t.Errorf("DateTime IS = %q", got)
	}
	if got := Date(EN, d); got != "Thu 3 Dec 2026" {
		t.Errorf("Date EN = %q", got)
	}
	msg := RenderMsg(EN, "planned “%s”", []string{ArgMeeting("", &d)})
	if msg != "planned “Meeting on Thu 3 Dec 2026”" {
		t.Errorf("RenderMsg EN = %q", msg)
	}
	list := RenderMsg(EN, "recorded the decision for “%s”: %s", []string{"Dec", ArgList([]string{"A", "B"})})
	if list != "recorded the decision for “Dec”: “A”, “B”" {
		t.Errorf("list = %q", list)
	}
	if got := FromAcceptLanguage("is-IS,is;q=0.9,en;q=0.8"); got != IS {
		t.Errorf("Accept-Language is-IS -> %q", got)
	}
	if got := FromAcceptLanguage("en-GB,en;q=0.9,is;q=0.5"); got != EN {
		t.Errorf("Accept-Language en-GB -> %q", got)
	}
}
