package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/ibuben/squareball/ext"
	"github.com/ibuben/squareball/internal/calendar"
	"github.com/ibuben/squareball/internal/mail"
	"github.com/ibuben/squareball/internal/session"
)

// Встречи с участниками: организатор — ящик (или его отправитель), письма
// iTIP уходят через SMTP ящика. Ошибка отправки не отменяет сохранение
// события: интерфейс получает notify_error и сообщает о ней.

var errNotifyLimit = errors.New("notify: rate limited")

// ownAddresses — адреса, от которых ящик может писать; отправитель по умолчанию первым.
func (s *Server) ownAddresses(r *http.Request, sess *session.Session) []mail.Address {
	out := []mail.Address{{Email: sess.Mailbox}}
	if ids, err := s.store.Identities(r.Context(), sess.Mailbox); err == nil {
		for _, id := range ids {
			a := mail.Address{Name: id.Name, Email: id.Email}
			if !s.checkSender(sess.Mailbox, a.Email) {
				continue
			}
			if id.IsDefault {
				out = append([]mail.Address{a}, out...)
			} else {
				out = append(out, a)
			}
		}
	}
	return out
}

// ownAddress — адрес ящика среди его отправителей.
func (s *Server) ownAddress(r *http.Request, sess *session.Session, email string) (mail.Address, bool) {
	for _, a := range s.ownAddresses(r, sess) {
		if strings.EqualFold(a.Email, email) {
			return a, true
		}
	}
	return mail.Address{}, false
}

// meetingRole — кто ящик во встрече из объекта orig (nil — новое событие):
// organizer — адрес, от которого рассылать приглашения; foreign — встреча
// чужая (ящик — участник), её участников менять и рассылать нельзя.
func (s *Server) meetingRole(r *http.Request, sess *session.Session, orig []byte) (organizer mail.Address, attendees []calendar.Person, foreign bool) {
	if orig != nil {
		org, atts, err := calendar.Meeting(orig)
		if err == nil {
			attendees = atts
			if org != nil {
				a, ok := s.ownAddress(r, sess, org.Email)
				if !ok {
					return mail.Address{}, atts, true
				}
				if a.Name == "" {
					a.Name = org.Name
				}
				return a, atts, false
			}
		}
	}
	return s.ownAddresses(r, sess)[0], attendees, false
}

// sendMeeting отправляет письмо iTIP участникам to (кроме самого ящика).
// Возвращает, скольким адресатам ушло письмо.
func (s *Server) sendMeeting(r *http.Request, sess *session.Session, from mail.Address, ics []byte, to []calendar.Person) (int, error) {
	var rcpt []mail.Address
	var env []string
	for _, p := range to {
		if _, own := s.ownAddress(r, sess, p.Email); own {
			continue
		}
		rcpt = append(rcpt, mail.Address{Name: p.Name, Email: p.Email})
		env = append(env, p.Email)
	}
	if len(rcpt) == 0 {
		return 0, nil
	}
	if max := s.cfg.SMTP.RateLimit.MaxRecipients; max > 0 && len(env) > max {
		return 0, errTooManyRecipients
	}
	raw, err := mail.BuildInviteMail(mail.InviteMail{ICS: ics, From: from, To: rcpt, Lang: s.Lang(r)})
	if err != nil {
		return 0, err
	}
	cr := s.smtpCreds(sess)
	if cr.Username == "" {
		return 0, errSendNotAllowed
	}
	if err := s.sendMail(&s.cfg.SMTP, cr, from.Email, env, raw, false); err != nil {
		return 0, err
	}
	s.audit.Info("meeting mail sent", "mailbox", sess.Mailbox, "recipients", len(env), "ip", s.ClientIP(r))
	return len(env), nil
}

var (
	errTooManyRecipients = errors.New("notify: too many recipients")
	errSendNotAllowed    = errors.New("notify: send not allowed")
)

func notifyErrCode(err error) string {
	switch {
	case errors.Is(err, errNotifyLimit):
		return "send.rate_limited"
	case errors.Is(err, errTooManyRecipients):
		return "send.too_many_recipients"
	case errors.Is(err, errSendNotAllowed):
		return "send.not_allowed"
	default:
		return "send.failed"
	}
}

// notifySaved рассылает изменения встречи после сохранения: REQUEST всем
// участникам и CANCEL тем, кого из встречи убрали.
func (s *Server) notifySaved(r *http.Request, sess *session.Session, from mail.Address, ics []byte, before []calendar.Person) (int, error) {
	_, after, err := calendar.Meeting(ics)
	if err != nil {
		return 0, err
	}
	keep := map[string]bool{}
	for _, a := range after {
		keep[strings.ToLower(a.Email)] = true
	}
	var removed []calendar.Person
	var removedEmails []string
	for _, a := range before {
		if !keep[strings.ToLower(a.Email)] {
			removed = append(removed, a)
			removedEmails = append(removedEmails, a.Email)
		}
	}
	if len(after)+len(removed) == 0 {
		return 0, nil
	}
	if !s.sendUser.Allow(sess.Mailbox) {
		return 0, errNotifyLimit
	}
	sent := 0
	now := time.Now()
	if len(after) > 0 {
		req, err := calendar.Request(ics, now)
		if err != nil {
			return 0, err
		}
		n, err := s.sendMeeting(r, sess, from, req, after)
		if err != nil {
			return 0, err
		}
		sent += n
	}
	if len(removed) > 0 {
		cancel, err := calendar.Cancel(ics, "", removedEmails, false, now)
		if err != nil {
			return sent, err
		}
		n, err := s.sendMeeting(r, sess, from, cancel, removed)
		if err != nil {
			return sent, err
		}
		sent += n
	}
	return sent, nil
}

// notifyCancelled рассылает отмену встречи (или одного вхождения) участникам.
func (s *Server) notifyCancelled(r *http.Request, sess *session.Session, from mail.Address, ics []byte, recurrenceID string) (int, error) {
	_, atts, err := calendar.Meeting(ics)
	if err != nil || len(atts) == 0 {
		return 0, err
	}
	if !s.sendUser.Allow(sess.Mailbox) {
		return 0, errNotifyLimit
	}
	cancel, err := calendar.Cancel(ics, recurrenceID, nil, recurrenceID == "", time.Now())
	if err != nil {
		return 0, err
	}
	return s.sendMeeting(r, sess, from, cancel, atts)
}

// applyReply записывает ответ участника во встречу организатора: ищет её по
// UID во всех календарях ящика с правом записи.
func (s *Server) applyReply(r *http.Request, src ext.CalendarSource, m *mail.Message) (found, updated bool, err error) {
	if len(m.Invite.Attendees) == 0 || len(m.From) == 0 {
		return false, false, nil
	}
	from := m.From[0].Email
	cals, _ := s.calendars(r, src, false)
	for _, c := range cals {
		if c.ReadOnly {
			continue
		}
		o, err := src.FindUID(r.Context(), c.acc, c.URL, m.Invite.UID)
		if errors.Is(err, ext.ErrContactNotFound) {
			continue
		}
		if err != nil {
			return false, false, err
		}
		out, changed, err := calendar.ApplyReply(o.ICS, m.InviteICS, from)
		if err != nil || !changed {
			return true, false, err
		}
		_, _, err = src.PutObject(r.Context(), c.acc, c.URL, o.Href, o.ETag, out)
		return true, err == nil, err
	}
	return false, false, nil
}
