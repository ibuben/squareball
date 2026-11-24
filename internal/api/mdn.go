package api

import (
	"net/http"
	"slices"
	"strings"

	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/ibuben/squareball/internal/mail"
)

// handleMDN отвечает на запрос уведомления о прочтении: send=true — отправить,
// false — отказаться. В обоих случаях письму ставится $MDNSent, и вопрос больше
// не задаётся (как в Roundcube и Thunderbird).
//
// auto=true — автоматическая отправка по настройке «всегда»: разрешена, только
// если адрес уведомления совпадает с отправителем и Return-Path письма
// (RFC 8098, 6.2); иначе 409 mdn.confirm — интерфейс спросит пользователя.
func (s *Server) handleMDN(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	var in struct {
		Folder string `json:"folder"`
		UID    uint32 `json:"uid"`
		Send   bool   `json:"send"`
		Auto   bool   `json:"auto"`
	}
	if err := readJSON(r, &in); err != nil || in.Folder == "" || in.UID == 0 {
		writeErr(w, http.StatusBadRequest, "invalid", "")
		return
	}
	var m *mail.Message
	if !s.withIMAP(w, r, func(c *imapclient.Client) (err error) {
		m, err = mail.GetMessage(c, in.Folder, in.UID, false)
		return err
	}) {
		return
	}
	if m.MDNTo == "" || slices.Contains(m.Flags, mail.FlagMDNSent) {
		writeErr(w, http.StatusConflict, "mdn.not_requested", "")
		return
	}
	if in.Send {
		if in.Auto && !mdnSafe(m) {
			writeErr(w, http.StatusConflict, "mdn.confirm", "")
			return
		}
		if !s.sendUser.Allow(sess.Mailbox) {
			writeErr(w, http.StatusTooManyRequests, "send.rate_limited", "")
			return
		}
		raw, err := mail.BuildMDN(mail.MDN{
			From: mail.Address{Email: sess.Mailbox}, To: m.MDNTo, Subject: m.Subject,
			MessageID: m.MessageID, Date: m.Date, Automatic: in.Auto,
		})
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "internal", "")
			return
		}
		cr := s.smtpCreds(sess)
		if cr.Username == "" {
			writeErr(w, http.StatusForbidden, "send.not_allowed", "")
			return
		}
		if err := mail.Send(&s.cfg.SMTP, cr, sess.Mailbox, []string{m.MDNTo}, raw, false); err != nil {
			s.mailError(w, r, err)
			return
		}
		s.audit.Info("mdn sent", "mailbox", sess.Mailbox, "to", m.MDNTo, "auto", in.Auto)
	}
	if s.withIMAP(w, r, func(c *imapclient.Client) error {
		return mail.SetFlags(c, in.Folder, []uint32{in.UID}, []string{mail.FlagMDNSent}, true)
	}) {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true, "sent": in.Send})
	}
}

// mdnSafe — уведомление уходит тому же адресу, что указан отправителем и в Return-Path.
func mdnSafe(m *mail.Message) bool {
	to := strings.ToLower(m.MDNTo)
	if len(m.From) == 0 || strings.ToLower(m.From[0].Email) != to {
		return false
	}
	return m.ReturnPath == "" || strings.ToLower(m.ReturnPath) == to
}
