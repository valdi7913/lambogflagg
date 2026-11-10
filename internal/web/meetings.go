package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"bookclub/internal/store"
	"bookclub/internal/views"
)

func (s *Server) listMeetings(w http.ResponseWriter, r *http.Request) error {
	up, err := s.st.UpcomingMeetings(r.Context(), 1000)
	if err != nil {
		return err
	}
	past, err := s.st.PastMeetings(r.Context())
	if err != nil {
		return err
	}
	return ok(w, r, views.MeetingsPage(views.MeetingsData{Upcoming: up, Past: past}))
}

func (s *Server) newMeeting(w http.ResponseWriter, r *http.Request) error {
	return ok(w, r, views.MeetingFormPage(views.MeetingFormData{Meeting: &store.Meeting{}, IsNew: true, Errors: map[string]string{}}))
}

func (s *Server) readMeetingForm(r *http.Request, m *store.Meeting) (string, map[string]string) {
	errs := map[string]string{}
	m.Title = formStr(r, "title")
	m.Place = formStr(r, "place")
	m.Description = formStr(r, "description")
	raw := formStr(r, "starts_at")
	t, err := s.parseLocalTime(raw)
	if err != nil {
		errs["starts_at"] = tr(r, "Enter a valid date and time.")
	}
	m.StartsAt = t
	return raw, errs
}

func (s *Server) createMeeting(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	u := user(r)
	m := &store.Meeting{CreatedBy: &u.ID}
	raw, errs := s.readMeetingForm(r, m)
	if len(errs) > 0 {
		return render(w, r, http.StatusUnprocessableEntity, views.MeetingFormPage(views.MeetingFormData{Meeting: m, StartsAt: raw, IsNew: true, Errors: errs}))
	}
	err := s.st.Tx(ctx, func(q store.Querier) error {
		if err := s.st.CreateMeeting(ctx, q, m); err != nil {
			return err
		}
		return s.st.Log(ctx, q, u.ID, "create", "meeting", m.ID, store.M("planned “%s”", meetingArg(m)), nil)
	})
	if err != nil {
		return err
	}
	s.flash(w, r, "ok", "Meeting saved. Add the book(s) below.")
	return redirect(w, r, fmt.Sprintf("/meetings/%d", m.ID))
}

func (s *Server) comments(ctx context.Context, subjectType string, id int64) (views.CommentsData, error) {
	cs, err := s.st.ListComments(ctx, subjectType, id)
	return views.CommentsData{SubjectType: subjectType, SubjectID: id, Comments: cs}, err
}

func (s *Server) showMeeting(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	m, err := s.st.GetMeeting(ctx, id)
	if err != nil {
		return err
	}
	d := views.MeetingData{Meeting: m}
	if d.Polls, err = s.st.MeetingPolls(ctx, id); err != nil {
		return err
	}
	if d.AllBooks, err = s.st.BooksForSelect(ctx); err != nil {
		return err
	}
	if d.Comments, err = s.comments(ctx, "meeting", id); err != nil {
		return err
	}
	if d.History, err = s.st.SubjectActivity(ctx, "meeting", id); err != nil {
		return err
	}
	return ok(w, r, views.MeetingPage(d))
}

func (s *Server) editMeeting(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	m, err := s.st.GetMeeting(r.Context(), id)
	if err != nil {
		return err
	}
	return ok(w, r, views.MeetingFormPage(views.MeetingFormData{Meeting: m, StartsAt: views.InputDateTime(m.StartsAt), Errors: map[string]string{}}))
}

func (s *Server) updateMeeting(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	old, err := s.st.GetMeeting(ctx, id)
	if err != nil {
		return err
	}
	m := *old
	raw, errs := s.readMeetingForm(r, &m)
	if len(errs) > 0 {
		return render(w, r, http.StatusUnprocessableEntity, views.MeetingFormPage(views.MeetingFormData{Meeting: &m, StartsAt: raw, Errors: errs}))
	}
	loc := s.cfg.Location
	changes := store.Diff([][3]string{
		{"title", old.Title, m.Title},
		{"date", timeStr(old.StartsAt, loc), timeStr(m.StartsAt, loc)},
		{"place", old.Place, m.Place},
		{"description", old.Description, m.Description},
	})
	if len(changes) > 0 {
		err = s.st.Tx(ctx, func(q store.Querier) error {
			if err := s.st.UpdateMeeting(ctx, q, &m); err != nil {
				return err
			}
			return s.st.Log(ctx, q, user(r).ID, "update", "meeting", id, store.M("edited “%s”", meetingArg(&m)), changes)
		})
		if err != nil {
			return err
		}
	}
	s.flash(w, r, "ok", "Saved.")
	return redirect(w, r, fmt.Sprintf("/meetings/%d", id))
}

func (s *Server) deleteMeeting(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	m, err := s.st.GetMeeting(ctx, id)
	if err != nil {
		return err
	}
	err = s.st.Tx(ctx, func(q store.Querier) error {
		if err := s.st.DeleteMeeting(ctx, q, id); err != nil {
			return err
		}
		return s.st.Log(ctx, q, user(r).ID, "delete", "meeting", id, store.M("deleted the meeting “%s”", meetingArg(m)), nil)
	})
	if err != nil {
		return err
	}
	s.flash(w, r, "ok", "Meeting deleted.")
	return redirect(w, r, "/meetings")
}

// meetingBooksResponse re-renders the meeting's book list for htmx, or
// redirects back for plain form posts. errMsg is an English key.
func (s *Server) meetingBooksResponse(w http.ResponseWriter, r *http.Request, id int64, errMsg string) error {
	if !isHTMX(r) {
		if errMsg != "" {
			s.flash(w, r, "error", errMsg)
		}
		return redirect(w, r, fmt.Sprintf("/meetings/%d", id))
	}
	m, err := s.st.GetMeeting(r.Context(), id)
	if err != nil {
		return err
	}
	all, err := s.st.BooksForSelect(r.Context())
	if err != nil {
		return err
	}
	if errMsg != "" {
		errMsg = tr(r, errMsg)
	}
	return ok(w, r, views.MeetingBooks(m, all, errMsg))
}

func (s *Server) addMeetingBook(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	m, err := s.st.GetMeeting(ctx, id)
	if err != nil {
		return err
	}
	b, err := s.st.GetBook(ctx, formInt64(r, "book_id"))
	if err != nil {
		return s.meetingBooksResponse(w, r, id, msgChooseBook)
	}
	err = s.st.Tx(ctx, func(q store.Querier) error {
		if err := s.st.AddMeetingBook(ctx, q, id, b.ID, formStr(r, "note"), user(r).ID); err != nil {
			return err
		}
		return s.st.Log(ctx, q, user(r).ID, "update", "meeting", id,
			store.M("added “%s” to “%s”", b.Title, meetingArg(m)), nil)
	})
	if errors.Is(err, store.ErrDuplicate) {
		return s.meetingBooksResponse(w, r, id, msgAlreadyInMeeting)
	}
	if err != nil {
		return err
	}
	return s.meetingBooksResponse(w, r, id, "")
}

// Keys passed through meetingBooksResponse (listed with tr so the catalog
// test sees them).
var (
	msgChooseBook       = trKey("Choose a book to add.")
	msgAlreadyInMeeting = trKey("That book is already part of this meeting.")
)

func (s *Server) removeMeetingBook(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	bookID, err := pathID(r, "bookID")
	if err != nil {
		return err
	}
	m, err := s.st.GetMeeting(ctx, id)
	if err != nil {
		return err
	}
	b, err := s.st.GetBook(ctx, bookID)
	if err != nil {
		return err
	}
	err = s.st.Tx(ctx, func(q store.Querier) error {
		if err := s.st.RemoveMeetingBook(ctx, q, id, bookID); err != nil {
			return err
		}
		return s.st.Log(ctx, q, user(r).ID, "update", "meeting", id,
			store.M("removed “%s” from “%s”", b.Title, meetingArg(m)), nil)
	})
	if err != nil {
		return err
	}
	return s.meetingBooksResponse(w, r, id, "")
}
