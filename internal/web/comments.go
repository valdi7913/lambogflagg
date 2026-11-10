package web

import (
	"fmt"
	"net/http"
	"strings"

	"bookclub/internal/i18n"
	"bookclub/internal/store"
	"bookclub/internal/views"
)

const maxCommentLen = 10000

// subjectArg names what a comment is on, for history messages.
func (s *Server) subjectArg(r *http.Request, subjectType string, id int64) string {
	ctx := r.Context()
	switch subjectType {
	case "book":
		if b, err := s.st.GetBook(ctx, id); err == nil {
			return i18n.ArgSubject("book", b.Title)
		}
	case "meeting":
		if m, err := s.st.GetMeeting(ctx, id); err == nil {
			return i18n.ArgSubject("meeting", meetingArg(m))
		}
	case "poll":
		if p, err := s.st.GetPoll(ctx, id); err == nil {
			return i18n.ArgSubject("poll", p.Title)
		}
	}
	return "?"
}

func (s *Server) createComment(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	u := user(r)
	subjectType := r.FormValue("subject_type")
	subjectID := formInt64(r, "subject_id")
	if !s.st.SubjectExists(ctx, subjectType, subjectID) {
		return errNotFound
	}
	body := strings.TrimSpace(r.FormValue("body"))
	back := fmt.Sprintf("%s#comments", subjectPath(subjectType, subjectID))
	if body == "" || len(body) > maxCommentLen {
		if isHTMX(r) {
			d, err := s.comments(ctx, subjectType, subjectID)
			if err != nil {
				return err
			}
			d.Error = tr(r, "Write something first (up to 10,000 characters).")
			return ok(w, r, views.CommentsSection(d))
		}
		return redirect(w, r, back)
	}
	c := &store.Comment{UserID: &u.ID, SubjectType: subjectType, SubjectID: subjectID, Body: body}
	err := s.st.Tx(ctx, func(q store.Querier) error {
		if err := s.st.CreateComment(ctx, q, c); err != nil {
			return err
		}
		return s.st.Log(ctx, q, u.ID, "comment", subjectType, subjectID,
			store.M("commented on %s", s.subjectArg(r, subjectType, subjectID)), nil)
	})
	if err != nil {
		return err
	}
	if isHTMX(r) {
		d, err := s.comments(ctx, subjectType, subjectID)
		if err != nil {
			return err
		}
		return ok(w, r, views.CommentsSection(d))
	}
	return redirect(w, r, back)
}

func subjectPath(subjectType string, id int64) string {
	switch subjectType {
	case "book":
		return fmt.Sprintf("/books/%d", id)
	case "meeting":
		return fmt.Sprintf("/meetings/%d", id)
	case "poll":
		return fmt.Sprintf("/polls/%d", id)
	}
	return "/"
}

func (s *Server) loadComment(r *http.Request, mustOwn bool) (*store.Comment, error) {
	id, err := pathID(r, "id")
	if err != nil {
		return nil, err
	}
	c, err := s.st.GetComment(r.Context(), id)
	if err != nil {
		return nil, err
	}
	u := user(r)
	own := c.UserID != nil && *c.UserID == u.ID
	if mustOwn && !own {
		return nil, errForbidden
	}
	if !mustOwn && !own && !u.IsAdmin {
		return nil, errForbidden
	}
	return c, nil
}

func (s *Server) showComment(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	c, err := s.st.GetComment(r.Context(), id)
	if err != nil {
		return err
	}
	if !isHTMX(r) {
		return redirect(w, r, subjectPath(c.SubjectType, c.SubjectID)+"#comments")
	}
	return ok(w, r, views.CommentItem(c))
}

func (s *Server) editComment(w http.ResponseWriter, r *http.Request) error {
	c, err := s.loadComment(r, true)
	if err != nil {
		return err
	}
	if !isHTMX(r) {
		return redirect(w, r, subjectPath(c.SubjectType, c.SubjectID)+"#comments")
	}
	return ok(w, r, views.CommentEdit(c, ""))
}

func (s *Server) updateComment(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	c, err := s.loadComment(r, true)
	if err != nil {
		return err
	}
	body := strings.TrimSpace(r.FormValue("body"))
	if body == "" || len(body) > maxCommentLen {
		if isHTMX(r) {
			return ok(w, r, views.CommentEdit(c, tr(r, "Write something (up to 10,000 characters).")))
		}
		return redirect(w, r, subjectPath(c.SubjectType, c.SubjectID)+"#comments")
	}
	if body != c.Body {
		if err := s.st.UpdateComment(ctx, s.st.Pool, c.ID, body); err != nil {
			return err
		}
	}
	if !isHTMX(r) {
		return redirect(w, r, subjectPath(c.SubjectType, c.SubjectID)+"#comments")
	}
	c, err = s.st.GetComment(ctx, c.ID)
	if err != nil {
		return err
	}
	return ok(w, r, views.CommentItem(c))
}

func (s *Server) deleteComment(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	c, err := s.loadComment(r, false)
	if err != nil {
		return err
	}
	u := user(r)
	err = s.st.Tx(ctx, func(q store.Querier) error {
		if err := s.st.DeleteComment(ctx, q, c.ID); err != nil {
			return err
		}
		subject := s.subjectArg(r, c.SubjectType, c.SubjectID)
		msg := store.M("deleted their own comment on %s", subject)
		if c.UserID == nil || *c.UserID != u.ID {
			msg = store.M("deleted a comment by %s on %s", c.UserName, subject)
		}
		return s.st.Log(ctx, q, u.ID, "comment", c.SubjectType, c.SubjectID, msg, nil)
	})
	if err != nil {
		return err
	}
	if isHTMX(r) {
		return ok(w, r, views.CommentDeleted())
	}
	return redirect(w, r, subjectPath(c.SubjectType, c.SubjectID)+"#comments")
}
