package api

import (
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/ibuben/squareball/internal/mail"
)

// prefValues — допустимые настройки ящика и их значения ("" — по умолчанию).
var prefValues = map[string][]string{
	"archive_structure": {mail.ArchiveFlat, mail.ArchiveYear, mail.ArchiveMonth},
}

func (s *Server) prefRoutes() {
	m := s.mux
	m.HandleFunc("GET /api/v1/prefs", s.authed(s.handleGetPrefs))
	m.HandleFunc("PUT /api/v1/prefs", s.authed(s.handlePutPrefs))
	m.HandleFunc("POST /api/v1/messages/archive", s.authed(s.handleArchive))
	m.HandleFunc("GET /api/v1/attachments.zip", s.authed(s.handleAttachmentsZip))
}

func (s *Server) prefs(r *http.Request) map[string]string {
	out := map[string]string{}
	for k := range prefValues {
		out[k] = ""
	}
	if s.store == nil {
		return out
	}
	saved, err := s.store.Prefs(r.Context(), sessionFrom(r.Context()).Mailbox)
	if err != nil {
		s.log.Error("prefs", "err", err)
		return out
	}
	for k, v := range saved {
		if _, ok := prefValues[k]; ok {
			out[k] = v
		}
	}
	return out
}

func (s *Server) handleGetPrefs(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"prefs": s.prefs(r)})
}

func (s *Server) handlePutPrefs(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		writeErr(w, http.StatusNotFound, "not_found", "")
		return
	}
	var in map[string]string
	if err := readJSON(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid", "")
		return
	}
	for k, v := range in {
		allowed, ok := prefValues[k]
		valid := false
		for _, a := range allowed {
			valid = valid || a == v
		}
		if !ok || !valid {
			writeErr(w, http.StatusBadRequest, "invalid", k)
			return
		}
	}
	if err := s.store.SetPrefs(r.Context(), sessionFrom(r.Context()).Mailbox, in); err != nil {
		s.storeError(w, err)
		return
	}
	s.handleGetPrefs(w, r)
}

// handleArchive переносит письма в «Архив» с учётом структуры (по годам, месяцам).
func (s *Server) handleArchive(w http.ResponseWriter, r *http.Request) {
	in, ok := s.readBatch(w, r)
	if !ok {
		return
	}
	structure := s.prefs(r)["archive_structure"]
	moved := 0
	if s.withIMAP(w, r, func(c *imapclient.Client) error {
		fs, err := s.folders(c)
		if err != nil {
			return err
		}
		moved, err = mail.Archive(c, fs, in.Folder, in.UIDs, structure)
		return err
	}) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "moved": moved})
	}
}

// handleAttachmentsZip отдаёт все вложения письма одним архивом.
func (s *Server) handleAttachmentsZip(w http.ResponseWriter, r *http.Request) {
	folder, uid, ok := uidParam(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "invalid", "")
		return
	}
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if name == "" {
		name = "attachments-" + strconv.FormatUint(uint64(uid), 10)
	}
	name = mail.SafeFileName(name) + ".zip"
	n := 0
	if !s.withIMAP(w, r, func(c *imapclient.Client) (err error) {
		n, err = mail.WriteAttachmentsZip(c, folder, uid, w, func() {
			h := w.Header()
			h.Set("Content-Type", "application/zip")
			h.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
			h.Set("Cache-Control", "private, no-store")
			h.Set("Last-Modified", time.Now().UTC().Format(http.TimeFormat))
		})
		return err
	}) {
		return
	}
	if n == 0 {
		writeErr(w, http.StatusNotFound, "mail.not_found", "no attachments")
	}
}
