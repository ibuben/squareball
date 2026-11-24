package session

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/ibuben/squareball/ext"
)

var secret = bytes.Repeat([]byte("k"), 32)

func TestPasswordNeedsCookieKey(t *testing.T) {
	m, _ := NewManager(secret, time.Hour, time.Hour, nil)
	_, cookie, err := m.Create("bob@test", "s3cret", ext.AuthPassword, "password", "1.2.3.4", "ua")
	if err != nil {
		t.Fatal(err)
	}
	s, err := m.Get(cookie)
	if err != nil {
		t.Fatal(err)
	}
	if pw, err := s.Password(m); err != nil || pw != "s3cret" {
		t.Fatalf("password = %q, %v", pw, err)
	}
	// В хранилище нет ни пароля в открытом виде, ни ключа из cookie.
	stored := m.sessions[s.ID]
	if stored.cookieKey != nil || bytes.Contains(stored.cipherPass, []byte("s3cret")) {
		t.Fatal("stored session leaks key or plaintext")
	}
	if _, err := stored.Password(m); err == nil {
		t.Fatal("stored session must not decrypt without cookie key")
	}
}

func TestForgedCookieRejected(t *testing.T) {
	m, _ := NewManager(secret, time.Hour, time.Hour, nil)
	_, cookie, _ := m.Create("bob@test", "pw", ext.AuthPassword, "password", "", "")
	id, _, _ := strings.Cut(cookie, ".")
	for _, bad := range []string{id, id + ".AAAA", "x.y", ""} {
		if _, err := m.Get(bad); err == nil {
			t.Errorf("cookie %q accepted", bad)
		}
	}
}

func TestIdleTimeout(t *testing.T) {
	ended := ""
	m, _ := NewManager(secret, 10*time.Millisecond, time.Hour, func(id string) { ended = id })
	s, cookie, _ := m.Create("bob@test", "pw", ext.AuthPassword, "password", "", "")
	time.Sleep(20 * time.Millisecond)
	if _, err := m.Get(cookie); err == nil {
		t.Fatal("expired session accepted")
	}
	if ended != s.ID {
		t.Error("onEnd not called")
	}
}

func TestUpdatePasswordAndDeleteMailbox(t *testing.T) {
	m, _ := NewManager(secret, time.Hour, time.Hour, nil)
	_, c1, _ := m.Create("bob@test", "old", ext.AuthPassword, "password", "", "")
	_, c2, _ := m.Create("bob@test", "old", ext.AuthPassword, "password", "", "")
	s1, _ := m.Get(c1)
	if err := m.UpdatePassword(s1, "new"); err != nil {
		t.Fatal(err)
	}
	if n := m.DeleteMailbox("bob@test", s1.ID); n != 1 {
		t.Errorf("deleted %d, want 1", n)
	}
	if _, err := m.Get(c2); err == nil {
		t.Error("other session survived")
	}
	s1, err := m.Get(c1)
	if err != nil {
		t.Fatal(err)
	}
	if pw, _ := s1.Password(m); pw != "new" {
		t.Errorf("password = %q, want new", pw)
	}
}

func TestCSRF(t *testing.T) {
	m, _ := NewManager(secret, time.Hour, time.Hour, nil)
	s, _, _ := m.Create("bob@test", "pw", ext.AuthPassword, "password", "", "")
	if !s.CheckCSRF(s.CSRF) || s.CheckCSRF("") || s.CheckCSRF("nope") {
		t.Error("csrf check wrong")
	}
}

func TestShortSecretRejected(t *testing.T) {
	if _, err := NewManager([]byte("short"), time.Hour, time.Hour, nil); err == nil {
		t.Error("short secret accepted")
	}
}
