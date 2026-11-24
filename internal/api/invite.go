package api

import (
	"net/http"
	"strings"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/ibuben/squareball/internal/mail"
	"github.com/ibuben/squareball/internal/session"
)

// Ключевые слова IMAP, которыми помечается письмо-приглашение после ответа:
// карточка показывает, что уже ответили. Если сервер не принимает свои
// ключевые слова, ответ всё равно уходит, просто без пометки.
var rsvpFlags = map[string]string{
	mail.PartAccepted:  "$RSVPAccepted",
	mail.PartTentative: "$RSVPTentative",
	mail.PartDeclined:  "$RSVPDeclined",
}

// inviteSelf — от чьего имени отвечать: адрес ящика или отправителя, который
// есть среди участников; иначе — отправитель по умолчанию (или сам ящик).
func (s *Server) inviteSelf(r *http.Request, sess *session.Session, inv *mail.Invite) (mail.Address, bool) {
	cands := []mail.Address{{Email: sess.Mailbox}}
	if ids, err := s.store.Identities(r.Context(), sess.Mailbox); err == nil {
		for _, id := range ids {
			a := mail.Address{Name: id.Name, Email: id.Email}
			if id.IsDefault {
				cands = append([]mail.Address{a}, cands...)
			} else {
				cands = append(cands, a)
			}
		}
	}
	for _, c := range cands {
		for _, a := range inv.Attendees {
			if strings.EqualFold(a.Email, c.Email) && s.checkSender(sess.Mailbox, c.Email) {
				if c.Name == "" {
					c.Name = a.Name
				}
				return c, true
			}
		}
	}
	for _, c := range cands {
		if s.checkSender(sess.Mailbox, c.Email) {
			return c, false
		}
	}
	return mail.Address{Email: sess.Mailbox}, false
}

// inviteView — что знает интерфейс о приглашении сверх самого события.
func (s *Server) inviteView(r *http.Request, m *mail.Message) map[string]any {
	if m.Invite == nil {
		return nil
	}
	sess := sessionFrom(r.Context())
	self, attendee := s.inviteSelf(r, sess, m.Invite)
	organizer := m.Invite.Organizer != nil && strings.EqualFold(m.Invite.Organizer.Email, self.Email)
	replied := ""
	for ps, f := range rsvpFlags {
		for _, fl := range m.Flags {
			if strings.EqualFold(fl, f) {
				replied = ps
			}
		}
	}
	return map[string]any{
		"self":         self.Email,
		"attendee":     attendee,
		"is_organizer": organizer,
		"replied":      replied,
		"can_reply":    m.Invite.Method == "REQUEST" && m.Invite.Organizer != nil && !organizer && m.Invite.Status != "CANCELLED",
	}
}

// handleInviteReply отвечает организатору на приглашение: METHOD:REPLY с PARTSTAT.
func (s *Server) handleInviteReply(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	var in struct {
		Folder   string `json:"folder"`
		UID      uint32 `json:"uid"`
		PartStat string `json:"partstat"`
		Comment  string `json:"comment"`
	}
	if err := readJSON(r, &in); err != nil || in.Folder == "" || in.UID == 0 || rsvpFlags[strings.ToUpper(in.PartStat)] == "" ||
		len(in.Comment) > 2000 {
		writeErr(w, http.StatusBadRequest, "invalid", "")
		return
	}
	in.PartStat = strings.ToUpper(in.PartStat)
	var m *mail.Message
	if !s.withIMAP(w, r, func(c *imapclient.Client) (err error) {
		m, err = mail.GetMessage(c, in.Folder, in.UID, false)
		return err
	}) {
		return
	}
	if v := s.inviteView(r, m); v == nil || v["can_reply"] != true {
		writeErr(w, http.StatusConflict, "invite.no_reply", "")
		return
	}
	if !s.sendUser.Allow(sess.Mailbox) {
		writeErr(w, http.StatusTooManyRequests, "send.rate_limited", "")
		return
	}
	self, _ := s.inviteSelf(r, sess, m.Invite)
	raw, to, err := mail.BuildInviteReply(mail.InviteReply{
		ICS: m.InviteICS, From: self, PartStat: in.PartStat, Comment: in.Comment,
		Subject: m.Subject, MessageID: m.MessageID, Lang: s.Lang(r),
	})
	if err != nil {
		writeErr(w, http.StatusUnprocessableEntity, "invite.invalid", "")
		return
	}
	cr := s.smtpCreds(sess)
	if cr.Username == "" {
		writeErr(w, http.StatusForbidden, "send.not_allowed", "")
		return
	}
	if err := mail.Send(&s.cfg.SMTP, cr, self.Email, []string{to}, raw, false); err != nil {
		s.mailError(w, r, err)
		return
	}
	s.audit.Info("invite reply sent", "mailbox", sess.Mailbox, "to", to, "partstat", in.PartStat, "ip", s.ClientIP(r))

	// Копия в «Отправленные» и пометка письма; ошибки здесь не отменяют ответ.
	_ = s.pool.With(sess.ID, s.creds(sess), func(c *imapclient.Client) error {
		if s.cfg.SMTP.SaveSent {
			if fs, err := s.folders(c); err == nil {
				if sent := mail.FolderByRole(fs, mail.RoleSent); sent != "" {
					_, _ = mail.Append(c, sent, raw, imap.FlagSeen)
				}
			}
		}
		var old []string
		for ps, f := range rsvpFlags {
			if ps != in.PartStat {
				old = append(old, f)
			}
		}
		_ = mail.SetFlags(c, in.Folder, []uint32{in.UID}, old, false)
		_ = mail.SetFlags(c, in.Folder, []uint32{in.UID}, []string{rsvpFlags[in.PartStat], string(imap.FlagAnswered)}, true)
		return nil
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "partstat": in.PartStat, "to": to})
}
