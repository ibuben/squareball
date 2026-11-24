package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/ibuben/squareball/ext"
	"github.com/ibuben/squareball/internal/calendar"
	"github.com/ibuben/squareball/internal/mail"
)

// Календарь. Ядро ничего не хранит: события запрашиваются у источника
// (CalDAV) за диапазон, который показывает интерфейс. Список календарей
// ящика кэшируется в памяти на calListTTL, чтобы не искать их на каждом
// запросе. Календари доступны только из пресетов администратора.

const (
	calListTTL    = 5 * time.Minute
	calTimeout    = 30 * time.Second
	calMaxResults = 5000
)

// SetCalendarSource — ext.Core.
func (s *Server) SetCalendarSource(cs ext.CalendarSource) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calendarSource = cs
}

func (s *Server) calSrc() ext.CalendarSource {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.calendarSource
}

// userCal — календарь ящика с учётными данными.
type userCal struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Preset   string `json:"preset"`
	ReadOnly bool   `json:"readonly"`
	URL      string `json:"-"`
	acc      ext.ContactAccount
}

type calListEntry struct {
	at   time.Time
	cals []ext.RemoteCalendar
}

type calLists struct {
	mu sync.Mutex
	m  map[string]calListEntry
}

func (c *calLists) get(key string) ([]ext.RemoteCalendar, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[key]
	if !ok || time.Since(e.at) > calListTTL {
		return nil, false
	}
	return e.cals, true
}

func (c *calLists) put(key string, cals []ext.RemoteCalendar) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]calListEntry{}
	}
	for k, e := range c.m { // без фонового чистильщика: убираем старое при записи
		if time.Since(e.at) > calListTTL {
			delete(c.m, k)
		}
	}
	c.m[key] = calListEntry{at: time.Now(), cals: cals}
}

func calID(preset, u string) string {
	h := sha256.Sum256([]byte(preset + "\x00" + u))
	return hex.EncodeToString(h[:6])
}

// presetAccount — учётка пресета с паролем для этой сессии.
func (s *Server) presetAccount(r *http.Request, acc ext.ContactAccount) (ext.ContactAccount, error) {
	acc.Trusted = true
	if acc.Password == ext.PasswordSession {
		pw, err := s.davSessionPassword(sessionFrom(r.Context()), acc)
		if err != nil {
			return acc, err
		}
		acc.Password = pw
	}
	return acc, nil
}

// calendars — календари ящика по всем пресетам; ошибки — по id пресета.
func (s *Server) calendars(r *http.Request, src ext.CalendarSource, fresh bool) ([]userCal, map[string]string) {
	mailbox := sessionFrom(r.Context()).Mailbox
	var out []userCal
	errs := map[string]string{}
	for _, p := range src.CalendarPresets(mailbox) {
		acc, err := s.presetAccount(r, p.Account)
		if err != nil {
			errs[p.ID] = davErrCode(err)
			continue
		}
		key := mailbox + "\x00" + p.ID
		list, ok := s.calLists.get(key)
		if !ok || fresh {
			ctx, cancel := context.WithTimeout(r.Context(), calTimeout)
			list, err = src.DiscoverCalendars(ctx, acc)
			cancel()
			if err != nil {
				s.log.Warn("calendar discover", "mailbox", mailbox, "preset", p.ID, "err", err)
				errs[p.ID] = davErrCode(err)
				continue
			}
			s.calLists.put(key, list)
		}
		for _, c := range list {
			out = append(out, userCal{ID: calID(p.ID, c.URL), Name: c.Name, Preset: p.ID, ReadOnly: p.ReadOnly || c.ReadOnly, URL: c.URL, acc: acc})
		}
	}
	return out, errs
}

func (s *Server) calendarByID(r *http.Request, src ext.CalendarSource, id string) (userCal, bool) {
	cals, _ := s.calendars(r, src, false)
	for _, c := range cals {
		if c.ID == id {
			return c, true
		}
	}
	return userCal{}, false
}

func (s *Server) calendarRoutes() {
	m := s.mux
	m.HandleFunc("GET /api/v1/calendars", s.authed(s.handleCalendars))
	m.HandleFunc("GET /api/v1/calendar/events", s.authed(s.handleCalendarEvents))
	m.HandleFunc("GET /api/v1/calendar/event", s.authed(s.handleCalendarEvent))
	m.HandleFunc("POST /api/v1/calendar/events", s.authed(s.handleSaveCalendarEvent))
	m.HandleFunc("POST /api/v1/calendar/events/delete", s.authed(s.handleDeleteCalendarEvent))
	m.HandleFunc("POST /api/v1/messages/invite-save", s.authed(s.handleInviteSave))
}

func (s *Server) handleCalendars(w http.ResponseWriter, r *http.Request) {
	src := s.calSrc()
	if src == nil {
		writeJSON(w, http.StatusOK, map[string]any{"enabled": false, "calendars": []userCal{}, "errors": map[string]string{}})
		return
	}
	cals, errs := s.calendars(r, src, r.URL.Query().Get("refresh") == "1")
	if cals == nil {
		cals = []userCal{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"enabled": true, "calendars": cals, "errors": errs})
}

type calOccurrence struct {
	Cal  string `json:"cal"`
	Href string `json:"href"`
	ETag string `json:"etag"`
	calendar.Occurrence
	ReadOnly bool `json:"readonly,omitempty"`
}

func (s *Server) handleCalendarEvents(w http.ResponseWriter, r *http.Request) {
	src := s.calSrc()
	if src == nil {
		writeErr(w, http.StatusNotFound, "not_found", "")
		return
	}
	q := r.URL.Query()
	from, err1 := time.Parse(time.RFC3339, q.Get("from"))
	to, err2 := time.Parse(time.RFC3339, q.Get("to"))
	loc := time.UTC
	if tz := q.Get("tz"); tz != "" && tz != "Local" {
		if l, err := time.LoadLocation(tz); err == nil {
			loc = l
		}
	}
	if err1 != nil || err2 != nil || !to.After(from) || to.Sub(from) > calendar.MaxRange {
		writeErr(w, http.StatusBadRequest, "invalid", "")
		return
	}
	only := map[string]bool{}
	for _, id := range strings.Split(q.Get("cals"), ",") {
		if id != "" {
			only[id] = true
		}
	}
	cals, errs := s.calendars(r, src, false)
	out := []calOccurrence{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, c := range cals {
		if len(only) > 0 && !only[c.ID] {
			continue
		}
		wg.Add(1)
		go func(c userCal) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(r.Context(), calTimeout)
			defer cancel()
			objs, err := src.Query(ctx, c.acc, c.URL, from, to)
			var part []calOccurrence
			for _, o := range objs {
				occ, err := calendar.Expand(o.ICS, from, to, loc)
				if err != nil {
					continue
				}
				for _, e := range occ {
					part = append(part, calOccurrence{Cal: c.ID, Href: o.Href, ETag: o.ETag, Occurrence: e, ReadOnly: c.ReadOnly})
				}
			}
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				s.log.Warn("calendar query", "cal", c.ID, "err", err)
				errs[c.ID] = davErrCode(err)
				return
			}
			out = append(out, part...)
		}(c)
	}
	wg.Wait()
	truncated := len(out) > calMaxResults
	if truncated {
		out = out[:calMaxResults]
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": out, "errors": errs, "truncated": truncated})
}

func (s *Server) handleCalendarEvent(w http.ResponseWriter, r *http.Request) {
	src := s.calSrc()
	q := r.URL.Query()
	if src == nil {
		writeErr(w, http.StatusNotFound, "not_found", "")
		return
	}
	c, ok := s.calendarByID(r, src, q.Get("cal"))
	if !ok || q.Get("href") == "" {
		writeErr(w, http.StatusNotFound, "not_found", "")
		return
	}
	o, err := src.GetObject(r.Context(), c.acc, c.URL, q.Get("href"))
	if err != nil {
		s.calError(w, err)
		return
	}
	ev, err := calendar.FormEvent(o.ICS)
	if err != nil {
		writeErr(w, http.StatusUnprocessableEntity, "calendar.invalid", "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"cal": c.ID, "href": o.Href, "etag": o.ETag, "readonly": c.ReadOnly, "event": ev})
}

func (s *Server) handleSaveCalendarEvent(w http.ResponseWriter, r *http.Request) {
	src := s.calSrc()
	if src == nil {
		writeErr(w, http.StatusNotFound, "not_found", "")
		return
	}
	var in struct {
		Cal   string         `json:"cal"`
		Href  string         `json:"href"`
		ETag  string         `json:"etag"`
		Event calendar.Event `json:"event"`
	}
	if err := readJSON(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid", "")
		return
	}
	c, ok := s.calendarByID(r, src, in.Cal)
	if !ok {
		writeErr(w, http.StatusNotFound, "not_found", "")
		return
	}
	if c.ReadOnly {
		writeErr(w, http.StatusForbidden, "calendar.readonly", "")
		return
	}
	var orig []byte
	if in.Href != "" {
		o, err := src.GetObject(r.Context(), c.acc, c.URL, in.Href)
		if err != nil {
			s.calError(w, err)
			return
		}
		if in.ETag != "" && o.ETag != "" && o.ETag != in.ETag {
			writeErr(w, http.StatusConflict, "calendar.conflict", "")
			return
		}
		orig, in.ETag = o.ICS, o.ETag
	} else {
		in.Event.UID = ""
	}
	ics, err := calendar.Build(orig, in.Event, time.Now())
	if err != nil {
		writeErr(w, http.StatusBadRequest, "calendar.invalid", "")
		return
	}
	href, etag, err := src.PutObject(r.Context(), c.acc, c.URL, in.Href, in.ETag, ics)
	if err != nil {
		s.calError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "cal": c.ID, "href": href, "etag": etag})
}

func (s *Server) handleDeleteCalendarEvent(w http.ResponseWriter, r *http.Request) {
	src := s.calSrc()
	if src == nil {
		writeErr(w, http.StatusNotFound, "not_found", "")
		return
	}
	var in struct {
		Cal          string `json:"cal"`
		Href         string `json:"href"`
		ETag         string `json:"etag"`
		UID          string `json:"uid"`
		RecurrenceID string `json:"recurrence_id"` // только это вхождение
	}
	if err := readJSON(r, &in); err != nil || in.Href == "" {
		writeErr(w, http.StatusBadRequest, "invalid", "")
		return
	}
	c, ok := s.calendarByID(r, src, in.Cal)
	if !ok {
		writeErr(w, http.StatusNotFound, "not_found", "")
		return
	}
	if c.ReadOnly {
		writeErr(w, http.StatusForbidden, "calendar.readonly", "")
		return
	}
	if in.RecurrenceID == "" {
		if err := src.DeleteObject(r.Context(), c.acc, c.URL, in.Href, in.ETag); err != nil && !errors.Is(err, ext.ErrContactNotFound) {
			s.calError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	o, err := src.GetObject(r.Context(), c.acc, c.URL, in.Href)
	if err != nil {
		s.calError(w, err)
		return
	}
	ics, err := calendar.DeleteOccurrence(o.ICS, in.UID, in.RecurrenceID, time.Now())
	if err != nil {
		writeErr(w, http.StatusBadRequest, "calendar.invalid", "")
		return
	}
	if _, _, err := src.PutObject(r.Context(), c.acc, c.URL, o.Href, o.ETag, ics); err != nil {
		s.calError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// defaultCalendar — куда сохранять приглашения: первый календарь с правом записи.
func (s *Server) defaultCalendar(r *http.Request, src ext.CalendarSource) (userCal, bool) {
	cals, _ := s.calendars(r, src, false)
	for _, c := range cals {
		if !c.ReadOnly {
			return c, true
		}
	}
	return userCal{}, false
}

// handleInviteSave сохраняет встречу из письма в календарь (или убирает её,
// если встреча отменена или приглашение отклонено). Повторное сохранение
// обновляет объект с тем же UID, а не создаёт новый.
func (s *Server) handleInviteSave(w http.ResponseWriter, r *http.Request) {
	src := s.calSrc()
	if src == nil {
		writeErr(w, http.StatusNotFound, "not_found", "")
		return
	}
	var in struct {
		Folder   string `json:"folder"`
		UID      uint32 `json:"uid"`
		PartStat string `json:"partstat"`
	}
	if err := readJSON(r, &in); err != nil || in.Folder == "" || in.UID == 0 {
		writeErr(w, http.StatusBadRequest, "invalid", "")
		return
	}
	in.PartStat = strings.ToUpper(in.PartStat)
	if in.PartStat != "" && rsvpFlags[in.PartStat] == "" {
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
	if m.Invite == nil || len(m.InviteICS) == 0 || m.Invite.Method == "REPLY" || m.Invite.Method == "COUNTER" {
		writeErr(w, http.StatusConflict, "invite.no_save", "")
		return
	}
	c, ok := s.defaultCalendar(r, src)
	if !ok {
		writeErr(w, http.StatusNotFound, "calendar.none", "")
		return
	}
	sess := sessionFrom(r.Context())
	self, _ := s.inviteSelf(r, sess, m.Invite)
	ics, uid, err := calendar.ForStorage(m.InviteICS, self.Email, in.PartStat)
	if err != nil {
		writeErr(w, http.StatusUnprocessableEntity, "invite.invalid", "")
		return
	}
	existing, err := src.FindUID(r.Context(), c.acc, c.URL, uid)
	if err != nil && !errors.Is(err, ext.ErrContactNotFound) {
		s.calError(w, err)
		return
	}
	found := err == nil
	remove := m.Invite.Method == "CANCEL" || m.Invite.Status == "CANCELLED" || in.PartStat == mail.PartDeclined
	switch {
	case remove && !found:
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "saved": false})
		return
	case remove && m.Invite.RecurrenceID != "":
		ics, err = calendar.MergeInvite(existing.ICS, ics) // отменено одно вхождение: исключение со STATUS:CANCELLED
		if err == nil {
			_, _, err = src.PutObject(r.Context(), c.acc, c.URL, existing.Href, existing.ETag, ics)
		}
	case remove:
		err = src.DeleteObject(r.Context(), c.acc, c.URL, existing.Href, existing.ETag)
	case found:
		if ics, err = calendar.MergeInvite(existing.ICS, ics); err == nil {
			_, _, err = src.PutObject(r.Context(), c.acc, c.URL, existing.Href, existing.ETag, ics)
		}
	default:
		_, _, err = src.PutObject(r.Context(), c.acc, c.URL, "", "", ics)
	}
	if err != nil {
		s.calError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "saved": !remove, "cal": c.ID})
}

// calError отвечает на ошибку календарного сервера.
func (s *Server) calError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ext.ErrContactConflict):
		writeErr(w, http.StatusConflict, "calendar.conflict", "")
	case errors.Is(err, ext.ErrContactReadOnly):
		writeErr(w, http.StatusForbidden, "calendar.readonly", "")
	case errors.Is(err, ext.ErrContactNotFound):
		writeErr(w, http.StatusNotFound, "not_found", "")
	default:
		s.log.Warn("calendar remote", "err", err)
		writeErr(w, http.StatusBadGateway, "calendar.remote_"+davErrCode(err), "")
	}
}
