package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/ibuben/squareball/internal/mail"
	"github.com/ibuben/squareball/internal/sanitize"
	"github.com/ibuben/squareball/internal/store"
)

func (s *Server) handleIdentities(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	ids, err := s.store.Identities(r.Context(), sess.Mailbox)
	if err != nil {
		s.storeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"identities": ids, "level": s.cfg.Identities.Level})
}

// checkSender проверяет, может ли ящик указать этот адрес отправителя
// (identities.level). Postfix дополнительно проверяет sender login maps.
func (s *Server) checkSender(mailbox, email string) bool {
	email = strings.ToLower(strings.TrimSpace(email))
	if !validEmail(email) {
		return false
	}
	switch s.cfg.Identities.Level {
	case "any":
		return true
	case "domain":
		_, md, _ := strings.Cut(strings.ToLower(mailbox), "@")
		_, ed, _ := strings.Cut(email, "@")
		return md != "" && md == ed
	default:
		return email == strings.ToLower(mailbox)
	}
}

func validEmail(s string) bool {
	local, domain, ok := strings.Cut(s, "@")
	return ok && local != "" && domain != "" && !strings.ContainsAny(s, " <>,;\"\r\n")
}

// addrList проверяет строку вида "a@b, c@d" и возвращает её в нормальном виде.
func addrList(s string) (string, bool) {
	var out []string
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if !validEmail(strings.ToLower(p)) {
			return "", false
		}
		out = append(out, p)
	}
	return strings.Join(out, ", "), true
}

func (s *Server) handleSaveIdentity(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	var in store.Identity
	if err := readJSON(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid", "")
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	in.Email = strings.TrimSpace(in.Email)
	if !s.checkSender(sess.Mailbox, in.Email) {
		writeErr(w, http.StatusBadRequest, "identity.email_not_allowed", s.cfg.Identities.Level)
		return
	}
	var ok bool
	if in.ReplyTo, ok = addrList(in.ReplyTo); !ok {
		writeErr(w, http.StatusBadRequest, "identity.invalid_reply_to", "")
		return
	}
	if in.Bcc, ok = addrList(in.Bcc); !ok {
		writeErr(w, http.StatusBadRequest, "identity.invalid_bcc", "")
		return
	}
	if len(in.Name) > 200 || len(in.Signature) > 64<<10 {
		writeErr(w, http.StatusBadRequest, "identity.too_long", "")
		return
	}
	in.Signature = sanitize.Email(in.Signature, func(string) string { return "" }, true).HTML
	saved, err := s.store.SaveIdentity(r.Context(), sess.Mailbox, in)
	if err != nil {
		s.storeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, saved)
}

func (s *Server) handleDeleteIdentity(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	var in struct {
		ID int64 `json:"id"`
	}
	if err := readJSON(r, &in); err != nil || in.ID == 0 {
		writeErr(w, http.StatusBadRequest, "invalid", "")
		return
	}
	if err := s.store.DeleteIdentity(r.Context(), sess.Mailbox, in.ID); err != nil {
		s.storeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// applyIdentity подставляет отправителя из БД: From, Reply-To и скрытую копию.
// Адрес From никогда не берётся из запроса напрямую.
func (s *Server) applyIdentity(r *http.Request, id int64, d *mail.Draft) error {
	mailbox := sessionFrom(r.Context()).Mailbox
	var ident store.Identity
	var err error
	if id == 0 {
		var ids []store.Identity
		if ids, err = s.store.Identities(r.Context(), mailbox); err == nil {
			ident = ids[0] // по умолчанию — первый (is_default DESC)
		}
	} else {
		ident, err = s.store.Identity(r.Context(), mailbox, id)
	}
	if err != nil {
		return err
	}
	// Уровень мог стать строже после создания отправителя.
	if !s.checkSender(mailbox, ident.Email) {
		ident.Email = mailbox
	}
	d.From = mail.Address{Name: ident.Name, Email: ident.Email}
	for _, a := range strings.Split(ident.ReplyTo, ",") {
		if a = strings.TrimSpace(a); a != "" {
			d.ReplyTo = append(d.ReplyTo, mail.Address{Email: a})
		}
	}
	for _, a := range strings.Split(ident.Bcc, ",") {
		if a = strings.TrimSpace(a); a != "" {
			d.Bcc = append(d.Bcc, mail.Address{Email: a})
		}
	}
	return nil
}

func (s *Server) storeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeErr(w, http.StatusNotFound, "not_found", "")
	case errors.Is(err, store.ErrTooMany):
		writeErr(w, http.StatusConflict, "contacts.limit", "")
	case errors.Is(err, store.ErrLastOne):
		writeErr(w, http.StatusConflict, "identity.last", "")
	default:
		s.log.Error("store", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal", "")
	}
}
