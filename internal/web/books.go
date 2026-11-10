package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"bookclub/internal/store"
	"bookclub/internal/views"
)

func (s *Server) home(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	s.closeDuePolls(ctx)
	var d views.HomeData
	var err error
	if d.Upcoming, err = s.st.UpcomingMeetings(ctx, 3); err != nil {
		return err
	}
	past, err := s.st.PastMeetings(ctx)
	if err != nil {
		return err
	}
	if len(past) > 0 {
		d.LastMeeting = past[0]
	}
	if d.OpenPolls, err = s.st.OpenPolls(ctx); err != nil {
		return err
	}
	if d.Voted, err = s.st.VotedPollIDs(ctx, user(r).ID); err != nil {
		return err
	}
	if d.Activity, err = s.st.RecentActivity(ctx, 12, 0); err != nil {
		return err
	}
	wish, err := s.st.ListBooks(ctx, "wishlist", "")
	if err != nil {
		return err
	}
	d.WishlistCount = len(wish)
	return ok(w, r, views.HomePage(d))
}

func (s *Server) activity(w http.ResponseWriter, r *http.Request) error {
	before, _ := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
	const pageSize = 100
	items, err := s.st.RecentActivity(r.Context(), pageSize, before)
	if err != nil {
		return err
	}
	d := views.ActivityData{Items: items}
	if len(items) == pageSize {
		d.NextBefore = items[len(items)-1].ID
	}
	return ok(w, r, views.ActivityPage(d))
}

// ---- Books ----

func (s *Server) listBooks(w http.ResponseWriter, r *http.Request) error {
	filter := r.URL.Query().Get("filter")
	if filter != "read" && filter != "all" {
		filter = "wishlist"
	}
	q := r.URL.Query().Get("q")
	books, err := s.st.ListBooks(r.Context(), filter, q)
	if err != nil {
		return err
	}
	return ok(w, r, views.BooksPage(views.BooksData{Filter: filter, Query: q, Books: books}))
}

func (s *Server) newBook(w http.ResponseWriter, r *http.Request) error {
	d := views.BookFormData{Book: &store.Book{OnWishlist: true}, IsNew: true, Errors: map[string]string{}}
	if pid, _ := strconv.ParseInt(r.URL.Query().Get("nominate"), 10, 64); pid > 0 {
		if p, err := s.st.GetPoll(r.Context(), pid); err == nil && p.AcceptsNominations() {
			d.NominatePoll, d.PollTitle = p.ID, p.Title
		}
	}
	return ok(w, r, views.BookFormPage(d))
}

func readBookForm(r *http.Request, b *store.Book) map[string]string {
	errs := map[string]string{}
	b.Title = formStr(r, "title")
	b.Authors = formStr(r, "authors")
	b.CoverURL = formStr(r, "cover_url")
	b.GoodreadsURL = formStr(r, "goodreads_url")
	b.OpenLibraryKey = formStr(r, "openlibrary_key")
	b.Notes = formStr(r, "notes")
	b.OnWishlist = formBool(r, "on_wishlist")
	b.Year = nil
	if y := formStr(r, "year"); y != "" {
		n, err := strconv.Atoi(y)
		if err != nil || n < -3000 || n > 3000 {
			errs["year"] = tr(r, "Enter a year like 1984.")
		} else {
			b.Year = &n
		}
	}
	if b.Title == "" {
		errs["title"] = tr(r, "A book needs a title.")
	}
	if !validHTTPURL(b.GoodreadsURL) {
		errs["goodreads_url"] = tr(r, "Enter a full link starting with https://")
	}
	if !validHTTPURL(b.CoverURL) {
		errs["cover_url"] = tr(r, "Enter a full link starting with https://")
	}
	return errs
}

func (s *Server) createBook(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	u := user(r)
	b := &store.Book{AddedBy: &u.ID}
	errs := readBookForm(r, b)
	pollID := formInt64(r, "nominate_poll")
	var pollTitle string
	if pollID > 0 {
		if p, err := s.st.GetPoll(ctx, pollID); err == nil {
			pollTitle = p.Title
		} else {
			pollID = 0
		}
	}
	if len(errs) > 0 {
		return render(w, r, http.StatusUnprocessableEntity, views.BookFormPage(views.BookFormData{
			Book: b, IsNew: true, Errors: errs, NominatePoll: pollID, PollTitle: pollTitle}))
	}
	if b.OpenLibraryKey != "" {
		if existing, err := s.st.BookByOpenLibraryKey(ctx, b.OpenLibraryKey); err == nil {
			if pollID > 0 {
				if err := s.nominateBookTx(ctx, pollID, existing.ID, "", u.ID); err != nil {
					s.flash(w, r, "error", "That book is already in the library, but couldn't be suggested: %s", errText(r, err))
				} else {
					s.flash(w, r, "ok", "That book was already in the library, so it was suggested from there.")
				}
				return redirect(w, r, fmt.Sprintf("/polls/%d", pollID))
			}
			return render(w, r, http.StatusConflict, views.BookFormPage(views.BookFormData{
				Book: b, IsNew: true, Errors: errs, Existing: existing}))
		}
	}
	err := s.st.Tx(ctx, func(q store.Querier) error {
		if err := s.st.CreateBook(ctx, q, b); err != nil {
			return err
		}
		return s.st.Log(ctx, q, u.ID, "create", "book", b.ID, store.M("added the book “%s”", b.Title), nil)
	})
	if err != nil {
		return err
	}
	if pollID > 0 {
		if err := s.nominateBookTx(ctx, pollID, b.ID, "", u.ID); err != nil {
			s.flash(w, r, "error", "Book added, but it couldn't be suggested: %s", errText(r, err))
		} else {
			s.flash(w, r, "ok", "Book added and suggested.")
		}
		return redirect(w, r, fmt.Sprintf("/polls/%d", pollID))
	}
	s.flash(w, r, "ok", "Book added.")
	return redirect(w, r, fmt.Sprintf("/books/%d", b.ID))
}

func (s *Server) lookupBooks(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query().Get("q")
	if len([]rune(q)) < 2 {
		return ok(w, r, views.LookupResults(nil, "", ""))
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	results, err := s.ol.Search(ctx, q)
	if err != nil {
		return ok(w, r, views.LookupResults(nil, tr(r, "Couldn't reach Open Library right now. Fill in the details by hand."), q))
	}
	return ok(w, r, views.LookupResults(results, "", q))
}

func (s *Server) showBook(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	b, err := s.st.GetBook(ctx, id)
	if err != nil {
		return err
	}
	d := views.BookData{Book: b}
	if d.Meetings, err = s.st.BookMeetings(ctx, id); err != nil {
		return err
	}
	if d.Nominations, err = s.st.BookNominations(ctx, id); err != nil {
		return err
	}
	if d.Comments, err = s.comments(ctx, "book", id); err != nil {
		return err
	}
	if d.History, err = s.st.SubjectActivity(ctx, "book", id); err != nil {
		return err
	}
	d.CanDelete = len(d.Meetings) == 0 && len(d.Nominations) == 0
	return ok(w, r, views.BookPage(d))
}

func (s *Server) editBook(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	b, err := s.st.GetBook(r.Context(), id)
	if err != nil {
		return err
	}
	return ok(w, r, views.BookFormPage(views.BookFormData{Book: b, Errors: map[string]string{}}))
}

func (s *Server) updateBook(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	old, err := s.st.GetBook(ctx, id)
	if err != nil {
		return err
	}
	b := *old
	errs := readBookForm(r, &b)
	if len(errs) > 0 {
		return render(w, r, http.StatusUnprocessableEntity, views.BookFormPage(views.BookFormData{Book: &b, Errors: errs}))
	}
	changes := store.Diff([][3]string{
		{"title", old.Title, b.Title},
		{"authors", old.Authors, b.Authors},
		{"year", ptrIntStr(old.Year), ptrIntStr(b.Year)},
		{"Goodreads link", old.GoodreadsURL, b.GoodreadsURL},
		{"cover", old.CoverURL, b.CoverURL},
		{"notes", old.Notes, b.Notes},
		{"on want-to-read list", boolStr(old.OnWishlist), boolStr(b.OnWishlist)},
	})
	if len(changes) > 0 {
		err = s.st.Tx(ctx, func(q store.Querier) error {
			if err := s.st.UpdateBook(ctx, q, &b); err != nil {
				return err
			}
			return s.st.Log(ctx, q, user(r).ID, "update", "book", b.ID, store.M("edited the book “%s”", b.Title), changes)
		})
		if err != nil {
			return err
		}
	}
	s.flash(w, r, "ok", "Saved.")
	return redirect(w, r, fmt.Sprintf("/books/%d", id))
}

func (s *Server) toggleWishlist(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	b, err := s.st.GetBook(ctx, id)
	if err != nil {
		return err
	}
	on := formBool(r, "on")
	if on != b.OnWishlist {
		summary := store.M("added “%s” to the want-to-read list", b.Title)
		if !on {
			summary = store.M("took “%s” off the want-to-read list", b.Title)
		}
		err = s.st.Tx(ctx, func(q store.Querier) error {
			if err := s.st.SetWishlist(ctx, q, id, on); err != nil {
				return err
			}
			return s.st.Log(ctx, q, user(r).ID, "update", "book", id, summary, nil)
		})
		if err != nil {
			return err
		}
		b.OnWishlist = on
	}
	if isHTMX(r) {
		return ok(w, r, views.WishlistToggle(b))
	}
	return redirect(w, r, fmt.Sprintf("/books/%d", id))
}

func (s *Server) deleteBook(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	b, err := s.st.GetBook(ctx, id)
	if err != nil {
		return err
	}
	err = s.st.Tx(ctx, func(q store.Querier) error {
		if err := s.st.DeleteBook(ctx, q, id); err != nil {
			return err
		}
		return s.st.Log(ctx, q, user(r).ID, "delete", "book", id, store.M("deleted the book “%s”", b.Title), nil)
	})
	if errors.Is(err, store.ErrInUse) {
		s.flash(w, r, "error", "This book is part of a meeting or a vote, so it can't be deleted. You can take it off the want-to-read list instead.")
		return redirect(w, r, fmt.Sprintf("/books/%d", id))
	}
	if err != nil {
		return err
	}
	s.flash(w, r, "ok", "Book deleted.")
	return redirect(w, r, "/books")
}
