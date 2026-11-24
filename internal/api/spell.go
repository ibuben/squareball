package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/ibuben/squareball/internal/spell"
	"github.com/ibuben/squareball/internal/store"
)

// Проверка орфографии: движок и словари отдаются в /me (features.spellcheck),
// выбор словаря хранится в настройке ящика "spell": "" — все словари сразу,
// имя словаря или "off".

func (s *Server) initSpell() {
	cfg := s.cfg.Spellcheck
	s.spellEngine = cfg.Engine
	if cfg.Engine != "hunspell" {
		return
	}
	c, err := spell.New(cfg.Hunspell.Command, cfg.Hunspell.Dictionaries, cfg.Hunspell.Timeout)
	if err != nil {
		s.log.Error("spellcheck", "err", err)
	}
	if c == nil {
		// Нет ни одного рабочего словаря — пусть проверяет хотя бы браузер.
		s.spellEngine = "browser"
		return
	}
	s.spell = c
}

func (s *Server) spellFeature() map[string]any {
	dicts := []string{}
	if s.spell != nil {
		dicts = s.spell.Dictionaries()
	}
	return map[string]any{"engine": s.spellEngine, "dictionaries": dicts}
}

// spellPrefOK — допустимое значение настройки "spell".
func (s *Server) spellPrefOK(v string) bool {
	if v == "" || v == "off" {
		return true
	}
	return s.spell != nil && contains(s.spell.Dictionaries(), v)
}

func (s *Server) spellRoutes() {
	m := s.mux
	m.HandleFunc("POST /api/v1/spell", s.authed(s.handleSpell))
	m.HandleFunc("GET /api/v1/spell/words", s.authed(s.handleSpellWords))
	m.HandleFunc("POST /api/v1/spell/words", s.authed(s.handleAddSpellWord))
	m.HandleFunc("POST /api/v1/spell/words/delete", s.authed(s.handleDeleteSpellWord))
}

func (s *Server) handleSpell(w http.ResponseWriter, r *http.Request) {
	if s.spell == nil {
		writeErr(w, http.StatusNotFound, "not_found", "")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 128<<10)
	var in struct {
		Dictionary string   `json:"dictionary"` // "" — все словари
		Words      []string `json:"words"`
	}
	if err := readJSON(r, &in); err != nil || len(in.Words) > spell.MaxWords {
		writeErr(w, http.StatusBadRequest, "invalid", "")
		return
	}
	var dicts []string
	if in.Dictionary != "" {
		dicts = []string{in.Dictionary}
	}
	// Слова из личного словаря не проверяем (без учёта регистра).
	own := map[string]bool{}
	if list, err := s.store.SpellWords(r.Context(), sessionFrom(r.Context()).Mailbox); err == nil {
		for _, x := range list {
			own[strings.ToLower(x)] = true
		}
	}
	words := in.Words[:0]
	for _, x := range in.Words {
		if !own[strings.ToLower(x)] {
			words = append(words, x)
		}
	}
	res, err := s.spell.Check(r.Context(), dicts, words)
	if errors.Is(err, spell.ErrDictionary) {
		writeErr(w, http.StatusBadRequest, "spell.dictionary", "")
		return
	}
	if err != nil {
		s.log.Error("spellcheck", "err", err)
		writeErr(w, http.StatusBadGateway, "spell.unavailable", "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"misspelled": res})
}

func (s *Server) handleSpellWords(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.SpellWords(r.Context(), sessionFrom(r.Context()).Mailbox)
	if err != nil {
		s.storeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"words": list})
}

func (s *Server) readSpellWord(w http.ResponseWriter, r *http.Request) (string, bool) {
	var in struct {
		Word string `json:"word"`
	}
	if err := readJSON(r, &in); err != nil || !spell.IsWord(in.Word) {
		writeErr(w, http.StatusBadRequest, "spell.word", "")
		return "", false
	}
	return in.Word, true
}

func (s *Server) handleAddSpellWord(w http.ResponseWriter, r *http.Request) {
	word, ok := s.readSpellWord(w, r)
	if !ok {
		return
	}
	err := s.store.AddSpellWord(r.Context(), sessionFrom(r.Context()).Mailbox, word)
	if errors.Is(err, store.ErrTooManyWords) {
		writeErr(w, http.StatusConflict, "spell.limit", "")
		return
	}
	if err != nil {
		s.storeError(w, err)
		return
	}
	s.handleSpellWords(w, r)
}

func (s *Server) handleDeleteSpellWord(w http.ResponseWriter, r *http.Request) {
	word, ok := s.readSpellWord(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteSpellWord(r.Context(), sessionFrom(r.Context()).Mailbox, word); err != nil {
		s.storeError(w, err)
		return
	}
	s.handleSpellWords(w, r)
}
