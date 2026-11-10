package voting

import (
	"math/rand"
	"reflect"
	"testing"
)

var opts = []Option{{1, "Alpha"}, {2, "Bravo"}, {3, "Charlie"}, {4, "Delta"}}

func validSet() map[int64]bool { return map[int64]bool{1: true, 2: true, 3: true, 4: true} }

func ranked(ids ...int64) Ballot { return Ballot{Ranking: ids} }

func TestSingleTally(t *testing.T) {
	bs := []Ballot{{Choices: []int64{1}}, {Choices: []int64{2}}, {Choices: []int64{1}}}
	r := Tally(Single, Settings{}, opts, bs)
	if !reflect.DeepEqual(r.Winners, []int64{1}) {
		t.Fatalf("winners = %v", r.Winners)
	}
	if r.Totals[0].OptionID != 1 || r.Totals[0].Value != 2 {
		t.Fatalf("totals = %+v", r.Totals)
	}
}

func TestSingleTie(t *testing.T) {
	bs := []Ballot{{Choices: []int64{1}}, {Choices: []int64{2}}}
	r := Tally(Single, Settings{}, opts, bs)
	if len(r.Winners) != 0 || !reflect.DeepEqual(r.Tied, []int64{1, 2}) {
		t.Fatalf("winners=%v tied=%v", r.Winners, r.Tied)
	}
}

func TestMultiTwoWinnersWithTieForSecond(t *testing.T) {
	bs := []Ballot{
		{Choices: []int64{1, 2}},
		{Choices: []int64{1, 3}},
		{Choices: []int64{1}},
	}
	r := Tally(Multi, Settings{Winners: 2}, opts, bs)
	if !reflect.DeepEqual(r.Winners, []int64{1}) {
		t.Fatalf("winners = %v", r.Winners)
	}
	if !reflect.DeepEqual(r.Tied, []int64{2, 3}) {
		t.Fatalf("tied = %v", r.Tied)
	}
}

func TestPointsTally(t *testing.T) {
	bs := []Ballot{
		{Points: map[int64]int{1: 7, 2: 3}},
		{Points: map[int64]int{2: 10}},
	}
	r := Tally(Points, Settings{PointsTotal: 10}, opts, bs)
	if !reflect.DeepEqual(r.Winners, []int64{2}) || r.Totals[0].Value != 13 {
		t.Fatalf("winners=%v totals=%+v", r.Winners, r.Totals)
	}
}

func TestBorda(t *testing.T) {
	// 4 options: 1st = 4 pts, 2nd = 3, 3rd = 2, 4th = 1.
	bs := []Ballot{ranked(1, 2, 3), ranked(2, 1), ranked(2, 3)}
	r := Tally(Ranked, Settings{RankedCount: CountBorda}, opts, bs)
	// Alpha: 4 + 3 = 7, Bravo: 3 + 4 + 4 = 11, Charlie: 2 + 3 = 5
	want := map[int64]int{1: 7, 2: 11, 3: 5, 4: 0}
	for _, tot := range r.Totals {
		if want[tot.OptionID] != tot.Value {
			t.Fatalf("option %d: got %d want %d", tot.OptionID, tot.Value, want[tot.OptionID])
		}
	}
	if !reflect.DeepEqual(r.Winners, []int64{2}) {
		t.Fatalf("winners = %v", r.Winners)
	}
}

func TestIRVMajorityFirstRound(t *testing.T) {
	bs := []Ballot{ranked(1), ranked(1), ranked(2)}
	r := Tally(Ranked, Settings{}, opts, bs)
	if !reflect.DeepEqual(r.Winners, []int64{1}) || len(r.Runs[0].Rounds) != 1 {
		t.Fatalf("winners=%v runs=%+v", r.Winners, r.Runs)
	}
}

func TestIRVTransfers(t *testing.T) {
	// Round 1: A=2, B=2, C=1 -> C eliminated, its ballot goes to B -> B wins 3/5.
	bs := []Ballot{ranked(1), ranked(1), ranked(2), ranked(2), ranked(3, 2)}
	r := Tally(Ranked, Settings{}, opts[:3], bs)
	if !reflect.DeepEqual(r.Winners, []int64{2}) {
		t.Fatalf("winners = %v", r.Winners)
	}
	if got := r.Runs[0].Rounds[0].Eliminated; !reflect.DeepEqual(got, []int64{3}) {
		t.Fatalf("eliminated round 1 = %v", got)
	}
}

func TestIRVExhaustedBallots(t *testing.T) {
	// Round 1: A=3, B=2, C=1 of 6 — no majority, C eliminated. C's voter ranked
	// only C, so that ballot is exhausted and A wins with 3 of 5 active ballots.
	bs := []Ballot{ranked(1), ranked(1), ranked(1), ranked(2), ranked(2), ranked(3)}
	r := Tally(Ranked, Settings{}, opts[:3], bs)
	if !reflect.DeepEqual(r.Winners, []int64{1}) {
		t.Fatalf("winners = %v (runs %+v)", r.Winners, r.Runs)
	}
	last := r.Runs[0].Rounds[len(r.Runs[0].Rounds)-1]
	if last.Exhausted != 1 {
		t.Fatalf("exhausted = %d", last.Exhausted)
	}
}

func TestIRVTie(t *testing.T) {
	bs := []Ballot{ranked(1, 2), ranked(2, 1)}
	r := Tally(Ranked, Settings{}, opts[:2], bs)
	if len(r.Winners) != 0 || !reflect.DeepEqual(r.Tied, []int64{1, 2}) {
		t.Fatalf("winners=%v tied=%v", r.Winners, r.Tied)
	}
}

func TestIRVSecondSeat(t *testing.T) {
	bs := []Ballot{ranked(1, 3), ranked(1, 3), ranked(1, 2), ranked(2, 3), ranked(3, 2)}
	r := Tally(Ranked, Settings{Winners: 2}, opts[:3], bs)
	// Seat 1: A wins 3/5. Seat 2 (A removed): C=3 (two A->C, one C), B=2 -> C wins.
	if !reflect.DeepEqual(r.Winners, []int64{1, 3}) {
		t.Fatalf("winners = %v", r.Winners)
	}
}

func TestTallyIsOrderIndependent(t *testing.T) {
	bs := []Ballot{ranked(1, 2, 3), ranked(2, 3), ranked(3, 1), ranked(1, 3), ranked(2, 1), ranked(3, 2, 1), ranked(4, 3)}
	want := Tally(Ranked, Settings{Winners: 2}, opts, bs)
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 50; i++ {
		shuffled := append([]Ballot(nil), bs...)
		rng.Shuffle(len(shuffled), func(a, b int) { shuffled[a], shuffled[b] = shuffled[b], shuffled[a] })
		got := Tally(Ranked, Settings{Winners: 2}, opts, shuffled)
		if !reflect.DeepEqual(got.Winners, want.Winners) || !reflect.DeepEqual(got.Tied, want.Tied) {
			t.Fatalf("result depends on ballot order: %v vs %v", got.Winners, want.Winners)
		}
	}
}

func TestCleanRules(t *testing.T) {
	v := validSet()
	own := map[int64]bool{3: true}
	cases := []struct {
		name string
		m    Method
		s    Settings
		b    Ballot
		ok   bool
	}{
		{"single ok", Single, Settings{}, Ballot{Choices: []int64{1}}, true},
		{"single two", Single, Settings{}, Ballot{Choices: []int64{1, 2}}, false},
		{"unknown option", Single, Settings{}, Ballot{Choices: []int64{99}}, false},
		{"multi limit", Multi, Settings{MaxChoices: 2}, Ballot{Choices: []int64{1, 2, 4}}, false},
		{"multi unlimited", Multi, Settings{}, Ballot{Choices: []int64{1, 2, 4}}, true},
		{"multi dupes", Multi, Settings{}, Ballot{Choices: []int64{1, 1}}, false},
		{"points over", Points, Settings{PointsTotal: 5}, Ballot{Points: map[int64]int{1: 3, 2: 3}}, false},
		{"points cap", Points, Settings{PointsTotal: 10, MaxPerOption: 4}, Ballot{Points: map[int64]int{1: 5}}, false},
		{"points ok", Points, Settings{PointsTotal: 10}, Ballot{Points: map[int64]int{1: 5, 2: 0}}, true},
		{"points negative", Points, Settings{PointsTotal: 10}, Ballot{Points: map[int64]int{1: -1, 2: 5}}, false},
		{"ranked max", Ranked, Settings{MaxRanks: 2}, ranked(1, 2, 4), false},
		{"ranked dupes", Ranked, Settings{}, ranked(1, 2, 1), false},
		{"own nomination blocked", Single, Settings{NoOwnNominations: true}, Ballot{Choices: []int64{3}}, false},
		{"own nomination allowed", Single, Settings{}, Ballot{Choices: []int64{3}}, true},
	}
	for _, c := range cases {
		_, err := Clean(c.m, c.s, c.b, v, own)
		if (err == nil) != c.ok {
			t.Errorf("%s: err = %v, want ok=%v", c.name, err, c.ok)
		}
	}
}

func TestFingerprintStableAndSensitive(t *testing.T) {
	os := []FingerprintOption{{ID: 1, BookID: 10}, {ID: 2, Label: "Sci-fi month"}}
	bs := []PublishedBallot{
		{Receipt: "AAAA-AAAA", Ballot: Ballot{Points: map[int64]int{1: 3, 2: 7}}},
		{Receipt: "BBBB-BBBB", Ballot: Ballot{Points: map[int64]int{2: 10}}},
	}
	a := Fingerprint(5, Points, os, bs)
	b := Fingerprint(5, Points, []FingerprintOption{os[1], os[0]}, []PublishedBallot{bs[1], bs[0]})
	if a != b {
		t.Fatal("fingerprint depends on ordering")
	}
	bs[1].Ballot.Points[2] = 9
	if Fingerprint(5, Points, os, bs) == a {
		t.Fatal("fingerprint did not change when a ballot changed")
	}
}

func TestReceiptFormat(t *testing.T) {
	r := NewReceipt()
	if len(r) != 9 || r[4] != '-' {
		t.Fatalf("bad receipt %q", r)
	}
}
