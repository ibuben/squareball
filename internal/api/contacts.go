package api

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ibuben/squareball/internal/contacts"
	"github.com/ibuben/squareball/internal/mail"
	"github.com/ibuben/squareball/internal/store"
)

func (s *Server) contactRoutes() {
	m := s.mux
	m.HandleFunc("GET /api/v1/contacts", s.authed(s.handleContacts))
	m.HandleFunc("POST /api/v1/contacts", s.authed(s.handleSaveContact))
	m.HandleFunc("POST /api/v1/contacts/delete", s.authed(s.handleDeleteContacts))
	m.HandleFunc("GET /api/v1/contacts/suggest", s.authed(s.handleSuggest))
	m.HandleFunc("GET /api/v1/contacts/collected", s.authed(s.handleCollected))
	m.HandleFunc("POST /api/v1/contacts/collected/delete", s.authed(s.handleDeleteCollected))
	m.HandleFunc("POST /api/v1/contacts/import", s.authed(s.handleImportContacts))
	m.HandleFunc("GET /api/v1/contacts/export", s.authed(s.handleExportContacts))
	m.HandleFunc("GET /api/v1/groups", s.authed(s.handleGroups))
	m.HandleFunc("POST /api/v1/groups", s.authed(s.handleSaveGroup))
	m.HandleFunc("POST /api/v1/groups/delete", s.authed(s.handleDeleteGroup))
}

func (s *Server) handleContacts(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	group, _ := strconv.ParseInt(q.Get("group"), 10, 64)
	book := int64(-1) // все книги
	if v := q.Get("book"); v != "" {
		book, _ = strconv.ParseInt(v, 10, 64)
	}
	list, total, err := s.store.ContactsIn(r.Context(), sessionFrom(r.Context()).Mailbox, q.Get("q"), group, book,
		intParam(r, "limit", 100, 500), intParam(r, "offset", 0, 0))
	if err != nil {
		s.storeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"contacts": list, "total": total})
}

func validateContact(c *store.Contact) string {
	if len(c.Emails) > 20 || len(c.Phones) > 20 {
		return "too many addresses or phones"
	}
	for _, f := range []string{c.Name, c.First, c.Last, c.Org, c.Title} {
		if len(f) > 200 {
			return "field is too long"
		}
	}
	if len(c.Notes) > 8<<10 {
		return "notes are too long"
	}
	for _, e := range c.Emails {
		if v := strings.TrimSpace(e.Value); v != "" && !validEmail(strings.ToLower(v)) {
			return "invalid e-mail: " + v
		}
	}
	if (&store.Contact{Name: c.Name, First: c.First, Last: c.Last, Emails: c.Emails}).DisplayName() == "" {
		return "name or e-mail is required"
	}
	return ""
}

func (s *Server) handleSaveContact(w http.ResponseWriter, r *http.Request) {
	var in store.Contact
	if err := readJSON(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid", "")
		return
	}
	if msg := validateContact(&in); msg != "" {
		writeErr(w, http.StatusBadRequest, "contact.invalid", msg)
		return
	}
	mailbox := sessionFrom(r.Context()).Mailbox
	// Книга контакта: у нового — из запроса, у существующего — из БД
	// (перенос между книгами не поддерживается).
	meta := store.RemoteMeta{Book: in.Book}
	if in.ID != 0 {
		var err error
		if meta, err = s.store.ContactRemote(r.Context(), mailbox, in.ID); err != nil {
			s.storeError(w, err)
			return
		}
	}
	if meta.Book > 0 {
		saved, err := s.saveRemoteContact(r, in, meta)
		if err != nil {
			s.remoteError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, saved)
		return
	}
	in.Book = 0
	saved, err := s.store.SaveContact(r.Context(), mailbox, in)
	if err != nil {
		s.storeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, saved)
}

func (s *Server) handleDeleteContacts(w http.ResponseWriter, r *http.Request) {
	var in struct {
		IDs []int64 `json:"ids"`
	}
	if err := readJSON(r, &in); err != nil || len(in.IDs) == 0 || len(in.IDs) > 5000 {
		writeErr(w, http.StatusBadRequest, "invalid", "")
		return
	}
	mailbox := sessionFrom(r.Context()).Mailbox
	// Карточки внешних книг сначала удаляются на сервере; не удалённые там
	// (книга только для чтения, карточку изменили) остаются и в кэше.
	ids := in.IDs[:0]
	var remoteErr error
	for _, id := range in.IDs {
		meta, err := s.store.ContactRemote(r.Context(), mailbox, id)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			s.storeError(w, err)
			return
		}
		if meta.Book > 0 {
			if err := s.deleteRemoteContact(r, meta); err != nil {
				remoteErr = err
				continue
			}
		}
		ids = append(ids, id)
	}
	n, err := s.store.DeleteContacts(r.Context(), mailbox, ids)
	if err != nil {
		s.storeError(w, err)
		return
	}
	if remoteErr != nil && n == 0 {
		s.remoteError(w, remoteErr)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": n, "failed": len(in.IDs) - n})
}

func (s *Server) handleSuggest(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if len([]rune(strings.TrimSpace(q))) < 1 || len(q) > 100 {
		writeJSON(w, http.StatusOK, map[string]any{"suggestions": []any{}})
		return
	}
	list, err := s.store.Suggest(r.Context(), sessionFrom(r.Context()).Mailbox, q, 8)
	if err != nil {
		s.storeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"suggestions": list})
}

func (s *Server) handleCollected(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.CollectedList(r.Context(), sessionFrom(r.Context()).Mailbox, r.URL.Query().Get("q"), 500)
	if err != nil {
		s.storeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"collected": list})
}

func (s *Server) handleDeleteCollected(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Emails []string `json:"emails"`
	}
	if err := readJSON(r, &in); err != nil || len(in.Emails) == 0 || len(in.Emails) > 5000 {
		writeErr(w, http.StatusBadRequest, "invalid", "")
		return
	}
	if err := s.store.DeleteCollected(r.Context(), sessionFrom(r.Context()).Mailbox, in.Emails); err != nil {
		s.storeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

const maxImportFile = 5 << 20

func (s *Server) handleImportContacts(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxImportFile+1<<16)
	f, _, err := r.FormFile("file")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "contacts.import_file", "")
		return
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxImportFile+1))
	if err != nil || len(data) > maxImportFile {
		writeErr(w, http.StatusRequestEntityTooLarge, "contacts.import_too_large", "")
		return
	}
	list, err := contacts.Parse(data)
	if err != nil && len(list) == 0 {
		writeErr(w, http.StatusBadRequest, "contacts.import_format", err.Error())
		return
	}
	// Некорректные записи пропускаем, а не роняем весь импорт.
	valid := list[:0]
	invalid := 0
	for _, c := range list {
		if validateContact(&c) == "" {
			valid = append(valid, c)
		} else {
			invalid++
		}
	}
	mailbox := sessionFrom(r.Context()).Mailbox
	added, skipped, err := s.store.ImportContacts(r.Context(), mailbox, valid)
	if err != nil && !errors.Is(err, store.ErrTooMany) {
		s.storeError(w, err)
		return
	}
	s.audit.Info("contacts imported", "mailbox", mailbox, "added", added, "skipped", skipped+invalid)
	writeJSON(w, http.StatusOK, map[string]any{"added": added, "skipped": skipped + invalid, "limit_reached": errors.Is(err, store.ErrTooMany)})
}

func (s *Server) handleExportContacts(w http.ResponseWriter, r *http.Request) {
	group, _ := strconv.ParseInt(r.URL.Query().Get("group"), 10, 64)
	list, _, err := s.store.Contacts(r.Context(), sessionFrom(r.Context()).Mailbox, "", group, store.MaxContacts, 0)
	if err != nil {
		s.storeError(w, err)
		return
	}
	var buf bytes.Buffer
	name := "contacts-" + time.Now().Format("2006-01-02")
	ct := "text/vcard; charset=utf-8"
	if r.URL.Query().Get("format") == "csv" {
		err, ct, name = contacts.WriteCSV(&buf, list), "text/csv; charset=utf-8", name+".csv"
	} else {
		err, name = contacts.WriteVCards(&buf, list), name+".vcf"
	}
	if err != nil {
		s.log.Error("contacts export", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal", "")
		return
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	w.Write(buf.Bytes())
}

func (s *Server) handleGroups(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.Groups(r.Context(), sessionFrom(r.Context()).Mailbox)
	if err != nil {
		s.storeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"groups": list})
}

func (s *Server) handleSaveGroup(w http.ResponseWriter, r *http.Request) {
	var in store.Group
	if err := readJSON(r, &in); err != nil || strings.TrimSpace(in.Name) == "" || len(in.Name) > 100 {
		writeErr(w, http.StatusBadRequest, "invalid", "")
		return
	}
	g, err := s.store.SaveGroup(r.Context(), sessionFrom(r.Context()).Mailbox, in)
	if err != nil {
		s.storeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, g)
}

func (s *Server) handleDeleteGroup(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ID int64 `json:"id"`
	}
	if err := readJSON(r, &in); err != nil || in.ID == 0 {
		writeErr(w, http.StatusBadRequest, "invalid", "")
		return
	}
	if err := s.store.DeleteGroup(r.Context(), sessionFrom(r.Context()).Mailbox, in.ID); err != nil {
		s.storeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// collectRecipients запоминает получателей отправленного письма для подсказок.
func (s *Server) collectRecipients(r *http.Request, d *mail.Draft) {
	var list []store.Collected
	for _, group := range [][]mail.Address{d.To, d.Cc, d.Bcc} {
		for _, a := range group {
			list = append(list, store.Collected{Email: a.Email, Name: a.Name})
		}
	}
	if err := s.store.Collect(r.Context(), sessionFrom(r.Context()).Mailbox, list); err != nil {
		s.log.Warn("collect recipients", "err", fmt.Sprint(err))
	}
}
