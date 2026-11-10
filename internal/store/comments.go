package store

import (
	"context"
	"fmt"
	"time"
)

type Comment struct {
	ID          int64
	UserID      *int64
	UserName    string
	SubjectType string
	SubjectID   int64
	Body        string
	CreatedAt   time.Time
	EditedAt    *time.Time
}

// subjectColumn maps a subject type to its foreign-key column. It is the only
// place subject types become SQL, so unknown types are rejected here.
func subjectColumn(subjectType string) (string, error) {
	switch subjectType {
	case "book":
		return "book_id", nil
	case "meeting":
		return "meeting_id", nil
	case "poll":
		return "poll_id", nil
	}
	return "", fmt.Errorf("unknown comment subject %q", subjectType)
}

const commentSelect = `SELECT c.id, c.user_id, coalesce(u.display_name, ''),
	CASE WHEN c.book_id IS NOT NULL THEN 'book' WHEN c.meeting_id IS NOT NULL THEN 'meeting' ELSE 'poll' END,
	coalesce(c.book_id, c.meeting_id, c.poll_id), c.body, c.created_at, c.edited_at
	FROM comments c LEFT JOIN users u ON u.id = c.user_id `

func scanComment(row interface{ Scan(...any) error }) (*Comment, error) {
	var c Comment
	if err := row.Scan(&c.ID, &c.UserID, &c.UserName, &c.SubjectType, &c.SubjectID, &c.Body, &c.CreatedAt, &c.EditedAt); err != nil {
		return nil, wrap(err)
	}
	return &c, nil
}

func (s *Store) ListComments(ctx context.Context, subjectType string, id int64) ([]*Comment, error) {
	col, err := subjectColumn(subjectType)
	if err != nil {
		return nil, err
	}
	rows, err := s.Pool.Query(ctx, commentSelect+` WHERE c.`+col+` = $1 ORDER BY c.created_at`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Comment
	for rows.Next() {
		c, err := scanComment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) GetComment(ctx context.Context, id int64) (*Comment, error) {
	return scanComment(s.Pool.QueryRow(ctx, commentSelect+` WHERE c.id = $1`, id))
}

func (s *Store) CreateComment(ctx context.Context, q Querier, c *Comment) error {
	col, err := subjectColumn(c.SubjectType)
	if err != nil {
		return err
	}
	return wrap(q.QueryRow(ctx, `INSERT INTO comments (user_id, `+col+`, body) VALUES ($1, $2, $3) RETURNING id, created_at`,
		c.UserID, c.SubjectID, c.Body).Scan(&c.ID, &c.CreatedAt))
}

func (s *Store) UpdateComment(ctx context.Context, q Querier, id int64, body string) error {
	_, err := q.Exec(ctx, `UPDATE comments SET body = $2, edited_at = now() WHERE id = $1`, id, body)
	return err
}

func (s *Store) DeleteComment(ctx context.Context, q Querier, id int64) error {
	_, err := q.Exec(ctx, `DELETE FROM comments WHERE id = $1`, id)
	return err
}

// SubjectExists checks that a book, meeting or poll exists.
func (s *Store) SubjectExists(ctx context.Context, subjectType string, id int64) bool {
	table := map[string]string{"book": "books", "meeting": "meetings", "poll": "polls"}[subjectType]
	if table == "" {
		return false
	}
	var ok bool
	_ = s.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM `+table+` WHERE id = $1)`, id).Scan(&ok)
	return ok
}
