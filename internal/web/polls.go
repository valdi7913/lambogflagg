package web

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"bookclub/internal/store"
	"bookclub/internal/views"
	"bookclub/internal/voting"
)

func (s *Server) listPolls(w http.ResponseWriter, r *http.Request) error {
	all, err := s.st.ListPolls(r.Context())
	if err != nil {
		return err
	}
	voted, err := s.st.VotedPollIDs(r.Context(), user(r).ID)
	if err != nil {
		return err
	}
	d := views.PollsData{Voted: voted}
	for _, p := range all {
		if p.Status == store.StatusClosed {
			d.Closed = append(d.Closed, p)
		} else {
			d.Open = append(d.Open, p)
		}
	}
	return ok(w, r, views.PollsPage(d))
}

// ---- Create / edit ----

func (s *Server) newPoll(w http.ResponseWriter, r *http.Request) error {
	meetings, err := s.st.MeetingsForSelect(r.Context())
	if err != nil {
		return err
	}
	p := &store.Poll{Method: voting.Single, Settings: voting.Settings{Winners: 1, PointsTotal: 10}}
	if mid, _ := strconv.ParseInt(r.URL.Query().Get("meeting"), 10, 64); mid > 0 {
		p.MeetingID = &mid
	}
	return ok(w, r, views.PollFormPage(views.PollFormData{Poll: p, Meetings: meetings, IsNew: true, Errors: map[string]string{}}))
}

// readRules parses the voting method and rules, keeping only the settings
// that apply to the chosen method.
func readRules(r *http.Request) (voting.Method, voting.Settings) {
	m := voting.Method(r.FormValue("method"))
	s := voting.Settings{
		Winners:          formInt(r, "winners"),
		Secret:           formBool(r, "secret"),
		LiveResults:      formBool(r, "live_results"),
		LateNominations:  formBool(r, "late_nominations"),
		MaxNominations:   formInt(r, "max_nominations"),
		NoOwnNominations: formBool(r, "no_own_nominations"),
	}
	switch m {
	case voting.Multi:
		s.MaxChoices = formInt(r, "max_choices")
	case voting.Points:
		s.PointsTotal = formInt(r, "points_total")
		s.MaxPerOption = formInt(r, "max_per_option")
	case voting.Ranked:
		s.RankedCount = r.FormValue("ranked_count")
		s.MaxRanks = formInt(r, "max_ranks")
	case voting.Informal:
		s.Secret, s.LiveResults, s.LateNominations, s.NoOwnNominations = false, false, false, false
	}
	return m, s.Normalize(m)
}

func (s *Server) readPollForm(r *http.Request, p *store.Poll, rulesLocked bool) (string, map[string]string) {
	errs := map[string]string{}
	p.Title = formStr(r, "title")
	p.Description = formStr(r, "description")
	p.MeetingID = optionalID(formInt64(r, "meeting_id"))
	if p.Title == "" {
		errs["title"] = "Give the vote a name."
	}
	if !rulesLocked {
		p.Method, p.Settings = readRules(r)
		if !p.Method.Valid() {
			errs["method"] = "Choose how you want to decide."
		}
	}
	raw := formStr(r, "closes_at")
	t, err := s.parseLocalTime(raw)
	if err != nil {
		errs["closes_at"] = "Enter a valid date and time."
	}
	p.ClosesAt = t
	return raw, errs
}

func (s *Server) createPoll(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	u := user(r)
	p := &store.Poll{CreatedBy: &u.ID, Status: store.StatusNominating}
	raw, errs := s.readPollForm(r, p, false)
	if p.MeetingID != nil {
		if _, err := s.st.GetMeeting(ctx, *p.MeetingID); err != nil {
			p.MeetingID = nil
		}
	}
	startVoting := r.FormValue("start") == "voting"
	if p.ClosesAt != nil && !p.ClosesAt.After(timeNow()) {
		errs["closes_at"] = "Pick a time in the future (or leave it empty)."
	}
	if len(errs) > 0 {
		meetings, _ := s.st.MeetingsForSelect(ctx)
		return render(w, r, http.StatusUnprocessableEntity, views.PollFormPage(views.PollFormData{
			Poll: p, Meetings: meetings, ClosesAt: raw, IsNew: true, StartVoting: startVoting, Errors: errs}))
	}
	if startVoting && p.Method.HasBallots() {
		p.Status = store.StatusVoting
	}
	err := s.st.Tx(ctx, func(q store.Querier) error {
		if err := s.st.CreatePoll(ctx, q, p); err != nil {
			return err
		}
		return s.st.Log(ctx, q, u.ID, "create", "poll", p.ID,
			fmt.Sprintf("started the vote %s (%s)", quote(p.Title), strings.ToLower(p.Method.Label())), nil)
	})
	if err != nil {
		return err
	}
	s.flash(w, "ok", "Vote created. Now add some options.")
	return redirect(w, r, fmt.Sprintf("/polls/%d", p.ID))
}

func (s *Server) editPoll(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	p, err := s.st.GetPoll(r.Context(), id)
	if err != nil {
		return err
	}
	meetings, err := s.st.MeetingsForSelect(r.Context())
	if err != nil {
		return err
	}
	return ok(w, r, views.PollFormPage(views.PollFormData{
		Poll: p, Meetings: meetings, ClosesAt: views.InputDateTime(p.ClosesAt), Locked: p.BallotCount > 0, Errors: map[string]string{}}))
}

func (s *Server) updatePoll(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	var formErrs map[string]string
	var formRaw string
	var edited *store.Poll
	err = s.st.Tx(ctx, func(q store.Querier) error {
		old, err := s.st.LockPoll(ctx, q, id)
		if err != nil {
			return err
		}
		p := *old
		locked := old.BallotCount > 0
		formRaw, formErrs = s.readPollForm(r, &p, locked)
		edited = &p
		deadlineChanged := timeStr(old.ClosesAt, s.cfg.Location) != timeStr(p.ClosesAt, s.cfg.Location)
		if deadlineChanged && p.ClosesAt != nil && !p.ClosesAt.After(timeNow()) && old.Status != store.StatusClosed {
			formErrs["closes_at"] = "Pick a time in the future (or leave it empty)."
		}
		if len(formErrs) > 0 {
			return nil
		}
		loc := s.cfg.Location
		changes := store.Diff([][3]string{
			{"title", old.Title, p.Title},
			{"details", old.Description, p.Description},
			{"meeting", meetingIDStr(old.MeetingID), meetingIDStr(p.MeetingID)},
			{"closes", timeStr(old.ClosesAt, loc), timeStr(p.ClosesAt, loc)},
			{"method", old.Method.Label(), p.Method.Label()},
			{"rules", strings.Join(old.Settings.Describe(old.Method), " "), strings.Join(p.Settings.Describe(p.Method), " ")},
		})
		if len(changes) == 0 {
			return nil
		}
		if err := s.st.UpdatePoll(ctx, q, &p); err != nil {
			return err
		}
		return s.st.Log(ctx, q, user(r).ID, "update", "poll", id, "edited the vote "+quote(p.Title), changes)
	})
	if err != nil {
		return err
	}
	if len(formErrs) > 0 {
		meetings, _ := s.st.MeetingsForSelect(ctx)
		return render(w, r, http.StatusUnprocessableEntity, views.PollFormPage(views.PollFormData{
			Poll: edited, Meetings: meetings, ClosesAt: formRaw, Locked: edited.BallotCount > 0, Errors: formErrs}))
	}
	s.flash(w, "ok", "Saved.")
	return redirect(w, r, fmt.Sprintf("/polls/%d", id))
}

func meetingIDStr(id *int64) string {
	if id == nil {
		return ""
	}
	return "#" + strconv.FormatInt(*id, 10)
}

func (s *Server) deletePoll(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	p, err := s.st.GetPoll(ctx, id)
	if err != nil {
		return err
	}
	err = s.st.Tx(ctx, func(q store.Querier) error {
		if err := s.st.DeletePoll(ctx, q, id); err != nil {
			return err
		}
		return s.st.Log(ctx, q, user(r).ID, "delete", "poll", id,
			fmt.Sprintf("deleted the vote %s (it had %s)", quote(p.Title), pluralize(p.BallotCount, "ballot", "ballots")), nil)
	})
	if err != nil {
		return err
	}
	s.flash(w, "ok", "Vote deleted.")
	return redirect(w, r, "/polls")
}

func (s *Server) duplicatePoll(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	u := user(r)
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	var copyID int64
	err = s.st.Tx(ctx, func(q store.Querier) error {
		src, err := s.st.LockPoll(ctx, q, id)
		if err != nil {
			return err
		}
		opts, err := s.st.PollOptions(ctx, q, id)
		if err != nil {
			return err
		}
		cp := &store.Poll{
			Title: src.Title + " (copy)", Description: src.Description, MeetingID: src.MeetingID,
			Method: src.Method, Settings: src.Settings, Status: store.StatusNominating, CreatedBy: &u.ID,
		}
		if err := s.st.CreatePoll(ctx, q, cp); err != nil {
			return err
		}
		for _, o := range opts {
			no := &store.PollOption{PollID: cp.ID, BookID: o.BookID, Label: o.Label, Pitch: o.Pitch, NominatedBy: o.NominatedBy}
			if err := s.st.AddOption(ctx, q, no); err != nil {
				return err
			}
		}
		copyID = cp.ID
		return s.st.Log(ctx, q, u.ID, "create", "poll", cp.ID,
			fmt.Sprintf("started %s as a copy of %s", quote(cp.Title), quote(src.Title)), nil)
	})
	if err != nil {
		return err
	}
	s.flash(w, "ok", "Copy created with the same options and no ballots. Use Edit to change the name or rules.")
	return redirect(w, r, fmt.Sprintf("/polls/%d", copyID))
}

func pluralize(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

// ---- Viewing ----

func (s *Server) showPoll(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	me := user(r)
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	p, err := s.st.GetPoll(ctx, id)
	if err != nil {
		return err
	}
	if s.closeIfDue(ctx, p) {
		if p, err = s.st.GetPoll(ctx, id); err != nil {
			return err
		}
	}
	d := views.PollData{ViewerID: me.ID, Poll: p, Rules: p.Settings.Describe(p.Method), Voted: map[int64]bool{}}
	if d.Options, err = s.st.PollOptions(ctx, s.st.Pool, id); err != nil {
		return err
	}
	if d.Members, err = s.st.ActiveUsers(ctx); err != nil {
		return err
	}
	ballots, err := s.st.Ballots(ctx, s.st.Pool, id)
	if err != nil {
		return err
	}
	for _, b := range ballots {
		d.Voted[b.UserID] = true
		if b.UserID == me.ID {
			d.MyBallot = b
		}
	}
	for _, o := range d.Options {
		if o.NominatedBy != nil && *o.NominatedBy == me.ID {
			d.MyNominations++
		}
	}

	closed := p.Status == store.StatusClosed
	d.ShowResults = p.Method.HasBallots() && (closed || (p.Status == store.StatusVoting && p.Settings.LiveResults))
	if d.ShowResults {
		d.Result = tally(p, d.Options, ballots)
	}
	if closed && p.Method.HasBallots() {
		d.Ballots = ballots
		d.CurrentFingerprint = fingerprint(p, d.Options, ballots)
	}
	if p.AcceptsNominations() {
		books, err := s.st.BooksForSelect(ctx)
		if err != nil {
			return err
		}
		taken := map[int64]bool{}
		for _, o := range d.Options {
			if o.BookID != nil {
				taken[*o.BookID] = true
			}
		}
		for _, b := range books {
			if !taken[b.ID] {
				d.Nominatable = append(d.Nominatable, b)
			}
		}
	}
	if d.Comments, err = s.comments(ctx, "poll", id); err != nil {
		return err
	}
	if d.History, err = s.st.SubjectActivity(ctx, "poll", id); err != nil {
		return err
	}
	return ok(w, r, views.PollPage(d))
}

func votingOptions(opts []*store.PollOption) []voting.Option {
	out := make([]voting.Option, len(opts))
	for i, o := range opts {
		out[i] = voting.Option{ID: o.ID, Label: o.Name()}
	}
	return out
}

func tally(p *store.Poll, opts []*store.PollOption, ballots []*store.BallotRow) voting.Result {
	bs := make([]voting.Ballot, len(ballots))
	for i, b := range ballots {
		bs[i] = b.Data
	}
	return voting.Tally(p.Method, p.Settings, votingOptions(opts), bs)
}

func fingerprint(p *store.Poll, opts []*store.PollOption, ballots []*store.BallotRow) string {
	fos := make([]voting.FingerprintOption, len(opts))
	for i, o := range opts {
		fos[i] = voting.FingerprintOption{ID: o.ID, Label: o.Label}
		if o.BookID != nil {
			fos[i].BookID = *o.BookID
		}
	}
	pbs := make([]voting.PublishedBallot, len(ballots))
	for i, b := range ballots {
		pbs[i] = voting.PublishedBallot{Receipt: b.Receipt, Ballot: b.Data}
		if !p.Settings.Secret {
			pbs[i].VoterID = b.UserID
		}
	}
	return voting.Fingerprint(p.ID, p.Method, fos, pbs)
}

// ---- Status changes ----

// closePoll closes voting and records the ballot fingerprint. Must be called
// inside a transaction holding the poll lock. userID 0 means "automatically".
func (s *Server) closePoll(ctx context.Context, q store.Querier, p *store.Poll, userID int64) error {
	opts, err := s.st.PollOptions(ctx, q, p.ID)
	if err != nil {
		return err
	}
	ballots, err := s.st.Ballots(ctx, q, p.ID)
	if err != nil {
		return err
	}
	fp := fingerprint(p, opts, ballots)
	if err := s.st.SetPollStatus(ctx, q, p.ID, store.StatusClosed, fp); err != nil {
		return err
	}
	res := tally(p, opts, ballots)
	summary := fmt.Sprintf("closed voting in %s with %s", quote(p.Title), pluralize(len(ballots), "ballot", "ballots"))
	if userID == 0 {
		summary = fmt.Sprintf("closed voting in %s automatically at its deadline, with %s", quote(p.Title), pluralize(len(ballots), "ballot", "ballots"))
	}
	var names []string
	for _, id := range res.Winners {
		for _, o := range opts {
			if o.ID == id {
				names = append(names, quote(o.Name()))
			}
		}
	}
	if len(names) > 0 {
		summary += "; counted winner: " + strings.Join(names, ", ")
	} else if len(res.Tied) > 0 {
		summary += "; the count is tied"
	}
	summary += ". Fingerprint " + fp[:12] + "…"
	return s.st.Log(ctx, q, userID, "close", "poll", p.ID, summary, nil)
}

// closeIfDue closes a poll whose deadline has passed. Reports whether it did.
func (s *Server) closeIfDue(ctx context.Context, p *store.Poll) bool {
	if p.Status != store.StatusVoting || p.ClosesAt == nil || p.ClosesAt.After(timeNow()) {
		return false
	}
	closed := false
	err := s.st.Tx(ctx, func(q store.Querier) error {
		cur, err := s.st.LockPoll(ctx, q, p.ID)
		if err != nil {
			return err
		}
		if cur.Status != store.StatusVoting || cur.ClosesAt == nil || cur.ClosesAt.After(timeNow()) {
			return nil
		}
		closed = true
		return s.closePoll(ctx, q, cur, 0)
	})
	if err != nil {
		slog.Error("auto-close poll", "poll", p.ID, "err", err)
		return false
	}
	return closed
}

func (s *Server) closeDuePolls(ctx context.Context) {
	ids, err := s.st.DuePolls(ctx)
	if err != nil {
		slog.Error("due polls", "err", err)
		return
	}
	for _, id := range ids {
		if p, err := s.st.GetPoll(ctx, id); err == nil {
			s.closeIfDue(ctx, p)
		}
	}
}

func (s *Server) setPollStatus(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	u := user(r)
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	target := r.FormValue("status")
	var userMsg string
	err = s.st.Tx(ctx, func(q store.Querier) error {
		p, err := s.st.LockPoll(ctx, q, id)
		if err != nil {
			return err
		}
		from := p.Status
		switch {
		case target == store.StatusVoting && from == store.StatusNominating && p.Method.HasBallots():
			if p.OptionCount == 0 {
				return errStatus(http.StatusConflict, "Add at least one option before opening voting.")
			}
			if err := s.st.SetPollStatus(ctx, q, id, store.StatusVoting, ""); err != nil {
				return err
			}
			userMsg = "Voting is open."
			return s.st.Log(ctx, q, u.ID, "status", "poll", id, "opened voting in "+quote(p.Title), nil)

		case target == store.StatusVoting && from == store.StatusClosed && p.Method.HasBallots():
			if p.ClosesAt != nil && !p.ClosesAt.After(timeNow()) {
				p.ClosesAt = nil // a past deadline would close it again immediately
				if err := s.st.UpdatePoll(ctx, q, p); err != nil {
					return err
				}
			}
			if err := s.st.SetPollStatus(ctx, q, id, store.StatusVoting, ""); err != nil {
				return err
			}
			userMsg = "Voting reopened."
			return s.st.Log(ctx, q, u.ID, "status", "poll", id,
				fmt.Sprintf("reopened voting in %s (the fingerprint recorded at closing, %s…, no longer applies)", quote(p.Title), short(p.Fingerprint, 12)), nil)

		case target == store.StatusClosed && from == store.StatusVoting:
			userMsg = "Voting closed. The ballots are published below."
			return s.closePoll(ctx, q, p, u.ID)

		case target == store.StatusNominating && from == store.StatusVoting && p.BallotCount == 0:
			if err := s.st.SetPollStatus(ctx, q, id, store.StatusNominating, ""); err != nil {
				return err
			}
			userMsg = "Back to collecting suggestions."
			return s.st.Log(ctx, q, u.ID, "status", "poll", id, "paused voting in "+quote(p.Title)+" to collect more suggestions", nil)

		case target == store.StatusNominating && from == store.StatusClosed && !p.Method.HasBallots():
			if err := s.st.SetPollStatus(ctx, q, id, store.StatusNominating, ""); err != nil {
				return err
			}
			userMsg = "Reopened."
			return s.st.Log(ctx, q, u.ID, "status", "poll", id, "reopened "+quote(p.Title), nil)
		}
		return errStatus(http.StatusConflict, "That change isn't possible right now — someone may have just changed this vote. Reload and try again.")
	})
	var he httpError
	if errors.As(err, &he) && he.status == http.StatusConflict {
		s.flash(w, "error", he.msg)
		return redirect(w, r, fmt.Sprintf("/polls/%d", id))
	}
	if err != nil {
		return err
	}
	s.flash(w, "ok", userMsg)
	return redirect(w, r, fmt.Sprintf("/polls/%d", id))
}

func short(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// ---- Options ----

// nominateBookTx suggests a book in a poll in its own transaction.
func (s *Server) nominateBookTx(ctx context.Context, pollID, bookID int64, pitch string, userID int64) error {
	return s.st.Tx(ctx, func(q store.Querier) error {
		return s.addOption(ctx, q, pollID, &store.PollOption{PollID: pollID, BookID: &bookID, Pitch: pitch, NominatedBy: &userID})
	})
}

func (s *Server) addOption(ctx context.Context, q store.Querier, pollID int64, o *store.PollOption) error {
	p, err := s.st.LockPoll(ctx, q, pollID)
	if err != nil {
		return err
	}
	if !p.AcceptsNominations() {
		return errStatus(http.StatusConflict, "this vote isn't taking suggestions right now")
	}
	if max := p.Settings.MaxNominations; max > 0 {
		n, err := s.st.CountNominations(ctx, q, pollID, *o.NominatedBy)
		if err != nil {
			return err
		}
		if n >= max {
			return errStatus(http.StatusConflict, fmt.Sprintf("you've already made %s", pluralize(max, "suggestion", "suggestions")))
		}
	}
	name := o.Label
	if o.BookID != nil {
		b, err := s.st.GetBook(ctx, *o.BookID)
		if err != nil {
			return errStatus(http.StatusConflict, "that book doesn't exist")
		}
		name = b.Title
	}
	if err := s.st.AddOption(ctx, q, o); err != nil {
		if errors.Is(err, store.ErrDuplicate) {
			return errStatus(http.StatusConflict, "that book has already been suggested in this vote")
		}
		return err
	}
	return s.st.Log(ctx, q, *o.NominatedBy, "update", "poll", pollID, fmt.Sprintf("suggested %s in %s", quote(name), quote(p.Title)), nil)
}

func (s *Server) nominate(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	u := user(r)
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	o := &store.PollOption{PollID: id, NominatedBy: &u.ID, Pitch: formStr(r, "pitch")}
	if bid := formInt64(r, "book_id"); bid > 0 {
		o.BookID = &bid
	} else if label := formStr(r, "label"); label != "" {
		o.Label = label
	} else {
		s.flash(w, "error", "Choose a book or type an option.")
		return redirect(w, r, fmt.Sprintf("/polls/%d#poll-options", id))
	}
	err = s.st.Tx(ctx, func(q store.Querier) error { return s.addOption(ctx, q, id, o) })
	var he httpError
	if errors.As(err, &he) {
		s.flash(w, "error", "Couldn't add that: "+he.msg+".")
		return redirect(w, r, fmt.Sprintf("/polls/%d#poll-options", id))
	}
	if err != nil {
		return err
	}
	s.flash(w, "ok", "Suggestion added.")
	return redirect(w, r, fmt.Sprintf("/polls/%d#poll-options", id))
}

func ballotMentions(b voting.Ballot, optionID int64) bool {
	for _, id := range b.Choices {
		if id == optionID {
			return true
		}
	}
	for _, id := range b.Ranking {
		if id == optionID {
			return true
		}
	}
	return b.Points[optionID] > 0
}

func (s *Server) removeOption(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	optID, err := pathID(r, "optionID")
	if err != nil {
		return err
	}
	err = s.st.Tx(ctx, func(q store.Querier) error {
		p, err := s.st.LockPoll(ctx, q, id)
		if err != nil {
			return err
		}
		if p.Status == store.StatusClosed {
			return errStatus(http.StatusConflict, "Options can't be removed after a vote has closed.")
		}
		opts, err := s.st.PollOptions(ctx, q, id)
		if err != nil {
			return err
		}
		var target *store.PollOption
		for _, o := range opts {
			if o.ID == optID {
				target = o
			}
		}
		if target == nil {
			return errNotFound
		}
		ballots, err := s.st.Ballots(ctx, q, id)
		if err != nil {
			return err
		}
		for _, b := range ballots {
			if ballotMentions(b.Data, optID) {
				return errStatus(http.StatusConflict, "That option is already on someone's ballot, so it can't be removed. Duplicate the vote if you need to start over without it.")
			}
		}
		if err := s.st.RemoveOption(ctx, q, id, optID); err != nil {
			return err
		}
		return s.st.Log(ctx, q, user(r).ID, "update", "poll", id, fmt.Sprintf("removed %s from %s", quote(target.Name()), quote(p.Title)), nil)
	})
	var he httpError
	if errors.As(err, &he) && he.status == http.StatusConflict {
		s.flash(w, "error", he.msg)
		return redirect(w, r, fmt.Sprintf("/polls/%d#poll-options", id))
	}
	if err != nil {
		return err
	}
	s.flash(w, "ok", "Option removed.")
	return redirect(w, r, fmt.Sprintf("/polls/%d#poll-options", id))
}

// ---- Ballots ----

func readBallot(r *http.Request, m voting.Method, opts []*store.PollOption) (voting.Ballot, error) {
	var b voting.Ballot
	switch m {
	case voting.Single, voting.Multi:
		for _, v := range r.Form["choice"] {
			id, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				return b, errors.New("invalid choice")
			}
			b.Choices = append(b.Choices, id)
		}
	case voting.Points:
		b.Points = map[int64]int{}
		for _, o := range opts {
			v := strings.TrimSpace(r.FormValue("points_" + strconv.FormatInt(o.ID, 10)))
			if v == "" {
				continue
			}
			n, err := strconv.Atoi(v)
			if err != nil {
				return b, errors.New("points must be whole numbers")
			}
			b.Points[o.ID] = n
		}
	case voting.Ranked:
		type ranked struct {
			id   int64
			rank int
		}
		var rs []ranked
		used := map[int]bool{}
		for _, o := range opts {
			v := strings.TrimSpace(r.FormValue("rank_" + strconv.FormatInt(o.ID, 10)))
			if v == "" {
				continue
			}
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 {
				return b, errors.New("invalid rank")
			}
			if used[n] {
				return b, fmt.Errorf("two options are both ranked %d — give each a different rank", n)
			}
			used[n] = true
			rs = append(rs, ranked{o.ID, n})
		}
		// Gaps (e.g. 1, 2, 4) are fine: only the order matters.
		sort.Slice(rs, func(i, j int) bool { return rs[i].rank < rs[j].rank })
		for _, x := range rs {
			b.Ranking = append(b.Ranking, x.id)
		}
	}
	return b, nil
}

func (s *Server) castBallot(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	u := user(r)
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	if err := r.ParseForm(); err != nil {
		return errStatus(http.StatusBadRequest, "Couldn't read the form.")
	}
	var receipt string
	var updated bool
	err = s.st.Tx(ctx, func(q store.Querier) error {
		p, err := s.st.LockPoll(ctx, q, id)
		if err != nil {
			return err
		}
		if p.Status != store.StatusVoting || !p.Method.HasBallots() {
			return errStatus(http.StatusConflict, "Voting isn't open, so your ballot wasn't saved.")
		}
		if p.ClosesAt != nil && !p.ClosesAt.After(timeNow()) {
			return errStatus(http.StatusConflict, "Voting has just closed, so your ballot wasn't saved.")
		}
		opts, err := s.st.PollOptions(ctx, q, id)
		if err != nil {
			return err
		}
		valid, own := map[int64]bool{}, map[int64]bool{}
		for _, o := range opts {
			valid[o.ID] = true
			if o.NominatedBy != nil && *o.NominatedBy == u.ID {
				own[o.ID] = true
			}
		}
		raw, err := readBallot(r, p.Method, opts)
		if err != nil {
			return errStatus(http.StatusConflict, "Your ballot wasn't saved: "+err.Error()+".")
		}
		clean, err := voting.Clean(p.Method, p.Settings, raw, valid, own)
		if err != nil {
			return errStatus(http.StatusConflict, "Your ballot wasn't saved: "+err.Error()+".")
		}
		ballots, err := s.st.Ballots(ctx, q, id)
		if err != nil {
			return err
		}
		for _, b := range ballots {
			if b.UserID == u.ID {
				updated = true
			}
		}
		if receipt, err = s.st.UpsertBallot(ctx, q, id, u.ID, clean); err != nil {
			return err
		}
		verb := "voted in "
		if updated {
			verb = "changed their ballot in "
		}
		return s.st.Log(ctx, q, u.ID, "vote", "poll", id, verb+quote(p.Title), nil)
	})
	var he httpError
	if errors.As(err, &he) && he.status == http.StatusConflict {
		s.flash(w, "error", he.msg)
		return redirect(w, r, fmt.Sprintf("/polls/%d#ballot", id))
	}
	if err != nil {
		return err
	}
	msg := "Ballot cast. Your receipt code is " + receipt + "."
	if updated {
		msg = "Ballot updated. Your receipt code is still " + receipt + "."
	}
	s.flash(w, "ok", msg)
	return redirect(w, r, fmt.Sprintf("/polls/%d#ballot", id))
}

func (s *Server) withdrawBallot(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	u := user(r)
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	err = s.st.Tx(ctx, func(q store.Querier) error {
		p, err := s.st.LockPoll(ctx, q, id)
		if err != nil {
			return err
		}
		if p.Status != store.StatusVoting {
			return errStatus(http.StatusConflict, "Voting has closed, so ballots can't be withdrawn.")
		}
		if err := s.st.DeleteBallot(ctx, q, id, u.ID); err != nil {
			return err
		}
		return s.st.Log(ctx, q, u.ID, "vote", "poll", id, "withdrew their ballot from "+quote(p.Title), nil)
	})
	var he httpError
	if errors.As(err, &he) {
		s.flash(w, "error", he.msg)
		return redirect(w, r, fmt.Sprintf("/polls/%d", id))
	}
	if err != nil {
		return err
	}
	s.flash(w, "ok", "Ballot withdrawn.")
	return redirect(w, r, fmt.Sprintf("/polls/%d#ballot", id))
}

// ---- Decision ----

func (s *Server) recordDecision(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	u := user(r)
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	if err := r.ParseForm(); err != nil {
		return errStatus(http.StatusBadRequest, "Couldn't read the form.")
	}
	picked := map[int64]bool{}
	for _, v := range r.Form["chosen"] {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			picked[n] = true
		}
	}
	note := formStr(r, "note")
	addToMeeting := formBool(r, "add_to_meeting")
	offWishlist := formBool(r, "off_wishlist")

	err = s.st.Tx(ctx, func(q store.Querier) error {
		p, err := s.st.LockPoll(ctx, q, id)
		if err != nil {
			return err
		}
		if p.Method.HasBallots() && p.Status != store.StatusClosed {
			return errStatus(http.StatusConflict, "Close voting before recording a decision.")
		}
		opts, err := s.st.PollOptions(ctx, q, id)
		if err != nil {
			return err
		}
		var chosen []int64
		var names []string
		for _, o := range opts {
			if !picked[o.ID] {
				continue
			}
			chosen = append(chosen, o.ID)
			names = append(names, quote(o.Name()))
			if o.BookID == nil {
				continue
			}
			if addToMeeting && p.MeetingID != nil {
				err := s.st.AddMeetingBook(ctx, q, *p.MeetingID, *o.BookID, "Picked in "+p.Title, u.ID)
				if err != nil && !errors.Is(err, store.ErrDuplicate) {
					return err
				}
				if err == nil {
					if err := s.st.Log(ctx, q, u.ID, "update", "meeting", *p.MeetingID,
						fmt.Sprintf("added %s to the meeting (picked in %s)", quote(o.Name()), quote(p.Title)), nil); err != nil {
						return err
					}
				}
			}
			if offWishlist {
				if err := s.st.SetWishlist(ctx, q, *o.BookID, false); err != nil {
					return err
				}
			}
		}
		if err := s.st.SetDecision(ctx, q, id, chosen, note, u.ID); err != nil {
			return err
		}
		if !p.Method.HasBallots() && p.Status != store.StatusClosed {
			if err := s.st.SetPollStatus(ctx, q, id, store.StatusClosed, ""); err != nil {
				return err
			}
		}
		summary := "recorded the decision for " + quote(p.Title) + ": "
		if len(names) == 0 {
			summary += "nothing chosen"
		} else {
			summary += strings.Join(names, ", ")
		}
		var changes []store.Change
		if note != "" {
			changes = []store.Change{{Field: "note", From: p.DecisionNote, To: note}}
		}
		return s.st.Log(ctx, q, u.ID, "decide", "poll", id, summary, changes)
	})
	var he httpError
	if errors.As(err, &he) {
		s.flash(w, "error", he.msg)
		return redirect(w, r, fmt.Sprintf("/polls/%d", id))
	}
	if err != nil {
		return err
	}
	s.flash(w, "ok", "Decision saved.")
	return redirect(w, r, fmt.Sprintf("/polls/%d#decision", id))
}

// ---- Downloads for independent recounts ----

type exportBallot struct {
	Receipt  string        `json:"receipt"`
	Voter    string        `json:"voter,omitempty"`
	VoterID  int64         `json:"voter_id,omitempty"`
	Ballot   voting.Ballot `json:"ballot"`
	Readable string        `json:"readable"`
}

func (s *Server) loadClosedPoll(r *http.Request) (*store.Poll, []*store.PollOption, []*store.BallotRow, error) {
	id, err := pathID(r, "id")
	if err != nil {
		return nil, nil, nil, err
	}
	p, err := s.st.GetPoll(r.Context(), id)
	if err != nil {
		return nil, nil, nil, err
	}
	if p.Status != store.StatusClosed || !p.Method.HasBallots() {
		return nil, nil, nil, errStatus(http.StatusForbidden, "Ballots are published when voting closes.")
	}
	opts, err := s.st.PollOptions(r.Context(), s.st.Pool, id)
	if err != nil {
		return nil, nil, nil, err
	}
	ballots, err := s.st.Ballots(r.Context(), s.st.Pool, id)
	return p, opts, ballots, err
}

func labelFunc(opts []*store.PollOption) func(int64) string {
	names := map[int64]string{}
	for _, o := range opts {
		names[o.ID] = o.Name()
	}
	return func(id int64) string {
		if n, ok := names[id]; ok {
			return n
		}
		return fmt.Sprintf("option %d", id)
	}
}

func (s *Server) ballotsJSON(w http.ResponseWriter, r *http.Request) error {
	p, opts, ballots, err := s.loadClosedPoll(r)
	if err != nil {
		return err
	}
	label := labelFunc(opts)
	type exportOption struct {
		ID     int64  `json:"id"`
		Name   string `json:"name"`
		BookID int64  `json:"book_id,omitempty"`
		Label  string `json:"label,omitempty"`
	}
	out := struct {
		Poll struct {
			ID          int64           `json:"id"`
			Title       string          `json:"title"`
			Method      voting.Method   `json:"method"`
			Rules       voting.Settings `json:"rules"`
			ClosedAt    any             `json:"closed_at"`
			Fingerprint string          `json:"fingerprint"`
		} `json:"poll"`
		Options []exportOption `json:"options"`
		Ballots []exportBallot `json:"ballots"`
		Winners []string       `json:"counted_winners"`
		Tied    []string       `json:"tied,omitempty"`
		Note    string         `json:"how_to_verify"`
	}{}
	out.Poll.ID, out.Poll.Title, out.Poll.Method = p.ID, p.Title, p.Method
	out.Poll.Rules, out.Poll.ClosedAt, out.Poll.Fingerprint = p.Rules(), p.ClosedAt, p.Fingerprint
	for _, o := range opts {
		eo := exportOption{ID: o.ID, Name: o.Name(), Label: o.Label}
		if o.BookID != nil {
			eo.BookID = *o.BookID
		}
		out.Options = append(out.Options, eo)
	}
	for _, b := range ballots {
		eb := exportBallot{Receipt: b.Receipt, Ballot: b.Data, Readable: voting.FormatBallot(p.Method, b.Data, label)}
		if !p.Settings.Secret {
			eb.Voter, eb.VoterID = b.UserName, b.UserID
		}
		out.Ballots = append(out.Ballots, eb)
	}
	res := tally(p, opts, ballots)
	for _, id := range res.Winners {
		out.Winners = append(out.Winners, label(id))
	}
	for _, id := range res.Tied {
		out.Tied = append(out.Tied, label(id))
	}
	out.Note = "The fingerprint is SHA-256 over the JSON of {poll, method, options [{id, book_id, label}] sorted by id, ballots [{receipt, voter_id (open votes only), ballot}] sorted by receipt}."
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="vote-%d-ballots.json"`, p.ID))
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

func (s *Server) ballotsCSV(w http.ResponseWriter, r *http.Request) error {
	p, opts, ballots, err := s.loadClosedPoll(r)
	if err != nil {
		return err
	}
	label := labelFunc(opts)
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="vote-%d-ballots.csv"`, p.ID))
	cw := csv.NewWriter(w)
	header := []string{"receipt"}
	if !p.Settings.Secret {
		header = append(header, "voter")
	}
	header = append(header, "ballot", "raw")
	cw.Write(header)
	for _, b := range ballots {
		raw, _ := json.Marshal(b.Data)
		row := []string{b.Receipt}
		if !p.Settings.Secret {
			row = append(row, b.UserName)
		}
		row = append(row, voting.FormatBallot(p.Method, b.Data, label), string(raw))
		cw.Write(row)
	}
	cw.Flush()
	return cw.Error()
}
