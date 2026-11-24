package api

import (
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/ibuben/squareball/internal/sanitize"
	"github.com/ibuben/squareball/internal/store"
)

// maxTemplateSize — с учётом картинок в тексте (data:), поэтому не 64 КБ, как у подписи.
const maxTemplateSize = 512 << 10

func (s *Server) templateRoutes() {
	m := s.mux
	m.HandleFunc("GET /api/v1/templates", s.authed(s.handleTemplates))
	m.HandleFunc("POST /api/v1/templates", s.authed(s.handleSaveTemplate))
	m.HandleFunc("POST /api/v1/templates/delete", s.authed(s.handleDeleteTemplate))
}

func (s *Server) handleTemplates(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.Templates(r.Context(), sessionFrom(r.Context()).Mailbox)
	if err != nil {
		s.storeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"templates": list})
}

func (s *Server) handleSaveTemplate(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxTemplateSize+4<<10)
	var in store.Template
	if err := readJSON(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "template.too_large", "")
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || utf8.RuneCountInString(in.Name) > 100 {
		writeErr(w, http.StatusBadRequest, "template.name", "")
		return
	}
	if len(in.HTML) > maxTemplateSize {
		writeErr(w, http.StatusBadRequest, "template.too_large", "")
		return
	}
	in.HTML = sanitize.Email(in.HTML, func(string) string { return "" }, sanitize.KeepRemote).HTML
	saved, err := s.store.SaveTemplate(r.Context(), sessionFrom(r.Context()).Mailbox, in)
	if err != nil {
		if err == store.ErrTooMany {
			writeErr(w, http.StatusConflict, "template.limit", "")
			return
		}
		s.storeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, saved)
}

func (s *Server) handleDeleteTemplate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ID int64 `json:"id"`
	}
	if err := readJSON(r, &in); err != nil || in.ID == 0 {
		writeErr(w, http.StatusBadRequest, "invalid", "")
		return
	}
	if err := s.store.DeleteTemplate(r.Context(), sessionFrom(r.Context()).Mailbox, in.ID); err != nil {
		s.storeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
