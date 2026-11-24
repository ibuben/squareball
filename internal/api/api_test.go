package api

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ibuben/squareball/internal/config"
)

func testConfig(t *testing.T) *config.Config {
	t.Helper()
	c := config.Defaults()
	c.Server.PublicURL = "http://127.0.0.1"
	c.Session.SecretFile = "unused"
	c.IMAP.Host, c.SMTP.Host = "127.0.0.1", "127.0.0.1"
	if os.Getenv("WEBMAIL_IT") == "1" { // стенд deploy/devstand
		c.IMAP.Port, c.IMAP.Security, c.IMAP.TLSVerify = 993, "tls", false
		c.SMTP.Port, c.SMTP.Security, c.SMTP.TLSVerify = 587, "starttls", false
		c.Login.DefaultDomain = "test.local"
	}
	return c
}

func newServer(t *testing.T, c *config.Config) *Server {
	t.Helper()
	s, err := New(Options{Config: c, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), SessionSecret: bytes.Repeat([]byte("s"), 32)})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestClientIPTrustsOnlyConfiguredProxies(t *testing.T) {
	s := newServer(t, testConfig(t))
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "127.0.0.1:5000"
	r.Header.Set("X-Forwarded-For", "203.0.113.7, 127.0.0.1")
	if ip := s.ClientIP(r); ip != "203.0.113.7" {
		t.Errorf("via trusted proxy: %s", ip)
	}
	r.RemoteAddr = "198.51.100.9:5000" // не доверенный — заголовок игнорируется
	if ip := s.ClientIP(r); ip != "198.51.100.9" {
		t.Errorf("untrusted proxy: %s", ip)
	}
}

func TestLimiter(t *testing.T) {
	l := newLimiter(config.Rate{N: 2, Period: 50 * time.Millisecond})
	if !l.Allow("k") || !l.Allow("k") || l.Allow("k") {
		t.Fatal("limit not enforced")
	}
	time.Sleep(60 * time.Millisecond)
	if !l.Allow("k") {
		t.Fatal("window did not slide")
	}
	l.Reset("k")
	if !l.Allow("k") || !l.Allow("k") {
		t.Fatal("reset failed")
	}
}

func TestUnauthenticatedAndHeaders(t *testing.T) {
	ts := httptest.NewServer(newServer(t, testConfig(t)).Handler())
	defer ts.Close()
	res, err := http.Get(ts.URL + "/api/v1/folders")
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 401 {
		t.Errorf("status %d", res.StatusCode)
	}
	if !strings.Contains(res.Header.Get("Content-Security-Policy"), "frame-ancestors 'none'") ||
		res.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("headers: %v", res.Header)
	}
}

// --- интеграция со стендом ---------------------------------------------------

type client struct {
	t    *testing.T
	base string
	hc   *http.Client
	csrf string
}

func (c *client) do(method, path string, body any) (int, map[string]any) {
	c.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.base+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.csrf != "" {
		req.Header.Set("X-CSRF-Token", c.csrf)
	}
	return c.send(req)
}

func (c *client) send(req *http.Request) (int, map[string]any) {
	res, err := c.hc.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer res.Body.Close()
	out := map[string]any{}
	_ = json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

func TestIntegrationWithDevstand(t *testing.T) {
	if os.Getenv("WEBMAIL_IT") != "1" {
		t.Skip("set WEBMAIL_IT=1 with deploy/devstand running")
	}
	ts := httptest.NewServer(newServer(t, testConfig(t)).Handler())
	defer ts.Close()
	jar, _ := cookiejar.New(nil)
	c := &client{t: t, base: ts.URL, hc: &http.Client{Jar: jar}}

	if st, body := c.do("POST", "/api/v1/auth/login", map[string]string{"username": "alice", "password": "nope"}); st != 401 || body["error"] != "auth.failed" {
		t.Fatalf("bad password: %d %v", st, body)
	}
	st, me := c.do("POST", "/api/v1/auth/login", map[string]string{"username": "alice", "password": "Test12345!"})
	if st != 200 || me["mailbox"] != "alice@test.local" {
		t.Fatalf("login: %d %v", st, me)
	}
	st, body := c.do("POST", "/api/v1/messages/flags", map[string]any{"folder": "INBOX", "uids": []int{1}, "flags": []string{`\Seen`}, "add": true})
	if st != 403 || body["error"] != "csrf" {
		t.Fatalf("csrf not enforced: %d %v", st, body)
	}
	c.csrf = me["csrf"].(string)

	st, body = c.do("GET", "/api/v1/folders", nil)
	if st != 200 {
		t.Fatalf("folders: %d %v", st, body)
	}
	roles := map[string]bool{}
	for _, f := range body["folders"].([]any) {
		if r, ok := f.(map[string]any)["role"].(string); ok {
			roles[r] = true
		}
	}
	for _, r := range []string{"inbox", "sent", "drafts", "trash", "junk"} {
		if !roles[r] {
			t.Errorf("role %s not detected: %v", r, roles)
		}
	}

	// Отправка с вложением; чужой From невозможен — From всегда ящик сессии.
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("draft", `{"to":[{"email":"bob@test.local"}],"bcc":[{"email":"alice@test.local"}],"subject":"IT test","text":"hello"}`)
	fw, _ := mw.CreateFormFile("attachments", "a.txt")
	fw.Write([]byte("attachment"))
	mw.Close()
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/send", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-CSRF-Token", c.csrf)
	if st, body := c.send(req); st != 200 || body["ok"] != true {
		t.Fatalf("send: %d %v", st, body)
	}

	if st, _ := c.do("POST", "/api/v1/auth/logout", nil); st != 200 {
		t.Fatalf("logout: %d", st)
	}
	if st, _ := c.do("GET", "/api/v1/folders", nil); st != 401 {
		t.Fatalf("session survived logout: %d", st)
	}
}
