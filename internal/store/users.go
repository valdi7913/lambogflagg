package store

import (
	"context"
	"time"
)

type User struct {
	ID            int64
	Username      string
	DisplayName   string
	PasswordHash  string
	IsAdmin       bool
	InvitedBy     *int64
	InvitedByName string
	CreatedAt     time.Time
	DeactivatedAt *time.Time
}

func (u *User) Active() bool { return u != nil && u.DeactivatedAt == nil }

const userCols = `u.id, u.username, u.display_name, u.password_hash, u.is_admin, u.invited_by,
	coalesce(inv.display_name, ''), u.created_at, u.deactivated_at`
const userFrom = ` FROM users u LEFT JOIN users inv ON inv.id = u.invited_by `

func scanUser(row interface{ Scan(...any) error }) (*User, error) {
	var u User
	err := row.Scan(&u.ID, &u.Username, &u.DisplayName, &u.PasswordHash, &u.IsAdmin, &u.InvitedBy,
		&u.InvitedByName, &u.CreatedAt, &u.DeactivatedAt)
	if err != nil {
		return nil, wrap(err)
	}
	return &u, nil
}

func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&n)
	return n, err
}

func (s *Store) CreateUser(ctx context.Context, q Querier, u *User) error {
	err := q.QueryRow(ctx, `INSERT INTO users (username, display_name, password_hash, is_admin, invited_by)
		VALUES ($1, $2, $3, $4, $5) RETURNING id, created_at`,
		u.Username, u.DisplayName, u.PasswordHash, u.IsAdmin, u.InvitedBy).Scan(&u.ID, &u.CreatedAt)
	return wrap(err)
}

func (s *Store) UserByID(ctx context.Context, id int64) (*User, error) {
	return scanUser(s.Pool.QueryRow(ctx, `SELECT `+userCols+userFrom+` WHERE u.id = $1`, id))
}

func (s *Store) UserByUsername(ctx context.Context, username string) (*User, error) {
	return scanUser(s.Pool.QueryRow(ctx, `SELECT `+userCols+userFrom+` WHERE lower(u.username) = lower($1)`, username))
}

func (s *Store) ListUsers(ctx context.Context) ([]*User, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+userCols+userFrom+` ORDER BY u.deactivated_at IS NOT NULL, lower(u.display_name)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *Store) ActiveUsers(ctx context.Context) ([]*User, error) {
	all, err := s.ListUsers(ctx)
	if err != nil {
		return nil, err
	}
	var out []*User
	for _, u := range all {
		if u.Active() {
			out = append(out, u)
		}
	}
	return out, nil
}

func (s *Store) CountAdmins(ctx context.Context) (int, error) {
	var n int
	err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM users WHERE is_admin AND deactivated_at IS NULL`).Scan(&n)
	return n, err
}

func (s *Store) UpdateDisplayName(ctx context.Context, q Querier, id int64, name string) error {
	_, err := q.Exec(ctx, `UPDATE users SET display_name = $2 WHERE id = $1`, id, name)
	return err
}

func (s *Store) UpdatePassword(ctx context.Context, q Querier, id int64, hash string) error {
	_, err := q.Exec(ctx, `UPDATE users SET password_hash = $2 WHERE id = $1`, id, hash)
	return err
}

func (s *Store) SetAdmin(ctx context.Context, q Querier, id int64, admin bool) error {
	_, err := q.Exec(ctx, `UPDATE users SET is_admin = $2 WHERE id = $1`, id, admin)
	return err
}

func (s *Store) SetDeactivated(ctx context.Context, q Querier, id int64, deactivated bool) error {
	var err error
	if deactivated {
		_, err = q.Exec(ctx, `UPDATE users SET deactivated_at = now() WHERE id = $1 AND deactivated_at IS NULL`, id)
		if err == nil {
			_, err = q.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1`, id)
		}
	} else {
		_, err = q.Exec(ctx, `UPDATE users SET deactivated_at = NULL WHERE id = $1`, id)
	}
	return err
}

// ---- Sessions ----

func (s *Store) CreateSession(ctx context.Context, userID int64, tokenHash []byte, csrf string, expires time.Time) error {
	_, err := s.Pool.Exec(ctx, `INSERT INTO sessions (token_hash, user_id, csrf_token, expires_at) VALUES ($1, $2, $3, $4)`,
		tokenHash, userID, csrf, expires)
	return err
}

// SessionUser returns the user and CSRF token for a valid session.
func (s *Store) SessionUser(ctx context.Context, tokenHash []byte) (*User, string, error) {
	row := s.Pool.QueryRow(ctx, `SELECT `+userCols+`, s.csrf_token FROM sessions s
		JOIN users u ON u.id = s.user_id LEFT JOIN users inv ON inv.id = u.invited_by
		WHERE s.token_hash = $1 AND s.expires_at > now() AND u.deactivated_at IS NULL`, tokenHash)
	var u User
	var csrf string
	err := row.Scan(&u.ID, &u.Username, &u.DisplayName, &u.PasswordHash, &u.IsAdmin, &u.InvitedBy,
		&u.InvitedByName, &u.CreatedAt, &u.DeactivatedAt, &csrf)
	if err != nil {
		return nil, "", wrap(err)
	}
	return &u, csrf, nil
}

func (s *Store) DeleteSession(ctx context.Context, tokenHash []byte) error {
	_, err := s.Pool.Exec(ctx, `DELETE FROM sessions WHERE token_hash = $1`, tokenHash)
	return err
}

func (s *Store) DeleteUserSessions(ctx context.Context, q Querier, userID int64) error {
	_, err := q.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1`, userID)
	return err
}

func (s *Store) DeleteExpiredSessions(ctx context.Context) error {
	_, err := s.Pool.Exec(ctx, `DELETE FROM sessions WHERE expires_at < now()`)
	return err
}

// ---- Invites ----

type Invite struct {
	ID            int64
	Token         string
	Note          string
	CreatedBy     *int64
	CreatedByName string
	CreatedAt     time.Time
	ExpiresAt     time.Time
	UsedBy        *int64
	UsedByName    string
	UsedAt        *time.Time
	RevokedAt     *time.Time
}

func (i *Invite) Usable() bool {
	return i.UsedAt == nil && i.RevokedAt == nil && time.Now().Before(i.ExpiresAt)
}

const inviteSelect = `SELECT i.id, i.token, i.note, i.created_by, coalesce(c.display_name, ''), i.created_at, i.expires_at,
	i.used_by, coalesce(us.display_name, ''), i.used_at, i.revoked_at
	FROM invites i LEFT JOIN users c ON c.id = i.created_by LEFT JOIN users us ON us.id = i.used_by `

func scanInvite(row interface{ Scan(...any) error }) (*Invite, error) {
	var i Invite
	err := row.Scan(&i.ID, &i.Token, &i.Note, &i.CreatedBy, &i.CreatedByName, &i.CreatedAt, &i.ExpiresAt,
		&i.UsedBy, &i.UsedByName, &i.UsedAt, &i.RevokedAt)
	if err != nil {
		return nil, wrap(err)
	}
	return &i, nil
}

func (s *Store) CreateInvite(ctx context.Context, q Querier, inv *Invite) error {
	return wrap(q.QueryRow(ctx, `INSERT INTO invites (token, note, created_by, expires_at) VALUES ($1, $2, $3, $4)
		RETURNING id, created_at`, inv.Token, inv.Note, inv.CreatedBy, inv.ExpiresAt).Scan(&inv.ID, &inv.CreatedAt))
}

// ListInvites returns usable invites plus those used or revoked in the last 30 days.
func (s *Store) ListInvites(ctx context.Context) ([]*Invite, error) {
	rows, err := s.Pool.Query(ctx, inviteSelect+`
		WHERE (i.used_at IS NULL AND i.revoked_at IS NULL AND i.expires_at > now())
		   OR coalesce(i.used_at, i.revoked_at, i.expires_at) > now() - interval '30 days'
		ORDER BY i.created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Invite
	for rows.Next() {
		i, err := scanInvite(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

func (s *Store) InviteByToken(ctx context.Context, q Querier, token string, forUpdate bool) (*Invite, error) {
	sql := inviteSelect + ` WHERE i.token = $1`
	if forUpdate {
		sql += ` FOR UPDATE OF i`
	}
	return scanInvite(q.QueryRow(ctx, sql, token))
}

func (s *Store) InviteByID(ctx context.Context, id int64) (*Invite, error) {
	return scanInvite(s.Pool.QueryRow(ctx, inviteSelect+` WHERE i.id = $1`, id))
}

func (s *Store) UseInvite(ctx context.Context, q Querier, id, userID int64) error {
	_, err := q.Exec(ctx, `UPDATE invites SET used_by = $2, used_at = now() WHERE id = $1`, id, userID)
	return err
}

func (s *Store) RevokeInvite(ctx context.Context, q Querier, id int64) error {
	_, err := q.Exec(ctx, `UPDATE invites SET revoked_at = now() WHERE id = $1 AND used_at IS NULL`, id)
	return err
}

// ---- Password resets ----

type PasswordReset struct {
	ID        int64
	Token     string
	UserID    int64
	CreatedBy *int64
	ExpiresAt time.Time
	UsedAt    *time.Time
}

func (s *Store) CreateReset(ctx context.Context, q Querier, r *PasswordReset) error {
	return wrap(q.QueryRow(ctx, `INSERT INTO password_resets (token, user_id, created_by, expires_at)
		VALUES ($1, $2, $3, $4) RETURNING id`, r.Token, r.UserID, r.CreatedBy, r.ExpiresAt).Scan(&r.ID))
}

func (s *Store) ResetByToken(ctx context.Context, q Querier, token string) (*PasswordReset, error) {
	var r PasswordReset
	err := q.QueryRow(ctx, `SELECT id, token, user_id, created_by, expires_at, used_at FROM password_resets
		WHERE token = $1 FOR UPDATE`, token).Scan(&r.ID, &r.Token, &r.UserID, &r.CreatedBy, &r.ExpiresAt, &r.UsedAt)
	if err != nil {
		return nil, wrap(err)
	}
	return &r, nil
}

func (s *Store) UseReset(ctx context.Context, q Querier, id int64) error {
	_, err := q.Exec(ctx, `UPDATE password_resets SET used_at = now() WHERE id = $1`, id)
	return err
}
