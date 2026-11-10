// Package voting implements the club's voting methods: ballot validation,
// tallying (with a step-by-step explanation) and the ballot fingerprint used
// to show that published ballots haven't changed since a vote closed.
//
// It is deliberately free of database and HTTP code so it can be tested and
// audited on its own.
package voting

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

type Method string

const (
	Single   Method = "single"   // one choice each
	Multi    Method = "multi"    // up to N choices each (N = 0: approval voting)
	Points   Method = "points"   // spread N points
	Ranked   Method = "ranked"   // rank options; instant-runoff or Borda count
	Informal Method = "informal" // no ballots; the decision is just recorded
)

// Methods in the order they're offered in the UI.
var Methods = []Method{Single, Multi, Points, Ranked, Informal}

func (m Method) Valid() bool {
	for _, x := range Methods {
		if x == m {
			return true
		}
	}
	return false
}

func (m Method) HasBallots() bool { return m != Informal }

// RuleError explains why a ballot was rejected. Format is an English format
// string; the web layer translates it before showing it.
type RuleError struct {
	Format string
	Args   []any
}

func (e *RuleError) Error() string { return fmt.Sprintf(e.Format, e.Args...) }

func Errf(format string, args ...any) error { return &RuleError{Format: format, Args: args} }

const (
	CountIRV   = "irv"
	CountBorda = "borda"
)

// Settings are the per-poll rules. They are stored as JSON so new rules can
// be added without a database migration. Zero values mean "no limit".
type Settings struct {
	MaxChoices   int    `json:"max_choices,omitempty"`    // multi: 0 = unlimited (approval)
	PointsTotal  int    `json:"points_total,omitempty"`   // points: points per member
	MaxPerOption int    `json:"max_per_option,omitempty"` // points: cap per option, 0 = none
	RankedCount  string `json:"ranked_count,omitempty"`   // ranked: "irv" or "borda"
	MaxRanks     int    `json:"max_ranks,omitempty"`      // ranked: 0 = rank as many as you like
	Winners      int    `json:"winners,omitempty"`        // how many options this vote picks

	Secret           bool `json:"secret,omitempty"`             // publish ballots without names
	LiveResults      bool `json:"live_results,omitempty"`       // show running totals while open
	LateNominations  bool `json:"late_nominations,omitempty"`   // allow suggestions after voting opens
	MaxNominations   int  `json:"max_nominations,omitempty"`    // per member, 0 = unlimited
	NoOwnNominations bool `json:"no_own_nominations,omitempty"` // can't vote for your own suggestions
}

// Normalize fills in defaults and clamps nonsense values.
func (s Settings) Normalize(m Method) Settings {
	if s.Winners < 1 {
		s.Winners = 1
	}
	if s.MaxChoices < 0 {
		s.MaxChoices = 0
	}
	if s.MaxRanks < 0 {
		s.MaxRanks = 0
	}
	if s.MaxNominations < 0 {
		s.MaxNominations = 0
	}
	if s.MaxPerOption < 0 {
		s.MaxPerOption = 0
	}
	if m == Points && s.PointsTotal < 1 {
		s.PointsTotal = 10
	}
	if m == Ranked && s.RankedCount != CountBorda {
		s.RankedCount = CountIRV
	}
	return s
}

// Ballot is one member's vote. Which field is used depends on the method.
type Ballot struct {
	Choices []int64       `json:"choices,omitempty"` // single, multi
	Points  map[int64]int `json:"points,omitempty"`  // points
	Ranking []int64       `json:"ranking,omitempty"` // ranked, most preferred first
}

// Clean validates a ballot against the poll's rules and returns a normalised
// copy (e.g. zero-point entries removed).
//   - valid: the poll's option IDs
//   - own:   option IDs the voter nominated (only used with NoOwnNominations)
func Clean(m Method, s Settings, b Ballot, valid, own map[int64]bool) (Ballot, error) {
	s = s.Normalize(m)
	checkOption := func(id int64) error {
		if !valid[id] {
			return Errf("your ballot refers to an option that isn't part of this vote")
		}
		if s.NoOwnNominations && own[id] {
			return Errf("you can't vote for an option you suggested")
		}
		return nil
	}
	noDupes := func(ids []int64) error {
		seen := map[int64]bool{}
		for _, id := range ids {
			if seen[id] {
				return Errf("the same option appears twice on your ballot")
			}
			seen[id] = true
		}
		return nil
	}

	switch m {
	case Single:
		if len(b.Choices) != 1 {
			return Ballot{}, Errf("pick exactly one option")
		}
		if err := checkOption(b.Choices[0]); err != nil {
			return Ballot{}, err
		}
		return Ballot{Choices: []int64{b.Choices[0]}}, nil

	case Multi:
		if len(b.Choices) == 0 {
			return Ballot{}, Errf("pick at least one option")
		}
		if s.MaxChoices > 0 && len(b.Choices) > s.MaxChoices {
			return Ballot{}, Errf("you can pick at most %d", s.MaxChoices)
		}
		if err := noDupes(b.Choices); err != nil {
			return Ballot{}, err
		}
		for _, id := range b.Choices {
			if err := checkOption(id); err != nil {
				return Ballot{}, err
			}
		}
		out := append([]int64(nil), b.Choices...)
		sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
		return Ballot{Choices: out}, nil

	case Points:
		total := 0
		out := map[int64]int{}
		for id, p := range b.Points {
			if p < 0 {
				return Ballot{}, Errf("points can't be negative")
			}
			if p == 0 {
				continue
			}
			if err := checkOption(id); err != nil {
				return Ballot{}, err
			}
			if s.MaxPerOption > 0 && p > s.MaxPerOption {
				return Ballot{}, Errf("at most %d points per option", s.MaxPerOption)
			}
			total += p
			out[id] = p
		}
		if total == 0 {
			return Ballot{}, Errf("give at least one point")
		}
		if total > s.PointsTotal {
			return Ballot{}, Errf("you used %d points but only have %d", total, s.PointsTotal)
		}
		return Ballot{Points: out}, nil

	case Ranked:
		if len(b.Ranking) == 0 {
			return Ballot{}, Errf("rank at least one option")
		}
		if s.MaxRanks > 0 && len(b.Ranking) > s.MaxRanks {
			return Ballot{}, Errf("you can rank at most %d options", s.MaxRanks)
		}
		if err := noDupes(b.Ranking); err != nil {
			return Ballot{}, err
		}
		for _, id := range b.Ranking {
			if err := checkOption(id); err != nil {
				return Ballot{}, err
			}
		}
		return Ballot{Ranking: append([]int64(nil), b.Ranking...)}, nil
	}
	return Ballot{}, Errf("this vote doesn't use ballots")
}

// Option is a choice in a poll, as far as counting is concerned.
type Option struct {
	ID    int64
	Label string
}

// Total is one option's score in a count (or in one round of a count).
type Total struct {
	OptionID int64
	Label    string
	Value    int
	Winner   bool
	Tied     bool
}

// How a round of instant runoff ended.
const (
	OutcomeOnlyOneLeft = "only_one_left" // Counts[0] wins by default
	OutcomeMajority    = "majority"      // Counts[0] has more than half of Active
	OutcomeNoActive    = "no_active"     // no ballot ranks any remaining option
	OutcomeAllTied     = "all_tied"      // every remaining option has Lowest
	OutcomeEliminated  = "eliminated"    // Eliminated (with Lowest each) are out
)

// Round is one round of an instant-runoff count.
type Round struct {
	Number     int
	Counts     []Total // continuing options, highest first
	Active     int     // ballots still counting toward a continuing option
	Exhausted  int     // ballots with no continuing option left
	Eliminated []int64
	Lowest     int
	Outcome    string
}

// Note kinds explain a result in a way the UI can translate.
const (
	NoteNoOptions    = "no_options"
	NoteNoBallots    = "no_ballots"
	NoteNoneReceived = "none_received" // nobody got any votes/points
	NoteFewReceived  = "few_received"  // Value options got anything; fewer than seats
	NoteTie          = "tie"           // Options tied with Value each for the last spot
	NoteRunoffTie    = "runoff_tie"
)

type Note struct {
	Kind    string
	Value   int
	Options []int64
}

// Run is a full instant-runoff count for one seat.
type Run struct {
	Seat     int
	Excluded []int64 // winners of earlier seats, removed from this count
	Rounds   []Round
	Winner   int64 // 0 if undecided
	Tied     []int64
}

type Result struct {
	Method      Method
	Unit        string // "votes" or "points"
	BallotCount int
	Totals      []Total // for every method except instant runoff
	Runs        []Run   // instant runoff only
	Winners     []int64
	Tied        []int64 // options tied for the last remaining spot
	Notes       []Note
}

func (r Result) IsWinner(id int64) bool { return contains(r.Winners, id) }
func (r Result) IsTied(id int64) bool   { return contains(r.Tied, id) }

func contains(ids []int64, id int64) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}

// Tally counts the ballots. It is deterministic: the same options and ballots
// always give the same result, regardless of ballot order.
func Tally(m Method, s Settings, options []Option, ballots []Ballot) Result {
	s = s.Normalize(m)
	res := Result{Method: m, Unit: "votes", BallotCount: len(ballots)}
	if !m.HasBallots() {
		return res
	}
	if len(options) == 0 {
		res.Notes = append(res.Notes, Note{Kind: NoteNoOptions})
		return res
	}
	if len(ballots) == 0 {
		res.Notes = append(res.Notes, Note{Kind: NoteNoBallots})
		return res
	}

	valid := map[int64]bool{}
	for _, o := range options {
		valid[o.ID] = true
	}
	values := map[int64]int{}

	switch m {
	case Single, Multi:
		for _, b := range ballots {
			for _, id := range b.Choices {
				if valid[id] {
					values[id]++
				}
			}
		}
	case Points:
		res.Unit = "points"
		for _, b := range ballots {
			for id, p := range b.Points {
				if valid[id] && p > 0 {
					values[id] += p
				}
			}
		}
	case Ranked:
		if s.RankedCount == CountBorda {
			res.Unit = "points"
			n := len(options)
			for _, b := range ballots {
				for i, id := range b.Ranking {
					if valid[id] {
						values[id] += n - i
					}
				}
			}
		} else {
			instantRunoff(&res, options, ballots, s.Winners)
			return res
		}
	}

	res.Totals = makeTotals(options, values)
	pickTop(&res, s.Winners)
	return res
}

func makeTotals(options []Option, values map[int64]int) []Total {
	out := make([]Total, 0, len(options))
	for _, o := range options {
		out = append(out, Total{OptionID: o.ID, Label: o.Label, Value: values[o.ID]})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Value != out[j].Value {
			return out[i].Value > out[j].Value
		}
		if out[i].Label != out[j].Label {
			return out[i].Label < out[j].Label
		}
		return out[i].OptionID < out[j].OptionID
	})
	return out
}

func pickTop(res *Result, seats int) {
	t := res.Totals
	if len(t) == 0 || t[0].Value == 0 {
		res.Notes = append(res.Notes, Note{Kind: NoteNoneReceived})
		return
	}
	if seats > len(t) {
		seats = len(t)
	}
	cutoff := t[seats-1].Value
	if cutoff == 0 {
		// Fewer options got anything than there are spots to fill.
		for i := range t {
			if t[i].Value > 0 {
				t[i].Winner = true
				res.Winners = append(res.Winners, t[i].OptionID)
			}
		}
		res.Notes = append(res.Notes, Note{Kind: NoteFewReceived, Value: len(res.Winners)})
		return
	}
	tieAtCutoff := seats < len(t) && t[seats].Value == cutoff
	for i := range t {
		switch {
		case t[i].Value > cutoff || (!tieAtCutoff && t[i].Value == cutoff):
			t[i].Winner = true
			res.Winners = append(res.Winners, t[i].OptionID)
		case tieAtCutoff && t[i].Value == cutoff:
			t[i].Tied = true
			res.Tied = append(res.Tied, t[i].OptionID)
		}
	}
	if tieAtCutoff {
		res.Notes = append(res.Notes, Note{Kind: NoteTie, Value: cutoff, Options: res.Tied})
	}
}

func instantRunoff(res *Result, options []Option, ballots []Ballot, seats int) {
	if seats > len(options) {
		seats = len(options)
	}
	excluded := map[int64]bool{}
	for seat := 1; seat <= seats; seat++ {
		run := runIRV(options, ballots, excluded)
		run.Seat = seat
		for id := range excluded {
			run.Excluded = append(run.Excluded, id)
		}
		sort.Slice(run.Excluded, func(i, j int) bool { return run.Excluded[i] < run.Excluded[j] })
		res.Runs = append(res.Runs, run)
		if run.Winner == 0 {
			res.Tied = run.Tied
			if len(run.Tied) > 0 {
				res.Notes = append(res.Notes, Note{Kind: NoteRunoffTie, Options: run.Tied})
			}
			return
		}
		res.Winners = append(res.Winners, run.Winner)
		excluded[run.Winner] = true
	}
}

func runIRV(options []Option, ballots []Ballot, excluded map[int64]bool) Run {
	continuing := map[int64]bool{}
	var conOpts []Option
	for _, o := range options {
		if !excluded[o.ID] {
			continuing[o.ID] = true
			conOpts = append(conOpts, o)
		}
	}
	var run Run
	for n := 1; len(continuing) > 0; n++ {
		counts := map[int64]int{}
		exhausted := 0
		for _, b := range ballots {
			top := int64(0)
			for _, id := range b.Ranking {
				if continuing[id] {
					top = id
					break
				}
			}
			if top == 0 {
				exhausted++
			} else {
				counts[top]++
			}
		}
		var live []Option
		for _, o := range conOpts {
			if continuing[o.ID] {
				live = append(live, o)
			}
		}
		round := Round{Number: n, Active: len(ballots) - exhausted, Exhausted: exhausted, Counts: makeTotals(live, counts)}

		if len(live) == 1 {
			round.Counts[0].Winner = true
			round.Outcome = OutcomeOnlyOneLeft
			run.Rounds = append(run.Rounds, round)
			run.Winner = round.Counts[0].OptionID
			return run
		}
		if round.Active == 0 {
			for i := range round.Counts {
				round.Counts[i].Tied = true
				run.Tied = append(run.Tied, round.Counts[i].OptionID)
			}
			round.Outcome = OutcomeNoActive
			run.Rounds = append(run.Rounds, round)
			return run
		}
		best := round.Counts[0]
		if best.Value*2 > round.Active {
			round.Counts[0].Winner = true
			round.Outcome = OutcomeMajority
			run.Rounds = append(run.Rounds, round)
			run.Winner = best.OptionID
			return run
		}
		lowest := round.Counts[len(round.Counts)-1].Value
		round.Lowest = lowest
		var out []int64
		for _, t := range round.Counts {
			if t.Value == lowest {
				out = append(out, t.OptionID)
			}
		}
		if len(out) == len(round.Counts) {
			for i := range round.Counts {
				round.Counts[i].Tied = true
			}
			run.Tied = out
			round.Outcome = OutcomeAllTied
			run.Rounds = append(run.Rounds, round)
			return run
		}
		round.Eliminated = out
		round.Outcome = OutcomeEliminated
		for _, id := range out {
			delete(continuing, id)
		}
		run.Rounds = append(run.Rounds, round)
	}
	return run
}

// FormatBallot renders a ballot in a compact human-readable form.
func FormatBallot(m Method, b Ballot, label func(int64) string) string {
	switch m {
	case Single, Multi:
		parts := make([]string, 0, len(b.Choices))
		for _, id := range b.Choices {
			parts = append(parts, label(id))
		}
		sort.Strings(parts)
		return strings.Join(parts, " · ")
	case Points:
		type kv struct {
			name string
			p    int
		}
		var items []kv
		for id, p := range b.Points {
			items = append(items, kv{label(id), p})
		}
		sort.Slice(items, func(i, j int) bool {
			if items[i].p != items[j].p {
				return items[i].p > items[j].p
			}
			return items[i].name < items[j].name
		})
		parts := make([]string, 0, len(items))
		for _, it := range items {
			parts = append(parts, fmt.Sprintf("%s: %d", it.name, it.p))
		}
		return strings.Join(parts, " · ")
	case Ranked:
		parts := make([]string, 0, len(b.Ranking))
		for i, id := range b.Ranking {
			parts = append(parts, fmt.Sprintf("%d. %s", i+1, label(id)))
		}
		return strings.Join(parts, "  ")
	}
	return ""
}

// ---- Verification ----

// FingerprintOption identifies an option without depending on editable text
// such as a book's title.
type FingerprintOption struct {
	ID     int64  `json:"id"`
	BookID int64  `json:"book_id,omitempty"`
	Label  string `json:"label,omitempty"`
}

// PublishedBallot is a ballot as it appears in the public ballot list.
type PublishedBallot struct {
	Receipt string `json:"receipt"`
	VoterID int64  `json:"voter_id,omitempty"` // open ballots only
	Ballot  Ballot `json:"ballot"`
}

// Fingerprint is a SHA-256 over a canonical encoding of the options and
// ballots. It's recorded when voting closes; if anything changes afterwards,
// the recomputed fingerprint won't match.
func Fingerprint(pollID int64, m Method, options []FingerprintOption, ballots []PublishedBallot) string {
	opts := append([]FingerprintOption(nil), options...)
	sort.Slice(opts, func(i, j int) bool { return opts[i].ID < opts[j].ID })
	bs := append([]PublishedBallot(nil), ballots...)
	sort.Slice(bs, func(i, j int) bool { return bs[i].Receipt < bs[j].Receipt })
	payload := struct {
		Poll    int64               `json:"poll"`
		Method  Method              `json:"method"`
		Options []FingerprintOption `json:"options"`
		Ballots []PublishedBallot   `json:"ballots"`
	}{pollID, m, opts, bs}
	data, _ := json.Marshal(payload) // maps are encoded with sorted keys
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

const receiptAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789" // no 0/O, 1/I

// NewReceipt returns a random code like "K7QF-2M9X".
func NewReceipt() string {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		panic(err)
	}
	out := make([]byte, 0, 9)
	for i, b := range buf {
		if i == 4 {
			out = append(out, '-')
		}
		out = append(out, receiptAlphabet[int(b)%len(receiptAlphabet)])
	}
	return string(out)
}
