// Package session — сессии BFF.
//
// Cookie содержит "<id>.<key>". На сервере хранится только шифротекст пароля
// IMAP; ключ шифрования = SHA-256(server secret || key из cookie). Поэтому
// содержимое хранилища без cookie пользователя не раскрывает пароли.
package session

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/ibuben/squareball/ext"
)

var ErrNotFound = errors.New("session not found")

type Session struct {
	ID        string
	Mailbox   string
	AuthMode  ext.AuthMode
	Source    string // "password" или имя источника SSO
	CSRF      string
	Created   time.Time
	LastSeen  time.Time
	RemoteIP  string
	UserAgent string

	cipherPass []byte
	keyHash    [32]byte // в хранилище только хэш ключа из cookie
	cookieKey  []byte   // заполнен только в копии, выданной на время запроса
}

// Password расшифровывает пароль пользователя (пусто для impersonation).
func (s *Session) Password(m *Manager) (string, error) {
	if len(s.cipherPass) == 0 {
		return "", nil
	}
	aead, err := m.aead(s.cookieKey)
	if err != nil {
		return "", err
	}
	ns := aead.NonceSize()
	if len(s.cipherPass) < ns {
		return "", errors.New("corrupt session")
	}
	pt, err := aead.Open(nil, s.cipherPass[:ns], s.cipherPass[ns:], []byte(s.ID))
	if err != nil {
		return "", err
	}
	return string(pt), nil
}

type Manager struct {
	secret   []byte
	idle     time.Duration
	absolute time.Duration

	mu       sync.Mutex
	sessions map[string]*Session
	onEnd    func(id string)
}

func NewManager(secret []byte, idle, absolute time.Duration, onEnd func(id string)) (*Manager, error) {
	if len(secret) < 32 {
		return nil, errors.New("session secret must be at least 32 bytes")
	}
	m := &Manager{secret: secret, idle: idle, absolute: absolute, sessions: map[string]*Session{}, onEnd: onEnd}
	go m.janitor()
	return m, nil
}

func (m *Manager) aead(cookieKey []byte) (cipher.AEAD, error) {
	h := sha256.New()
	h.Write(m.secret)
	h.Write(cookieKey)
	block, err := aes.NewCipher(h.Sum(nil))
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func randBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		panic(err)
	}
	return b
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// Create создаёт сессию и возвращает значение cookie.
func (m *Manager) Create(mailbox, password string, mode ext.AuthMode, source, ip, ua string) (*Session, string, error) {
	now := time.Now()
	s := &Session{
		ID: b64(randBytes(24)), Mailbox: mailbox, AuthMode: mode, Source: source,
		CSRF: b64(randBytes(24)), Created: now, LastSeen: now, RemoteIP: ip, UserAgent: ua,
		cookieKey: randBytes(32),
	}
	if password != "" {
		aead, err := m.aead(s.cookieKey)
		if err != nil {
			return nil, "", err
		}
		nonce := randBytes(aead.NonceSize())
		s.cipherPass = aead.Seal(nonce, nonce, []byte(password), []byte(s.ID))
	}
	cookie := s.ID + "." + b64(s.cookieKey)
	s.keyHash = sha256.Sum256(s.cookieKey)
	stored := *s
	stored.cookieKey = nil
	m.mu.Lock()
	m.sessions[s.ID] = &stored
	m.mu.Unlock()
	return s, cookie, nil
}

// Get находит сессию по значению cookie и продлевает её. Возвращает копию
// с ключом из cookie — она живёт только в рамках запроса.
func (m *Manager) Get(cookie string) (*Session, error) {
	id, key, ok := strings.Cut(cookie, ".")
	if !ok {
		return nil, ErrNotFound
	}
	keyBytes, err := base64.RawURLEncoding.DecodeString(key)
	if err != nil {
		return nil, ErrNotFound
	}
	m.mu.Lock()
	s, ok := m.sessions[id]
	m.mu.Unlock()
	if !ok {
		return nil, ErrNotFound
	}
	if h := sha256.Sum256(keyBytes); subtle.ConstantTimeCompare(h[:], s.keyHash[:]) != 1 {
		return nil, ErrNotFound
	}
	now := time.Now()
	if m.expired(s, now) {
		m.Delete(id)
		return nil, ErrNotFound
	}
	m.mu.Lock()
	s.LastSeen = now
	cp := *s
	m.mu.Unlock()
	cp.cookieKey = keyBytes
	return &cp, nil
}

func (m *Manager) expired(s *Session, now time.Time) bool {
	return now.Sub(s.LastSeen) > m.idle || now.Sub(s.Created) > m.absolute
}

func (m *Manager) Delete(id string) {
	m.mu.Lock()
	_, ok := m.sessions[id]
	delete(m.sessions, id)
	m.mu.Unlock()
	if ok && m.onEnd != nil {
		m.onEnd(id)
	}
}

// DeleteMailbox завершает все сессии ящика (например, после смены пароля).
func (m *Manager) DeleteMailbox(mailbox, exceptID string) int {
	var ids []string
	m.mu.Lock()
	for id, s := range m.sessions {
		if s.Mailbox == mailbox && id != exceptID {
			ids = append(ids, id)
		}
	}
	m.mu.Unlock()
	for _, id := range ids {
		m.Delete(id)
	}
	return len(ids)
}

func (m *Manager) janitor() {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for now := range t.C {
		var dead []string
		m.mu.Lock()
		for id, s := range m.sessions {
			if m.expired(s, now) {
				dead = append(dead, id)
			}
		}
		m.mu.Unlock()
		for _, id := range dead {
			m.Delete(id)
		}
	}
}

// CheckCSRF сравнивает токен за постоянное время.
func (s *Session) CheckCSRF(token string) bool {
	return token != "" && subtle.ConstantTimeCompare([]byte(token), []byte(s.CSRF)) == 1
}

// UpdatePassword перешифровывает пароль сессии после его смены.
// s — копия из Get (с ключом из cookie текущего запроса).
func (m *Manager) UpdatePassword(s *Session, password string) error {
	aead, err := m.aead(s.cookieKey)
	if err != nil {
		return err
	}
	nonce := randBytes(aead.NonceSize())
	ct := aead.Seal(nonce, nonce, []byte(password), []byte(s.ID))
	m.mu.Lock()
	defer m.mu.Unlock()
	stored, ok := m.sessions[s.ID]
	if !ok {
		return ErrNotFound
	}
	stored.cipherPass = ct
	return nil
}

// Alive сообщает, существует ли ещё сессия (для долгих соединений вроде SSE).
func (m *Manager) Alive(id string) bool {
	m.mu.Lock()
	s, ok := m.sessions[id]
	m.mu.Unlock()
	return ok && !m.expired(s, time.Now())
}

// Count — число живых сессий (для метрик).
func (m *Manager) Count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sessions)
}
