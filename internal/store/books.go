package store

import (
	"context"
	"strings"
	"time"
)

type Book struct {
	ID             int64
	Title          string
	Authors        string
	Year           *int
	CoverURL       string
	GoodreadsURL   string
	OpenLibraryKey string
	Notes          string
	OnWishlist     bool
	AddedBy        *int64
	AddedByName    string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	MeetingCount   int // how many meetings it has been part of
}

const bookCols = `b.id, b.title, b.authors, b.year, b.cover_url, b.goodreads_url, b.openlibrary_key, b.notes,
	b.on_wishlist, b.added_by, coalesce(u.display_name, ''), b.created_at, b.updated_at,
	(SELECT count(*) FROM meeting_books mb WHERE mb.book_id = b.id)`
const bookFrom = ` FROM books b LEFT JOIN users u ON u.id = b.added_by `

func scanBook(row interface{ Scan(...any) error }) (*Book, error) {
	var b Book
	err := row.Scan(&b.ID, &b.Title, &b.Authors, &b.Year, &b.CoverURL, &b.GoodreadsURL, &b.OpenLibraryKey,
		&b.Notes, &b.OnWishlist, &b.AddedBy, &b.AddedByName, &b.CreatedAt, &b.UpdatedAt, &b.MeetingCount)
	if err != nil {
		return nil, wrap(err)
	}
	return &b, nil
}

func (s *Store) queryBooks(ctx context.Context, sql string, args ...any) ([]*Book, error) {
	rows, err := s.Pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Book
	for rows.Next() {
		b, err := scanBook(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// ListBooks filters by "wishlist", "read" (part of at least one meeting) or "all",
// optionally matching a search term against title and authors.
func (s *Store) ListBooks(ctx context.Context, filter, search string) ([]*Book, error) {
	var where []string
	var args []any
	switch filter {
	case "wishlist":
		where = append(where, "b.on_wishlist")
	case "read":
		where = append(where, "EXISTS (SELECT 1 FROM meeting_books mb WHERE mb.book_id = b.id)")
	}
	if search = strings.TrimSpace(search); search != "" {
		args = append(args, "%"+strings.ReplaceAll(strings.ReplaceAll(search, `\`, `\\`), "%", `\%`)+"%")
		where = append(where, "(b.title ILIKE $1 OR b.authors ILIKE $1)")
	}
	sql := `SELECT ` + bookCols + bookFrom
	if len(where) > 0 {
		sql += " WHERE " + strings.Join(where, " AND ")
	}
	sql += " ORDER BY lower(b.title)"
	return s.queryBooks(ctx, sql, args...)
}

// BooksForSelect returns every book, want-to-read ones first.
func (s *Store) BooksForSelect(ctx context.Context) ([]*Book, error) {
	return s.queryBooks(ctx, `SELECT `+bookCols+bookFrom+` ORDER BY b.on_wishlist DESC, lower(b.title)`)
}

func (s *Store) GetBook(ctx context.Context, id int64) (*Book, error) {
	return scanBook(s.Pool.QueryRow(ctx, `SELECT `+bookCols+bookFrom+` WHERE b.id = $1`, id))
}

func (s *Store) BookByOpenLibraryKey(ctx context.Context, key string) (*Book, error) {
	return scanBook(s.Pool.QueryRow(ctx, `SELECT `+bookCols+bookFrom+` WHERE b.openlibrary_key = $1 AND $1 <> '' LIMIT 1`, key))
}

func (s *Store) CreateBook(ctx context.Context, q Querier, b *Book) error {
	return wrap(q.QueryRow(ctx, `INSERT INTO books (title, authors, year, cover_url, goodreads_url, openlibrary_key, notes, on_wishlist, added_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING id, created_at, updated_at`,
		b.Title, b.Authors, b.Year, b.CoverURL, b.GoodreadsURL, b.OpenLibraryKey, b.Notes, b.OnWishlist, b.AddedBy).
		Scan(&b.ID, &b.CreatedAt, &b.UpdatedAt))
}

func (s *Store) UpdateBook(ctx context.Context, q Querier, b *Book) error {
	_, err := q.Exec(ctx, `UPDATE books SET title = $2, authors = $3, year = $4, cover_url = $5, goodreads_url = $6,
		openlibrary_key = $7, notes = $8, on_wishlist = $9, updated_at = now() WHERE id = $1`,
		b.ID, b.Title, b.Authors, b.Year, b.CoverURL, b.GoodreadsURL, b.OpenLibraryKey, b.Notes, b.OnWishlist)
	return wrap(err)
}

func (s *Store) SetWishlist(ctx context.Context, q Querier, id int64, on bool) error {
	_, err := q.Exec(ctx, `UPDATE books SET on_wishlist = $2, updated_at = now() WHERE id = $1`, id, on)
	return err
}

// DeleteBook refuses (ErrInUse) if the book is part of a meeting or a vote.
func (s *Store) DeleteBook(ctx context.Context, q Querier, id int64) error {
	_, err := q.Exec(ctx, `DELETE FROM books WHERE id = $1`, id)
	return wrap(err)
}

// BookUsage says whether a book is referenced by meetings or votes.
func (s *Store) BookUsage(ctx context.Context, id int64) (meetings, polls int, err error) {
	err = s.Pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM meeting_books WHERE book_id = $1),
		(SELECT count(*) FROM poll_options WHERE book_id = $1)`, id).Scan(&meetings, &polls)
	return
}

// BookNomination is a vote a book took part in.
type BookNomination struct {
	PollID          int64
	PollTitle       string
	Status          string
	Chosen          bool
	NominatedByName string
	Pitch           string
	CreatedAt       time.Time
}

func (s *Store) BookNominations(ctx context.Context, bookID int64) ([]BookNomination, error) {
	rows, err := s.Pool.Query(ctx, `SELECT p.id, p.title, p.status, o.chosen, coalesce(u.display_name, ''), o.pitch, o.created_at
		FROM poll_options o JOIN polls p ON p.id = o.poll_id LEFT JOIN users u ON u.id = o.nominated_by
		WHERE o.book_id = $1 ORDER BY o.created_at DESC`, bookID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BookNomination
	for rows.Next() {
		var n BookNomination
		if err := rows.Scan(&n.PollID, &n.PollTitle, &n.Status, &n.Chosen, &n.NominatedByName, &n.Pitch, &n.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}
