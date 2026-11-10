package store

import (
	"context"
	"encoding/json"
	"time"
)

// Change records one edited field for the history log.
type Change struct {
	Field string `json:"field"`
	From  string `json:"from"`
	To    string `json:"to"`
}

type Activity struct {
	ID          int64
	UserID      *int64
	UserName    string
	Action      string
	SubjectType string
	SubjectID   int64
	Summary     string
	Changes     []Change
	CreatedAt   time.Time
}

type activityDetails struct {
	Changes []Change `json:"changes,omitempty"`
}

// Diff compares field values and returns the ones that changed.
// Pass pairs as field name -> [old, new].
func Diff(fields [][3]string) []Change {
	var out []Change
	for _, f := range fields {
		if f[1] != f[2] {
			out = append(out, Change{Field: f[0], From: f[1], To: f[2]})
		}
	}
	return out
}

// Log appends an entry to the history. userID may be 0 for system actions.
func (s *Store) Log(ctx context.Context, q Querier, userID int64, action, subjectType string, subjectID int64, summary string, changes []Change) error {
	details, _ := json.Marshal(activityDetails{Changes: changes})
	var uid *int64
	if userID != 0 {
		uid = &userID
	}
	_, err := q.Exec(ctx, `INSERT INTO activity (user_id, action, subject_type, subject_id, summary, details)
		VALUES ($1, $2, $3, $4, $5, $6)`, uid, action, subjectType, subjectID, summary, details)
	return err
}

const activitySelect = `SELECT a.id, a.user_id, coalesce(u.display_name, ''), a.action, a.subject_type, a.subject_id,
	a.summary, a.details, a.created_at FROM activity a LEFT JOIN users u ON u.id = a.user_id `

func (s *Store) queryActivity(ctx context.Context, sql string, args ...any) ([]Activity, error) {
	rows, err := s.Pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Activity
	for rows.Next() {
		var a Activity
		var details []byte
		if err := rows.Scan(&a.ID, &a.UserID, &a.UserName, &a.Action, &a.SubjectType, &a.SubjectID,
			&a.Summary, &details, &a.CreatedAt); err != nil {
			return nil, err
		}
		var d activityDetails
		_ = json.Unmarshal(details, &d)
		a.Changes = d.Changes
		out = append(out, a)
	}
	return out, rows.Err()
}

// RecentActivity returns up to limit entries older than beforeID (0 = newest).
func (s *Store) RecentActivity(ctx context.Context, limit int, beforeID int64) ([]Activity, error) {
	if beforeID <= 0 {
		return s.queryActivity(ctx, activitySelect+` ORDER BY a.id DESC LIMIT $1`, limit)
	}
	return s.queryActivity(ctx, activitySelect+` WHERE a.id < $2 ORDER BY a.id DESC LIMIT $1`, limit, beforeID)
}

func (s *Store) SubjectActivity(ctx context.Context, subjectType string, id int64) ([]Activity, error) {
	return s.queryActivity(ctx, activitySelect+` WHERE a.subject_type = $1 AND a.subject_id = $2 ORDER BY a.id DESC LIMIT 200`, subjectType, id)
}
