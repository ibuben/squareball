package api

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/ibuben/squareball/ext"
	"github.com/ibuben/squareball/internal/contacts"
	"github.com/ibuben/squareball/internal/session"
	"github.com/ibuben/squareball/internal/store"
)

// Внешние адресные книги. Протокол (CardDAV) — в адаптере, ядро ведёт учётки,
// кэш карточек в SQLite и синхронизацию. Изменения контактов внешней книги
// сначала записываются на сервер (с If-Match), потом в кэш.
//
// Пароль учётки, добавленной пользователем, хранится зашифрованным ключом,
// выведенным из секрета сессий: иначе синхронизация в следующих сессиях
// невозможна. Пресеты администратора с паролем ящика (ext.PasswordSession)
// пароль не хранят: при обычном входе берётся пароль сессии, при
// impersonation (SSO из панели) для доверенных пресетов на loopback —
// пароль мастер-учётки IMAP (локальный Radicale его принимает).

const (
	bookStale       = 15 * time.Minute // автоматическая синхронизация не чаще
	bookSyncTimeout = 90 * time.Second
	bookGetBatch    = 100
)

type davLocks struct {
	mu sync.Mutex
	m  map[int64]*sync.Mutex
}

func (l *davLocks) get(id int64) *sync.Mutex {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.m == nil {
		l.m = map[int64]*sync.Mutex{}
	}
	if l.m[id] == nil {
		l.m[id] = &sync.Mutex{}
	}
	return l.m[id]
}

// SetContactSource — ext.Core.
func (s *Server) SetContactSource(cs ext.ContactSource) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.contactSource = cs
}

func (s *Server) contactSrc() ext.ContactSource {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.contactSource
}

// --- шифрование пароля учётки -----------------------------------------------------

func (s *Server) davAEAD() (cipher.AEAD, error) {
	k := sha256.Sum256(append([]byte("webmail contact accounts\x00"), s.sessionSecret...))
	b, err := aes.NewCipher(k[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(b)
}

func (s *Server) sealSecret(mailbox, pw string) ([]byte, error) {
	a, err := s.davAEAD()
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, a.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return a.Seal(nonce, nonce, []byte(pw), []byte(mailbox)), nil
}

func (s *Server) openSecret(mailbox string, sealed []byte) (string, error) {
	if len(sealed) == 0 {
		return "", nil
	}
	a, err := s.davAEAD()
	if err != nil {
		return "", err
	}
	ns := a.NonceSize()
	if len(sealed) < ns {
		return "", errors.New("corrupt secret")
	}
	pt, err := a.Open(nil, sealed[:ns], sealed[ns:], []byte(mailbox))
	return string(pt), err
}

// --- учётки и пресеты ------------------------------------------------------------

var errNoSessionPassword = errors.New("session has no mailbox password")

// ensurePresets приводит учётки-пресеты ящика к конфигу адаптера.
func (s *Server) ensurePresets(ctx context.Context, src ext.ContactSource, mailbox string) error {
	presets := src.Presets(mailbox)
	accounts, err := s.store.DavAccounts(ctx, mailbox)
	if err != nil {
		return err
	}
	have := map[string]store.DavAccount{}
	for _, a := range accounts {
		if a.Preset != "" {
			have[a.Preset] = a
		}
	}
	want := map[string]bool{}
	for _, p := range presets {
		want[p.ID] = true
		a, ok := have[p.ID]
		switch {
		case !ok:
			if _, err := s.store.AddDavAccount(ctx, mailbox, store.DavAccount{Preset: p.ID, Name: p.Name, URL: p.Account.URL, Username: p.Account.Username}); err != nil {
				return err
			}
		case a.Name != p.Name || a.URL != p.Account.URL || a.Username != p.Account.Username:
			if err := s.store.UpdateDavPreset(ctx, a.ID, p.Name, p.Account.URL, p.Account.Username); err != nil {
				return err
			}
		}
	}
	for id, a := range have {
		if !want[id] {
			if err := s.store.DeleteDavAccount(ctx, mailbox, a.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

// davCreds собирает учётные данные учётки для запроса к серверу.
func (s *Server) davCreds(r *http.Request, src ext.ContactSource, a store.DavAccount) (acc ext.ContactAccount, readonly bool, err error) {
	sess := sessionFrom(r.Context())
	if a.Preset == "" {
		pw, err := s.openSecret(sess.Mailbox, a.Secret)
		return ext.ContactAccount{URL: a.URL, Username: a.Username, Password: pw}, false, err
	}
	for _, p := range src.Presets(sess.Mailbox) {
		if p.ID != a.Preset {
			continue
		}
		acc = p.Account
		acc.Trusted = true
		if acc.Password == ext.PasswordSession {
			if acc.Password, err = s.davSessionPassword(sess, acc); err != nil {
				return acc, p.ReadOnly, err
			}
		}
		return acc, p.ReadOnly, nil
	}
	return acc, false, ext.ErrContactNotFound
}

// davSessionPassword — пароль ящика из сессии или мастер-пароль для
// локального доверенного CardDAV при входе из панели (impersonation).
func (s *Server) davSessionPassword(sess *session.Session, acc ext.ContactAccount) (string, error) {
	if sess.AuthMode == ext.AuthImpersonation {
		if s.masterPw == "" || !acc.Trusted || !davLoopback(acc.URL) {
			return "", errNoSessionPassword
		}
		return s.masterPw, nil
	}
	return sess.Password(s.sessions)
}

func davLoopback(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return false
	}
	switch strings.ToLower(u.Hostname()) {
	case "127.0.0.1", "localhost", "::1":
		return true
	default:
		return false
	}
}

// davErrCode — код ошибки для интерфейса.
func davErrCode(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ext.ErrContactAuth), errors.Is(err, errNoSessionPassword):
		return "auth"
	case errors.Is(err, ext.ErrContactForbidden):
		return "forbidden"
	case errors.Is(err, ext.ErrContactNotFound):
		return "not_found"
	case errors.Is(err, store.ErrTooMany):
		return "too_many"
	default:
		return "unavailable"
	}
}

// --- синхронизация ---------------------------------------------------------------

// syncAccount обновляет список книг учётки и карточки в каждой книге.
func (s *Server) syncAccount(ctx context.Context, src ext.ContactSource, mailbox string, a store.DavAccount, acc ext.ContactAccount, presetRO bool) error {
	lock := s.davLocks.get(a.ID)
	lock.Lock()
	defer lock.Unlock()
	found, err := src.Discover(ctx, acc)
	if err != nil {
		_ = s.store.SetDavAccountState(context.WithoutCancel(ctx), a.ID, davErrCode(err))
		s.log.Warn("contacts sync", "mailbox", mailbox, "account", a.ID, "err", err)
		return err
	}
	info := make([]store.RemoteBookInfo, 0, len(found))
	for _, b := range found {
		info = append(info, store.RemoteBookInfo{URL: b.URL, Name: b.Name, ReadOnly: b.ReadOnly || presetRO})
	}
	if err := s.store.SetAccountBooks(ctx, mailbox, a.ID, info); err != nil {
		return err
	}
	books, err := s.store.Books(ctx, mailbox)
	if err != nil {
		return err
	}
	var firstErr error
	for _, b := range books {
		if b.Account != a.ID {
			continue
		}
		err := s.syncBook(ctx, src, mailbox, b, acc)
		_ = s.store.SetBookState(context.WithoutCancel(ctx), b.ID, davErrCode(err))
		if err != nil {
			s.log.Warn("contacts sync", "mailbox", mailbox, "book", b.ID, "err", err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	_ = s.store.SetDavAccountState(context.WithoutCancel(ctx), a.ID, "")
	return firstErr
}

func (s *Server) syncBook(ctx context.Context, src ext.ContactSource, mailbox string, b store.Book, acc ext.ContactAccount) error {
	remote, err := src.List(ctx, acc, b.URL)
	if err != nil {
		return err
	}
	local, err := s.store.BookCards(ctx, b.ID)
	if err != nil {
		return err
	}
	var need, gone []string
	for href, etag := range remote {
		if l, ok := local[href]; !ok || etag == "" || l.ETag != etag {
			need = append(need, href)
		}
	}
	for href := range local {
		if _, ok := remote[href]; !ok {
			gone = append(gone, href)
		}
	}
	if err := s.store.DeleteCards(ctx, b.ID, gone); err != nil {
		return err
	}
	for i := 0; i < len(need); i += bookGetBatch {
		cards, err := src.Get(ctx, acc, b.URL, need[i:min(i+bookGetBatch, len(need))])
		if err != nil {
			return err
		}
		for _, card := range cards {
			if contacts.IsGroupCard(card.VCard) {
				continue // группы CardDAV пока не показываем
			}
			c, err := contacts.FromVCard(card.VCard)
			if err != nil {
				s.log.Debug("contacts sync: bad vcard", "book", b.ID, "href", card.Href, "err", err)
				continue
			}
			if err := s.store.UpsertCard(ctx, mailbox, b.ID, card.Href, card.ETag, card.VCard, c); err != nil {
				return err
			}
		}
	}
	return nil
}

// syncMailbox синхронизирует учётки ящика: все (force) или только устаревшие.
func (s *Server) syncMailbox(r *http.Request, src ext.ContactSource, only int64, force bool) error {
	mailbox := sessionFrom(r.Context()).Mailbox
	ctx, cancel := context.WithTimeout(r.Context(), bookSyncTimeout)
	defer cancel()
	if err := s.ensurePresets(ctx, src, mailbox); err != nil {
		return err
	}
	accounts, err := s.store.DavAccounts(ctx, mailbox)
	if err != nil {
		return err
	}
	for _, a := range accounts {
		if (only != 0 && a.ID != only) || (!force && time.Since(a.Synced) < bookStale) {
			continue
		}
		acc, ro, err := s.davCreds(r, src, a)
		if err != nil {
			_ = s.store.SetDavAccountState(ctx, a.ID, davErrCode(err))
			continue
		}
		_ = s.syncAccount(ctx, src, mailbox, a, acc, ro) // ошибка сохранена в учётке/книге
	}
	return nil
}

// --- HTTP ------------------------------------------------------------------------

func (s *Server) bookRoutes() {
	m := s.mux
	m.HandleFunc("GET /api/v1/books", s.authed(s.handleBooks))
	m.HandleFunc("POST /api/v1/books/sync", s.authed(s.handleSyncBooks))
	m.HandleFunc("POST /api/v1/books/accounts", s.authed(s.handleAddDavAccount))
	m.HandleFunc("POST /api/v1/books/accounts/delete", s.authed(s.handleDeleteDavAccount))
}

func (s *Server) booksBody(r *http.Request, src ext.ContactSource) (map[string]any, error) {
	mailbox := sessionFrom(r.Context()).Mailbox
	body := map[string]any{"enabled": src != nil, "allow_user_accounts": false, "accounts": []store.DavAccount{}, "books": []store.Book{}}
	if src == nil {
		return body, nil
	}
	if err := s.ensurePresets(r.Context(), src, mailbox); err != nil {
		return nil, err
	}
	accounts, err := s.store.DavAccounts(r.Context(), mailbox)
	if err != nil {
		return nil, err
	}
	books, err := s.store.Books(r.Context(), mailbox)
	if err != nil {
		return nil, err
	}
	stale := false
	for _, a := range accounts {
		stale = stale || time.Since(a.Synced) >= bookStale
	}
	body["allow_user_accounts"] = src.AllowUserAccounts()
	body["accounts"], body["books"], body["stale"] = accounts, books, stale
	return body, nil
}

func (s *Server) handleBooks(w http.ResponseWriter, r *http.Request) {
	body, err := s.booksBody(r, s.contactSrc())
	if err != nil {
		s.storeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, body)
}

func (s *Server) handleSyncBooks(w http.ResponseWriter, r *http.Request) {
	src := s.contactSrc()
	if src == nil {
		writeErr(w, http.StatusNotFound, "not_found", "")
		return
	}
	var in struct {
		Account int64 `json:"account"`
		Force   bool  `json:"force"`
	}
	if err := readJSON(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid", "")
		return
	}
	if err := s.syncMailbox(r, src, in.Account, in.Force); err != nil {
		s.storeError(w, err)
		return
	}
	s.handleBooks(w, r)
}

func (s *Server) handleAddDavAccount(w http.ResponseWriter, r *http.Request) {
	src := s.contactSrc()
	if src == nil || !src.AllowUserAccounts() {
		writeErr(w, http.StatusForbidden, "forbidden", "")
		return
	}
	var in struct {
		Name     string `json:"name"`
		URL      string `json:"url"`
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := readJSON(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid", "")
		return
	}
	in.Name, in.URL, in.Username = strings.TrimSpace(in.Name), strings.TrimSpace(in.URL), strings.TrimSpace(in.Username)
	u, err := url.Parse(in.URL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || len(in.URL) > 1024 {
		writeErr(w, http.StatusBadRequest, "books.url", "")
		return
	}
	if in.Name == "" {
		in.Name = u.Hostname()
	}
	if utf8.RuneCountInString(in.Name) > 100 || len(in.Username) > 256 || len(in.Password) > 1024 {
		writeErr(w, http.StatusBadRequest, "invalid", "")
		return
	}
	mailbox := sessionFrom(r.Context()).Mailbox
	acc := ext.ContactAccount{URL: in.URL, Username: in.Username, Password: in.Password}
	// Сначала проверяем, что сервер отвечает и пускает, — не сохраняем нерабочую учётку.
	ctx, cancel := context.WithTimeout(r.Context(), bookSyncTimeout)
	defer cancel()
	found, err := src.Discover(ctx, acc)
	if err == nil && len(found) == 0 {
		err = ext.ErrContactNotFound
	}
	if err != nil {
		s.log.Info("contacts account", "mailbox", mailbox, "err", err)
		status := http.StatusBadGateway
		if code := davErrCode(err); code == "auth" || code == "forbidden" || code == "not_found" {
			status = http.StatusUnprocessableEntity // ошибка во введённых данных, а не сбой сервера
		}
		writeErr(w, status, "books."+davErrCode(err), "")
		return
	}
	sealed, err := s.sealSecret(mailbox, in.Password)
	if err != nil {
		s.storeError(w, err)
		return
	}
	a, err := s.store.AddDavAccount(ctx, mailbox, store.DavAccount{Name: in.Name, URL: in.URL, Username: in.Username, Secret: sealed})
	if errors.Is(err, store.ErrTooMany) {
		writeErr(w, http.StatusConflict, "books.limit", "")
		return
	}
	if err != nil {
		s.storeError(w, err)
		return
	}
	s.audit.Info("contacts account added", "mailbox", mailbox, "host", u.Host)
	_ = s.syncAccount(ctx, src, mailbox, a, acc, false)
	s.handleBooks(w, r)
}

func (s *Server) handleDeleteDavAccount(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ID int64 `json:"id"`
	}
	if err := readJSON(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid", "")
		return
	}
	mailbox := sessionFrom(r.Context()).Mailbox
	a, err := s.store.DavAccount(r.Context(), mailbox, in.ID)
	if err != nil {
		s.storeError(w, err)
		return
	}
	if a.Preset != "" {
		writeErr(w, http.StatusForbidden, "books.preset", "") // пресеты убирает администратор
		return
	}
	lock := s.davLocks.get(a.ID)
	lock.Lock()
	err = s.store.DeleteDavAccount(r.Context(), mailbox, a.ID)
	lock.Unlock()
	if err != nil {
		s.storeError(w, err)
		return
	}
	s.audit.Info("contacts account deleted", "mailbox", mailbox, "account", a.ID)
	s.handleBooks(w, r)
}

// --- запись контактов во внешние книги ------------------------------------------------

// remoteTarget — книга, учётка и данные для записи карточки.
func (s *Server) remoteTarget(r *http.Request, book int64) (ext.ContactSource, store.Book, ext.ContactAccount, error) {
	src := s.contactSrc()
	if src == nil {
		return nil, store.Book{}, ext.ContactAccount{}, ext.ErrContactNotFound
	}
	mailbox := sessionFrom(r.Context()).Mailbox
	b, err := s.store.Book(r.Context(), mailbox, book)
	if err != nil {
		return nil, b, ext.ContactAccount{}, err
	}
	if b.ReadOnly {
		return nil, b, ext.ContactAccount{}, ext.ErrContactReadOnly
	}
	a, err := s.store.DavAccount(r.Context(), mailbox, b.Account)
	if err != nil {
		return nil, b, ext.ContactAccount{}, err
	}
	acc, ro, err := s.davCreds(r, src, a)
	if err == nil && ro {
		err = ext.ErrContactReadOnly
	}
	return src, b, acc, err
}

// saveRemoteContact записывает контакт внешней книги на сервер, затем в кэш.
func (s *Server) saveRemoteContact(r *http.Request, in store.Contact, meta store.RemoteMeta) (store.Contact, error) {
	mailbox := sessionFrom(r.Context()).Mailbox
	in.Book = meta.Book
	// Изменились только группы (они локальные) — сервер не трогаем; так можно
	// раскладывать по группам и контакты книг только для чтения.
	if in.ID != 0 {
		if old, err := s.store.Contact(r.Context(), mailbox, in.ID); err == nil && sameCard(old, in) {
			return s.store.SaveContact(r.Context(), mailbox, in)
		}
	}
	src, b, acc, err := s.remoteTarget(r, meta.Book)
	if err != nil {
		return in, err
	}
	vc, err := contacts.MergeVCard(meta.VCard, in)
	if err != nil {
		return in, err
	}
	href, etag, err := src.Put(r.Context(), acc, b.URL, meta.Href, meta.ETag, vc)
	if err != nil {
		return in, err
	}
	saved, err := s.store.SaveContact(r.Context(), mailbox, in)
	if err != nil {
		return saved, err
	}
	return saved, s.store.SetContactRemote(r.Context(), saved.ID, href, etag, vc)
}

// sameCard — совпадают ли поля карточки (без групп и служебных полей).
func sameCard(a, b store.Contact) bool {
	store.Normalize(&a)
	store.Normalize(&b)
	a.Groups, b.Groups = nil, nil
	a.Updated = b.Updated
	return reflect.DeepEqual(a, b)
}

// deleteRemoteContact удаляет карточку на сервере (если её там уже нет — тоже успех).
func (s *Server) deleteRemoteContact(r *http.Request, meta store.RemoteMeta) error {
	src, b, acc, err := s.remoteTarget(r, meta.Book)
	if err != nil {
		return err
	}
	err = src.Delete(r.Context(), acc, b.URL, meta.Href, meta.ETag)
	if errors.Is(err, ext.ErrContactNotFound) {
		return nil
	}
	return err
}

// remoteError отвечает на ошибку записи во внешнюю книгу.
func (s *Server) remoteError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ext.ErrContactConflict):
		writeErr(w, http.StatusConflict, "contacts.conflict", "")
	case errors.Is(err, ext.ErrContactReadOnly):
		writeErr(w, http.StatusForbidden, "contacts.readonly", "")
	case errors.Is(err, store.ErrNotFound), errors.Is(err, store.ErrTooMany):
		s.storeError(w, err)
	default:
		s.log.Warn("contacts remote", "err", err)
		writeErr(w, http.StatusBadGateway, "contacts.remote_"+davErrCode(err), "")
	}
}
