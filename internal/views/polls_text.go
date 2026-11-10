package views

import (
	"context"

	"bookclub/internal/store"
	"bookclub/internal/voting"
)

// rulesText describes a vote's rules in plain language.
func rulesText(ctx context.Context, p *store.Poll) []string {
	m := p.Method
	s := p.Rules()
	var out []string
	switch m {
	case voting.Single:
		out = append(out, t(ctx, "Each member votes for one option. Most votes wins."))
	case voting.Multi:
		if s.MaxChoices == 0 {
			out = append(out, t(ctx, "Each member can vote for as many options as they like (approval voting). Most votes wins."))
		} else {
			out = append(out, tn(ctx, s.MaxChoices, "Each member can vote for up to %d option. Most votes wins.", "Each member can vote for up to %d options. Most votes wins."))
		}
	case voting.Points:
		if s.MaxPerOption > 0 {
			out = append(out, tf(ctx, "Each member has %d points to spread across the options, at most %d on any one option. Most points wins.", s.PointsTotal, s.MaxPerOption))
		} else {
			out = append(out, tf(ctx, "Each member has %d points to spread across the options. Most points wins.", s.PointsTotal))
		}
	case voting.Ranked:
		if s.MaxRanks > 0 {
			out = append(out, tf(ctx, "Each member ranks the options in order of preference (up to %d).", s.MaxRanks))
		} else {
			out = append(out, t(ctx, "Each member ranks the options in order of preference."))
		}
		if s.RankedCount == voting.CountBorda {
			out = append(out, t(ctx, "Counted with points by rank (Borda): with N options, a 1st choice is worth N points, a 2nd choice N−1, and so on. Unranked options get 0. Most points wins."))
		} else {
			out = append(out, t(ctx, "Counted by instant runoff: if no option has more than half of the first choices, the option(s) with the fewest are eliminated and those ballots move to their next choice. This repeats until one option has a majority."))
			if s.Winners > 1 {
				out = append(out, t(ctx, "For each further pick, the count is re-run from scratch with the earlier winners removed."))
			}
		}
	case voting.Informal:
		out = append(out, t(ctx, "No ballots. Talk it out (or let someone pick) and record the decision here."))
	}
	if s.Winners > 1 {
		out = append(out, tf(ctx, "This vote picks %d options.", s.Winners))
	}
	if m.HasBallots() {
		if s.Secret {
			out = append(out, t(ctx, "Secret ballots: when voting closes, every ballot is published without names. Each voter gets a receipt code to find their own ballot."))
		} else {
			out = append(out, t(ctx, "Open ballots: when voting closes, every ballot is published with the voter's name."))
		}
		if s.LiveResults {
			out = append(out, t(ctx, "Running totals are visible while voting is open."))
		} else {
			out = append(out, t(ctx, "Results are hidden until voting closes."))
		}
		if s.NoOwnNominations {
			out = append(out, t(ctx, "You can't vote for options you suggested yourself."))
		}
	}
	if s.MaxNominations > 0 {
		out = append(out, tn(ctx, s.MaxNominations, "Each member can suggest up to %d option.", "Each member can suggest up to %d options."))
	}
	if s.LateNominations && m.HasBallots() {
		out = append(out, t(ctx, "Suggestions can still be added after voting opens."))
	}
	return out
}

// noteText explains a counting note.
func noteText(ctx context.Context, d PollData, n voting.Note) string {
	switch n.Kind {
	case voting.NoteNoOptions:
		return t(ctx, "There are no options to count.")
	case voting.NoteNoBallots:
		return t(ctx, "No ballots were cast.")
	case voting.NoteNoneReceived:
		return t(ctx, "Nobody received any votes or points.")
	case voting.NoteFewReceived:
		return tn(ctx, n.Value, "Only %d option received anything.", "Only %d options received anything.")
	case voting.NoteTie:
		return tf(ctx, "Tie with %s each between: %s. Record the decision below once you've settled it.",
			unitCount(ctx, n.Value, d.Result.Unit), d.labels(ctx, n.Options))
	case voting.NoteRunoffTie:
		return t(ctx, "The count ended in a tie. Record the decision below once you've settled it.")
	}
	return ""
}

// roundText explains how a round of instant runoff ended.
func roundText(ctx context.Context, d PollData, r voting.Round) string {
	switch r.Outcome {
	case voting.OutcomeOnlyOneLeft:
		return tf(ctx, "%s is the only option left.", q(ctx, d.Label(r.Counts[0].OptionID)))
	case voting.OutcomeMajority:
		return tf(ctx, "%s has a majority: %d of %d active ballots.", q(ctx, d.Label(r.Counts[0].OptionID)), r.Counts[0].Value, r.Active)
	case voting.OutcomeNoActive:
		return t(ctx, "No ballot ranks any of the remaining options, so they are tied.")
	case voting.OutcomeAllTied:
		return tf(ctx, "All remaining options are tied with %d each.", r.Lowest)
	case voting.OutcomeEliminated:
		if len(r.Eliminated) == 1 {
			return tf(ctx, "No majority (more than %d needed). %s has the fewest (%d) and is eliminated.", r.Active/2, q(ctx, d.Label(r.Eliminated[0])), r.Lowest)
		}
		return tf(ctx, "No majority (more than %d needed). %s are tied for fewest (%d) and are all eliminated.", r.Active/2, d.labels(ctx, r.Eliminated), r.Lowest)
	}
	return ""
}

func (d PollData) labels(ctx context.Context, ids []int64) string {
	names := make([]string, len(ids))
	for i, id := range ids {
		names[i] = d.Label(id)
	}
	out := ""
	for i, n := range names {
		if i > 0 {
			out += ", "
		}
		out += q(ctx, n)
	}
	return out
}
