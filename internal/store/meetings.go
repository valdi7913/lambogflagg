package store

import (
	"context"
	"time"
)

type Meeting struct {
	ID            int64
	Title         string
	StartsAt      *time.Time
	Place         string
	Description   string
	CreatedBy     *int64
	CreatedByName string
	CreatedAt     time.Time
	UpdatedAt     time.Time
	Books         []MeetingBook
}

type MeetingBook struct {
	Book        *Book
	Note        string
	AddedByName string
}

const meetingSelect = `SELECT m.id, m.title, m.starts_at, m.place, m.description, m.created_by,
	coalesce(u.display_name, ''), m.created_at, m.updated_at
	FROM meetings m LEFT JOIN users u ON u.id = m.created_by `

func scanMeeting(row interface{ Scan(...any) error }) (*Meeting, error) {
	var m Meeting
	err := row.Scan(&m.ID, &m.Title, &m.StartsAt, &m.Place, &m.Description, &m.CreatedBy, &m.CreatedByName,
		&m.CreatedAt, &m.UpdatedAt)
	if err != nil {
		return nil, wrap(err)
	}
	return &m, nil
}

func (s *Store) queryMeetings(ctx context.Context, sql string, args ...any) ([]*Meeting, error) {
	rows, err := s.Pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	var out []*Meeting
	for rows.Next() {
		m, err := scanMeeting(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, m)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, s.attachBooks(ctx, out)
}

func (s *Store) attachBooks(ctx context.Context, ms []*Meeting) error {
	if len(ms) == 0 {
		return nil
	}
	ids := make([]int64, len(ms))
	byID := map[int64]*Meeting{}
	for i, m := range ms {
		ids[i] = m.ID
		byID[m.ID] = m
	}
	rows, err := s.Pool.Query(ctx, `SELECT mb.meeting_id, mb.note, coalesce(ab.display_name, ''), `+bookCols+`
		FROM meeting_books mb JOIN books b ON b.id = mb.book_id LEFT JOIN users u ON u.id = b.added_by
		LEFT JOIN users ab ON ab.id = mb.added_by
		WHERE mb.meeting_id = ANY($1) ORDER BY mb.added_at`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var mid int64
		var mb MeetingBook
		var b Book
		if err := rows.Scan(&mid, &mb.Note, &mb.AddedByName, &b.ID, &b.Title, &b.Authors, &b.Year, &b.CoverURL,
			&b.GoodreadsURL, &b.OpenLibraryKey, &b.Notes, &b.OnWishlist, &b.AddedBy, &b.AddedByName,
			&b.CreatedAt, &b.UpdatedAt, &b.MeetingCount); err != nil {
			return err
		}
		mb.Book = &b
		byID[mid].Books = append(byID[mid].Books, mb)
	}
	return rows.Err()
}

// Upcoming meetings: no date yet, or later than 12 hours ago (so tonight's
// meeting stays "upcoming" while it's happening). Soonest first, undated last.
func (s *Store) UpcomingMeetings(ctx context.Context, limit int) ([]*Meeting, error) {
	return s.queryMeetings(ctx, meetingSelect+`
		WHERE m.starts_at IS NULL OR m.starts_at > now() - interval '12 hours'
		ORDER BY m.starts_at ASC NULLS LAST, m.id DESC LIMIT $1`, limit)
}

func (s *Store) PastMeetings(ctx context.Context) ([]*Meeting, error) {
	return s.queryMeetings(ctx, meetingSelect+`
		WHERE m.starts_at <= now() - interval '12 hours' ORDER BY m.starts_at DESC`)
}

func (s *Store) MeetingsForSelect(ctx context.Context) ([]*Meeting, error) {
	return s.queryMeetings(ctx, meetingSelect+` ORDER BY m.starts_at DESC NULLS FIRST, m.id DESC`)
}

func (s *Store) BookMeetings(ctx context.Context, bookID int64) ([]*Meeting, error) {
	return s.queryMeetings(ctx, meetingSelect+`
		WHERE m.id IN (SELECT meeting_id FROM meeting_books WHERE book_id = $1)
		ORDER BY m.starts_at DESC NULLS FIRST`, bookID)
}

func (s *Store) GetMeeting(ctx context.Context, id int64) (*Meeting, error) {
	ms, err := s.queryMeetings(ctx, meetingSelect+` WHERE m.id = $1`, id)
	if err != nil {
		return nil, err
	}
	if len(ms) == 0 {
		return nil, ErrNotFound
	}
	return ms[0], nil
}

func (s *Store) CreateMeeting(ctx context.Context, q Querier, m *Meeting) error {
	return wrap(q.QueryRow(ctx, `INSERT INTO meetings (title, starts_at, place, description, created_by)
		VALUES ($1, $2, $3, $4, $5) RETURNING id, created_at, updated_at`,
		m.Title, m.StartsAt, m.Place, m.Description, m.CreatedBy).Scan(&m.ID, &m.CreatedAt, &m.UpdatedAt))
}

func (s *Store) UpdateMeeting(ctx context.Context, q Querier, m *Meeting) error {
	_, err := q.Exec(ctx, `UPDATE meetings SET title = $2, starts_at = $3, place = $4, description = $5, updated_at = now()
		WHERE id = $1`, m.ID, m.Title, m.StartsAt, m.Place, m.Description)
	return wrap(err)
}

func (s *Store) DeleteMeeting(ctx context.Context, q Querier, id int64) error {
	_, err := q.Exec(ctx, `DELETE FROM meetings WHERE id = $1`, id)
	return wrap(err)
}

func (s *Store) AddMeetingBook(ctx context.Context, q Querier, meetingID, bookID int64, note string, userID int64) error {
	_, err := q.Exec(ctx, `INSERT INTO meeting_books (meeting_id, book_id, note, added_by) VALUES ($1, $2, $3, $4)`,
		meetingID, bookID, note, userID)
	return wrap(err)
}

func (s *Store) RemoveMeetingBook(ctx context.Context, q Querier, meetingID, bookID int64) error {
	_, err := q.Exec(ctx, `DELETE FROM meeting_books WHERE meeting_id = $1 AND book_id = $2`, meetingID, bookID)
	return err
}

// MeetingDisplayName gives a meeting a readable name even without a title.
func MeetingDisplayName(m *Meeting, loc *time.Location) string {
	if m.Title != "" {
		return m.Title
	}
	if m.StartsAt != nil {
		return "Meeting on " + m.StartsAt.In(loc).Format("Mon 2 Jan 2006")
	}
	return "Meeting (date to be decided)"
}
