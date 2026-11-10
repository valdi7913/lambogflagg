package store

import (
	"context"
	"encoding/json"
	"time"

	"bookclub/internal/voting"
)

const (
	StatusNominating = "nominating"
	StatusVoting     = "voting"
	StatusClosed     = "closed"
)

type Poll struct {
	ID              int64
	Title           string
	Description     string
	MeetingID       *int64
	MeetingTitle    string
	MeetingStartsAt *time.Time
	Method          voting.Method
	Settings        voting.Settings
	Status          string
	ClosesAt        *time.Time
	VotingOpenedAt  *time.Time
	ClosedAt        *time.Time
	Fingerprint     string
	DecisionNote    string
	DecidedBy       *int64
	DecidedByName   string
	DecidedAt       *time.Time
	CreatedBy       *int64
	CreatedByName   string
	CreatedAt       time.Time
	UpdatedAt       time.Time
	OptionCount     int
	BallotCount     int
}

func (p *Poll) Rules() voting.Settings { return p.Settings.Normalize(p.Method) }

// AcceptsNominations reports whether new options may be added right now.
func (p *Poll) AcceptsNominations() bool {
	switch p.Status {
	case StatusNominating:
		return true
	case StatusVoting:
		return p.Settings.LateNominations
	}
	return false
}

const pollSelect = `SELECT p.id, p.title, p.description, p.meeting_id, coalesce(m.title, ''), m.starts_at,
	p.method, p.settings, p.status, p.closes_at, p.voting_opened_at, p.closed_at, p.ballot_fingerprint,
	p.decision_note, p.decided_by, coalesce(d.display_name, ''), p.decided_at,
	p.created_by, coalesce(c.display_name, ''), p.created_at, p.updated_at,
	(SELECT count(*) FROM poll_options o WHERE o.poll_id = p.id),
	(SELECT count(*) FROM ballots bl WHERE bl.poll_id = p.id)
	FROM polls p
	LEFT JOIN meetings m ON m.id = p.meeting_id
	LEFT JOIN users d ON d.id = p.decided_by
	LEFT JOIN users c ON c.id = p.created_by `

func scanPoll(row interface{ Scan(...any) error }) (*Poll, error) {
	var p Poll
	var settings []byte
	var method string
	err := row.Scan(&p.ID, &p.Title, &p.Description, &p.MeetingID, &p.MeetingTitle, &p.MeetingStartsAt,
		&method, &settings, &p.Status, &p.ClosesAt, &p.VotingOpenedAt, &p.ClosedAt, &p.Fingerprint,
		&p.DecisionNote, &p.DecidedBy, &p.DecidedByName, &p.DecidedAt,
		&p.CreatedBy, &p.CreatedByName, &p.CreatedAt, &p.UpdatedAt, &p.OptionCount, &p.BallotCount)
	if err != nil {
		return nil, wrap(err)
	}
	p.Method = voting.Method(method)
	_ = json.Unmarshal(settings, &p.Settings)
	return &p, nil
}

func (s *Store) queryPolls(ctx context.Context, sql string, args ...any) ([]*Poll, error) {
	rows, err := s.Pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Poll
	for rows.Next() {
		p, err := scanPoll(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) ListPolls(ctx context.Context) ([]*Poll, error) {
	return s.queryPolls(ctx, pollSelect+` ORDER BY (p.status = 'closed'), p.created_at DESC`)
}

func (s *Store) OpenPolls(ctx context.Context) ([]*Poll, error) {
	return s.queryPolls(ctx, pollSelect+` WHERE p.status <> 'closed' ORDER BY p.created_at DESC`)
}

func (s *Store) MeetingPolls(ctx context.Context, meetingID int64) ([]*Poll, error) {
	return s.queryPolls(ctx, pollSelect+` WHERE p.meeting_id = $1 ORDER BY p.created_at DESC`, meetingID)
}

func (s *Store) GetPoll(ctx context.Context, id int64) (*Poll, error) {
	return scanPoll(s.Pool.QueryRow(ctx, pollSelect+` WHERE p.id = $1`, id))
}

// LockPoll re-reads a poll inside a transaction, locking the row so status
// changes, ballots and nominations can't race each other.
func (s *Store) LockPoll(ctx context.Context, q Querier, id int64) (*Poll, error) {
	if _, err := q.Exec(ctx, `SELECT 1 FROM polls WHERE id = $1 FOR UPDATE`, id); err != nil {
		return nil, err
	}
	return scanPoll(q.QueryRow(ctx, pollSelect+` WHERE p.id = $1`, id))
}

// DuePolls returns polls whose deadline has passed but are still open for voting.
func (s *Store) DuePolls(ctx context.Context) ([]int64, error) {
	rows, err := s.Pool.Query(ctx, `SELECT id FROM polls WHERE status = 'voting' AND closes_at IS NOT NULL AND closes_at <= now()`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *Store) CreatePoll(ctx context.Context, q Querier, p *Poll) error {
	settings, _ := json.Marshal(p.Settings)
	return wrap(q.QueryRow(ctx, `INSERT INTO polls (title, description, meeting_id, method, settings, status, closes_at,
		voting_opened_at, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, CASE WHEN $6 = 'voting' THEN now() END, $8)
		RETURNING id, created_at, updated_at`,
		p.Title, p.Description, p.MeetingID, string(p.Method), settings, p.Status, p.ClosesAt, p.CreatedBy).
		Scan(&p.ID, &p.CreatedAt, &p.UpdatedAt))
}

func (s *Store) UpdatePoll(ctx context.Context, q Querier, p *Poll) error {
	settings, _ := json.Marshal(p.Settings)
	_, err := q.Exec(ctx, `UPDATE polls SET title = $2, description = $3, meeting_id = $4, method = $5, settings = $6,
		closes_at = $7, updated_at = now() WHERE id = $1`,
		p.ID, p.Title, p.Description, p.MeetingID, string(p.Method), settings, p.ClosesAt)
	return wrap(err)
}

func (s *Store) DeletePoll(ctx context.Context, q Querier, id int64) error {
	_, err := q.Exec(ctx, `DELETE FROM polls WHERE id = $1`, id)
	return err
}

func (s *Store) SetPollStatus(ctx context.Context, q Querier, id int64, status, fingerprint string) error {
	var err error
	switch status {
	case StatusVoting:
		_, err = q.Exec(ctx, `UPDATE polls SET status = 'voting', voting_opened_at = coalesce(voting_opened_at, now()),
			closed_at = NULL, ballot_fingerprint = '', updated_at = now() WHERE id = $1`, id)
	case StatusClosed:
		_, err = q.Exec(ctx, `UPDATE polls SET status = 'closed', closed_at = now(), ballot_fingerprint = $2,
			updated_at = now() WHERE id = $1`, id, fingerprint)
	default:
		_, err = q.Exec(ctx, `UPDATE polls SET status = $2, closed_at = NULL, ballot_fingerprint = '', updated_at = now()
			WHERE id = $1`, id, status)
	}
	return err
}

func (s *Store) SetDecision(ctx context.Context, q Querier, pollID int64, chosen []int64, note string, userID int64) error {
	if _, err := q.Exec(ctx, `UPDATE poll_options SET chosen = coalesce(id = ANY($2::bigint[]), false) WHERE poll_id = $1`, pollID, chosen); err != nil {
		return err
	}
	_, err := q.Exec(ctx, `UPDATE polls SET decision_note = $2, decided_by = $3, decided_at = now(), updated_at = now()
		WHERE id = $1`, pollID, note, userID)
	return err
}

func (s *Store) ClearDecision(ctx context.Context, q Querier, pollID int64) error {
	if _, err := q.Exec(ctx, `UPDATE poll_options SET chosen = false WHERE poll_id = $1`, pollID); err != nil {
		return err
	}
	_, err := q.Exec(ctx, `UPDATE polls SET decision_note = '', decided_by = NULL, decided_at = NULL, updated_at = now()
		WHERE id = $1`, pollID)
	return err
}

// ---- Options ----

type PollOption struct {
	ID              int64
	PollID          int64
	BookID          *int64
	Book            *Book
	Label           string
	Pitch           string
	NominatedBy     *int64
	NominatedByName string
	Chosen          bool
	CreatedAt       time.Time
}

// Name is what the option is called: the book title or the free-text label.
func (o *PollOption) Name() string {
	if o.Book != nil {
		return o.Book.Title
	}
	return o.Label
}

func (s *Store) PollOptions(ctx context.Context, q Querier, pollID int64) ([]*PollOption, error) {
	rows, err := q.Query(ctx, `SELECT o.id, o.poll_id, o.book_id, o.label, o.pitch, o.nominated_by,
		coalesce(nu.display_name, ''), o.chosen, o.created_at,
		b.id, coalesce(b.title, ''), coalesce(b.authors, ''), b.year, coalesce(b.cover_url, ''), coalesce(b.goodreads_url, '')
		FROM poll_options o
		LEFT JOIN users nu ON nu.id = o.nominated_by
		LEFT JOIN books b ON b.id = o.book_id
		WHERE o.poll_id = $1 ORDER BY o.created_at, o.id`, pollID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*PollOption
	for rows.Next() {
		var o PollOption
		var bid *int64
		var b Book
		if err := rows.Scan(&o.ID, &o.PollID, &o.BookID, &o.Label, &o.Pitch, &o.NominatedBy, &o.NominatedByName,
			&o.Chosen, &o.CreatedAt, &bid, &b.Title, &b.Authors, &b.Year, &b.CoverURL, &b.GoodreadsURL); err != nil {
			return nil, err
		}
		if bid != nil {
			b.ID = *bid
			o.Book = &b
		}
		out = append(out, &o)
	}
	return out, rows.Err()
}

func (s *Store) AddOption(ctx context.Context, q Querier, o *PollOption) error {
	return wrap(q.QueryRow(ctx, `INSERT INTO poll_options (poll_id, book_id, label, pitch, nominated_by)
		VALUES ($1, $2, $3, $4, $5) RETURNING id, created_at`,
		o.PollID, o.BookID, o.Label, o.Pitch, o.NominatedBy).Scan(&o.ID, &o.CreatedAt))
}

func (s *Store) RemoveOption(ctx context.Context, q Querier, pollID, optionID int64) error {
	_, err := q.Exec(ctx, `DELETE FROM poll_options WHERE poll_id = $1 AND id = $2`, pollID, optionID)
	return err
}

func (s *Store) CountNominations(ctx context.Context, q Querier, pollID, userID int64) (int, error) {
	var n int
	err := q.QueryRow(ctx, `SELECT count(*) FROM poll_options WHERE poll_id = $1 AND nominated_by = $2`, pollID, userID).Scan(&n)
	return n, err
}

// ---- Ballots ----

type BallotRow struct {
	ID        int64
	PollID    int64
	UserID    int64
	UserName  string
	Receipt   string
	Data      voting.Ballot
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (s *Store) Ballots(ctx context.Context, q Querier, pollID int64) ([]*BallotRow, error) {
	rows, err := q.Query(ctx, `SELECT bl.id, bl.poll_id, bl.user_id, u.display_name, bl.receipt, bl.data,
		bl.created_at, bl.updated_at FROM ballots bl JOIN users u ON u.id = bl.user_id
		WHERE bl.poll_id = $1 ORDER BY bl.receipt`, pollID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*BallotRow
	for rows.Next() {
		var b BallotRow
		var data []byte
		if err := rows.Scan(&b.ID, &b.PollID, &b.UserID, &b.UserName, &b.Receipt, &data, &b.CreatedAt, &b.UpdatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(data, &b.Data); err != nil {
			return nil, err
		}
		out = append(out, &b)
	}
	return out, rows.Err()
}

// UpsertBallot stores a member's ballot. The receipt code stays the same
// when a ballot is changed.
func (s *Store) UpsertBallot(ctx context.Context, q Querier, pollID, userID int64, b voting.Ballot) (string, error) {
	data, err := json.Marshal(b)
	if err != nil {
		return "", err
	}
	var receipt string
	for attempt := 0; attempt < 5; attempt++ {
		err = q.QueryRow(ctx, `INSERT INTO ballots (poll_id, user_id, receipt, data) VALUES ($1, $2, $3, $4)
			ON CONFLICT (poll_id, user_id) DO UPDATE SET data = excluded.data, updated_at = now()
			RETURNING receipt`, pollID, userID, voting.NewReceipt(), data).Scan(&receipt)
		if wrap(err) != ErrDuplicate { // a receipt collision: try another code
			break
		}
	}
	return receipt, wrap(err)
}

func (s *Store) DeleteBallot(ctx context.Context, q Querier, pollID, userID int64) error {
	_, err := q.Exec(ctx, `DELETE FROM ballots WHERE poll_id = $1 AND user_id = $2`, pollID, userID)
	return err
}

func (s *Store) DeleteAllBallots(ctx context.Context, q Querier, pollID int64) error {
	_, err := q.Exec(ctx, `DELETE FROM ballots WHERE poll_id = $1`, pollID)
	return err
}

// VotedPollIDs returns which of the open polls the user has a ballot in.
func (s *Store) VotedPollIDs(ctx context.Context, userID int64) (map[int64]bool, error) {
	rows, err := s.Pool.Query(ctx, `SELECT poll_id FROM ballots WHERE user_id = $1`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}
