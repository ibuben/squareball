package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ibuben/squareball/ext"
	"github.com/ibuben/squareball/internal/config"
	"github.com/ibuben/squareball/internal/store"
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
		c.Sieve.Enabled = true
		c.Sieve.Endpoint = config.Endpoint{Host: "127.0.0.1", Port: 4190, Security: "starttls", TLSVerify: false}
	}
	return c
}

func newServer(t *testing.T, c *config.Config) *Server {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	s, err := New(Options{Config: c, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), SessionSecret: bytes.Repeat([]byte("s"), 32), Store: st})
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

func (c *client) multipart(path, draft string, files map[string]string) (int, map[string]any) {
	c.t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("draft", draft)
	for name, body := range files {
		fw, _ := mw.CreateFormFile("attachments", name)
		fw.Write([]byte(body))
	}
	mw.Close()
	req, _ := http.NewRequest("POST", c.base+path, &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-CSRF-Token", c.csrf)
	return c.send(req)
}

func TestIntegrationDrafts(t *testing.T) {
	if os.Getenv("WEBMAIL_IT") != "1" {
		t.Skip("set WEBMAIL_IT=1 with deploy/devstand running")
	}
	ts := httptest.NewServer(newServer(t, testConfig(t)).Handler())
	defer ts.Close()
	jar, _ := cookiejar.New(nil)
	c := &client{t: t, base: ts.URL, hc: &http.Client{Jar: jar}}
	st, me := c.do("POST", "/api/v1/auth/login", map[string]string{"username": "alice", "password": "Test12345!"})
	if st != 200 {
		t.Fatalf("login %d", st)
	}
	c.csrf = me["csrf"].(string)

	// 1. Черновик без получателя «Кому», но со скрытой копией и вложением.
	st, body := c.multipart("/api/v1/drafts", `{"bcc":[{"email":"bob@test.local"}],"subject":"Черновик IT","html":"<p>v1</p>"}`,
		map[string]string{"plan.txt": "план"})
	if st != 200 {
		t.Fatalf("save draft: %d %v", st, body)
	}
	ref := body["draft_ref"].(map[string]any)
	folder, uid1 := ref["folder"].(string), int(ref["uid"].(float64))
	if folder != "Drafts" || uid1 == 0 {
		t.Fatalf("draft ref: %v", ref)
	}
	st, view := c.do("GET", "/api/v1/message?peek=1&folder=Drafts&uid="+itoa(uid1), nil)
	msg := view["message"].(map[string]any)
	if st != 200 || len(msg["bcc"].([]any)) != 1 || len(msg["attachments"].([]any)) != 1 {
		t.Fatalf("draft content: %d %v", st, msg)
	}

	// 2. Пересохранение: вложение переносится из прежней версии, старая удаляется.
	st, body = c.multipart("/api/v1/drafts", `{"to":[{"email":"bob@test.local"}],"subject":"Черновик IT","html":"<p>v2</p>",
		"draft_ref":{"folder":"Drafts","uid":`+itoa(uid1)+`},"keep_attachments":[0]}`, nil)
	if st != 200 {
		t.Fatalf("resave: %d %v", st, body)
	}
	uid2 := int(body["draft_ref"].(map[string]any)["uid"].(float64))
	if st, _ := c.do("GET", "/api/v1/message?peek=1&folder=Drafts&uid="+itoa(uid1), nil); st != 404 {
		t.Errorf("old draft still exists: %d", st)
	}
	_, view = c.do("GET", "/api/v1/message?peek=1&folder=Drafts&uid="+itoa(uid2), nil)
	if atts := view["message"].(map[string]any)["attachments"].([]any); len(atts) != 1 {
		t.Fatalf("attachment not kept: %v", atts)
	}

	// 3. Отправка из черновика удаляет его.
	st, body = c.multipart("/api/v1/send", `{"to":[{"email":"bob@test.local"}],"subject":"Черновик IT","html":"<p>v2</p>",
		"draft_ref":{"folder":"Drafts","uid":`+itoa(uid2)+`},"keep_attachments":[0]}`, nil)
	if st != 200 {
		t.Fatalf("send: %d %v", st, body)
	}
	if st, _ := c.do("GET", "/api/v1/message?peek=1&folder=Drafts&uid="+itoa(uid2), nil); st != 404 {
		t.Errorf("draft not removed after send: %d", st)
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

func TestIntegrationIdentities(t *testing.T) {
	if os.Getenv("WEBMAIL_IT") != "1" {
		t.Skip("set WEBMAIL_IT=1 with deploy/devstand running")
	}
	ts := httptest.NewServer(newServer(t, testConfig(t)).Handler())
	defer ts.Close()
	jar, _ := cookiejar.New(nil)
	c := &client{t: t, base: ts.URL, hc: &http.Client{Jar: jar}}
	_, me := c.do("POST", "/api/v1/auth/login", map[string]string{"username": "alice", "password": "Test12345!"})
	c.csrf = me["csrf"].(string)

	st, body := c.do("GET", "/api/v1/identities", nil)
	if st != 200 || len(body["identities"].([]any)) != 1 || body["level"] != "single" {
		t.Fatalf("list: %d %v", st, body)
	}
	if st, body := c.do("POST", "/api/v1/identities", map[string]any{"email": "boss@test.local", "name": "Boss"}); st != 400 {
		t.Fatalf("foreign address accepted at level single: %d %v", st, body)
	}
	st, body = c.do("POST", "/api/v1/identities", map[string]any{
		"name": "Алиса из поддержки", "email": "alice@test.local", "reply_to": "support@test.local",
		"signature": `<p>С уважением, Алиса</p><script>alert(1)</script>`, "is_default": false,
	})
	if st != 200 {
		t.Fatalf("save: %d %v", st, body)
	}
	if strings.Contains(body["signature"].(string), "script") {
		t.Fatalf("signature not sanitized: %v", body["signature"])
	}
	id := int(body["id"].(float64))

	subject := "Identity IT " + strconv.FormatInt(time.Now().UnixNano(), 10)
	st, body = c.multipart("/api/v1/send", `{"to":[{"email":"bob@test.local"}],"subject":"`+subject+`","text":"hi","identity_id":`+itoa(id)+`}`, nil)
	if st != 200 {
		t.Fatalf("send: %d %v", st, body)
	}
	// Копия в «Отправленных»: From — имя отправителя, Reply-To — из отправителя.
	_, page := c.do("GET", "/api/v1/messages?folder=Sent&q="+strings.ReplaceAll(subject, " ", "+"), nil)
	msgs := page["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("sent copy not found: %v", page)
	}
	uid := int(msgs[0].(map[string]any)["uid"].(float64))
	_, view := c.do("GET", "/api/v1/message?peek=1&folder=Sent&uid="+itoa(uid), nil)
	m := view["message"].(map[string]any)
	from := m["from"].([]any)[0].(map[string]any)
	rt := m["reply_to"].([]any)[0].(map[string]any)
	if from["name"] != "Алиса из поддержки" || rt["email"] != "support@test.local" {
		t.Fatalf("headers: from=%v reply_to=%v", from, rt)
	}

	if st, _ := c.do("POST", "/api/v1/identities/delete", map[string]any{"id": id}); st != 200 {
		t.Fatalf("delete: %d", st)
	}
}

func TestIntegrationSieve(t *testing.T) {
	if os.Getenv("WEBMAIL_IT") != "1" {
		t.Skip("set WEBMAIL_IT=1 with deploy/devstand running")
	}
	ts := httptest.NewServer(newServer(t, testConfig(t)).Handler())
	defer ts.Close()
	jar, _ := cookiejar.New(nil)
	c := &client{t: t, base: ts.URL, hc: &http.Client{Jar: jar}}
	_, me := c.do("POST", "/api/v1/auth/login", map[string]string{"username": "bob", "password": "Test12345!"})
	c.csrf = me["csrf"].(string)

	st, cur := c.do("GET", "/api/v1/sieve", nil)
	if st != 200 || cur["mode"] == nil {
		t.Fatalf("get: %d %v", st, cur)
	}
	ruleset := map[string]any{
		"rules": []any{map[string]any{"name": "IT-фильтр", "enabled": true, "match": "all", "stop": true,
			"conditions": []any{map[string]any{"field": "subject", "op": "contains", "value": "[IT-sieve]"}},
			"actions":    []any{map[string]any{"type": "seen"}, map[string]any{"type": "fileinto", "value": "Archive"}}}},
		"vacation": map[string]any{"enabled": false, "subject": "", "body": "", "days": 7},
		"forward":  map[string]any{"enabled": false, "to": "", "keep_copy": false},
	}
	// Устаревший tag — конфликт, скрипт не трогаем.
	if st, body := c.do("PUT", "/api/v1/sieve", map[string]any{"mode": "rules", "tag": "stale", "ruleset": ruleset}); st != 409 {
		t.Fatalf("stale tag accepted: %d %v", st, body)
	}
	st, saved := c.do("PUT", "/api/v1/sieve", map[string]any{"mode": "rules", "tag": cur["tag"], "ruleset": ruleset})
	if st != 200 {
		t.Fatalf("put: %d %v", st, saved)
	}
	// Ошибка синтаксиса в текстовом режиме возвращается с текстом сервера.
	if st, body := c.do("PUT", "/api/v1/sieve", map[string]any{"mode": "raw", "tag": saved["tag"], "raw": "if true { fileinto }"}); st != 400 || body["detail"] == "" {
		t.Fatalf("bad raw: %d %v", st, body)
	}

	// Правило действительно работает: письмо с меткой в теме уходит в Archive прочитанным.
	subject := "[IT-sieve] " + strconv.FormatInt(time.Now().UnixNano(), 10)
	if st, body := c.multipart("/api/v1/send", `{"to":[{"email":"bob@test.local"}],"subject":"`+subject+`","text":"x"}`, nil); st != 200 {
		t.Fatalf("send: %d %v", st, body)
	}
	var found map[string]any
	for i := 0; i < 20 && found == nil; i++ {
		time.Sleep(250 * time.Millisecond)
		_, page := c.do("GET", "/api/v1/messages?folder=Archive&q="+strings.ReplaceAll(subject, " ", "+"), nil)
		if msgs, _ := page["messages"].([]any); len(msgs) == 1 {
			found = msgs[0].(map[string]any)
		}
	}
	if found == nil || found["seen"] != true {
		t.Fatalf("message not filed by sieve: %v", found)
	}

	// Убираем за собой: пустой набор правил.
	_, now := c.do("GET", "/api/v1/sieve", nil)
	empty := map[string]any{"rules": []any{}, "vacation": map[string]any{"days": 7}, "forward": map[string]any{}}
	if st, body := c.do("PUT", "/api/v1/sieve", map[string]any{"mode": "rules", "tag": now["tag"], "ruleset": empty}); st != 200 {
		t.Fatalf("cleanup: %d %v", st, body)
	}
}

func TestIntegrationContacts(t *testing.T) {
	if os.Getenv("WEBMAIL_IT") != "1" {
		t.Skip("set WEBMAIL_IT=1 with deploy/devstand running")
	}
	ts := httptest.NewServer(newServer(t, testConfig(t)).Handler())
	defer ts.Close()
	jar, _ := cookiejar.New(nil)
	c := &client{t: t, base: ts.URL, hc: &http.Client{Jar: jar}}
	_, me := c.do("POST", "/api/v1/auth/login", map[string]string{"username": "alice", "password": "Test12345!"})
	c.csrf = me["csrf"].(string)

	st, g := c.do("POST", "/api/v1/groups", map[string]any{"name": "Команда"})
	if st != 200 {
		t.Fatalf("group: %d %v", st, g)
	}
	gid := g["id"].(float64)
	if st, body := c.do("POST", "/api/v1/contacts", map[string]any{"first_name": "Боб", "emails": []any{map[string]any{"value": "not-an-email"}}}); st != 400 {
		t.Fatalf("invalid email accepted: %d %v", st, body)
	}
	st, ct := c.do("POST", "/api/v1/contacts", map[string]any{"first_name": "Боб", "last_name": "Тестов",
		"emails": []any{map[string]any{"value": "bob@test.local", "label": "work"}}, "groups": []any{gid}})
	if st != 200 || ct["id"] == nil {
		t.Fatalf("save: %d %v", st, ct)
	}

	_, sg := c.do("GET", "/api/v1/contacts/suggest?q=%D0%BA%D0%BE%D0%BC", nil) // "ком"
	list := sg["suggestions"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["kind"] != "group" || len(list[0].(map[string]any)["members"].([]any)) != 1 {
		t.Fatalf("group suggestion: %v", sg)
	}

	// Импорт vCard: один новый, один дубль по адресу.
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "contacts.vcf")
	fw.Write([]byte("BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Новый Контакт\r\nEMAIL:new@test.local\r\nEND:VCARD\r\n" +
		"BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Боб дубль\r\nEMAIL:BOB@test.local\r\nEND:VCARD\r\n"))
	mw.Close()
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/contacts/import", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-CSRF-Token", c.csrf)
	if st, body := c.send(req); st != 200 || body["added"] != 1.0 || body["skipped"] != 1.0 {
		t.Fatalf("import: %d %v", st, body)
	}

	res, err := c.hc.Get(ts.URL + "/api/v1/contacts/export?format=csv")
	if err != nil {
		t.Fatal(err)
	}
	csvData, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if !strings.Contains(string(csvData), "new@test.local") || !strings.Contains(res.Header.Get("Content-Disposition"), ".csv") {
		t.Fatalf("export: %s %s", res.Header, csvData)
	}

	// Получатели отправленного письма попадают в собранные адреса.
	if st, body := c.multipart("/api/v1/send", `{"to":[{"email":"bob@test.local","name":"Боб"}],"cc":[{"email":"carol@test.local","name":"Кэрол"}],"subject":"collect","text":"x"}`, nil); st != 200 {
		t.Fatalf("send: %d %v", st, body)
	}
	_, coll := c.do("GET", "/api/v1/contacts/collected?q=carol", nil)
	if l := coll["collected"].([]any); len(l) != 1 || l[0].(map[string]any)["name"] != "Кэрол" {
		t.Fatalf("collected: %v", coll)
	}
}

type fakeChanger struct {
	caps ext.PasswordCaps
	got  []ext.PasswordChange
	err  error
}

func (f *fakeChanger) Capabilities() ext.PasswordCaps { return f.caps }
func (f *fakeChanger) Change(_ context.Context, r ext.PasswordChange) error {
	f.got = append(f.got, r)
	return f.err
}

// Смена пароля: политика, проверка текущего через IMAP, сброс других сессий.
// Драйвер подменён: настоящий пароль на стенде не меняется.
func TestIntegrationPassword(t *testing.T) {
	if os.Getenv("WEBMAIL_IT") != "1" {
		t.Skip("set WEBMAIL_IT=1 with deploy/devstand running")
	}
	cfg := testConfig(t)
	s := newServer(t, cfg)
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	login := func() *client {
		jar, _ := cookiejar.New(nil)
		c := &client{t: t, base: ts.URL, hc: &http.Client{Jar: jar}}
		st, me := c.do("POST", "/api/v1/auth/login", map[string]string{"username": "bob", "password": "Test12345!"})
		if st != 200 {
			t.Fatalf("login: %d %v", st, me)
		}
		c.csrf = me["csrf"].(string)
		return c
	}
	c, other := login(), login()
	if st, _ := c.do("GET", "/api/v1/account/password", nil); st != 404 {
		t.Fatalf("driver none: %d", st)
	}
	fake := &fakeChanger{caps: ext.PasswordCaps{Policy: &ext.PasswordPolicy{MinLength: 12, ForbidChars: ":"}}}
	cfg.Password.Driver = "fake"
	s.SetPasswordChanger("fake", fake)
	st, body := c.do("GET", "/api/v1/account/password", nil)
	pol, _ := body["policy"].(map[string]any)
	if st != 200 || pol["min_length"] != float64(12) || pol["forbid_chars"] != ":" {
		t.Fatalf("policy: %d %v", st, body)
	}
	for _, tc := range []struct {
		cur, new, code, detail string
		status                 int
	}{
		{"Test12345!", "Short1!", "password.weak", "too_short", 400},
		{"Test12345!", "Colon:Pass-123", "password.weak", "chars", 400},
		{"Test12345!", "Bob-Strong-Pass1", "password.weak", "mailbox", 400},
		{"Wrong-Current1", "New-Strong-Pass1", "password.current", "", 403},
	} {
		st, body := c.do("POST", "/api/v1/account/password", map[string]string{"current": tc.cur, "new": tc.new})
		if st != tc.status || body["error"] != tc.code || (tc.detail != "" && body["detail"] != tc.detail) {
			t.Errorf("%s: %d %v", tc.new, st, body)
		}
	}
	if len(fake.got) != 0 {
		t.Fatalf("driver called on rejected request: %+v", fake.got)
	}
	fake.err = ext.ErrWeakPassword
	if st, body := c.do("POST", "/api/v1/account/password", map[string]string{"current": "Test12345!", "new": "New-Strong-Pass1"}); st != 400 || body["detail"] != "backend" {
		t.Fatalf("backend weak: %d %v", st, body)
	}
	fake.err = nil
	if st, body := c.do("POST", "/api/v1/account/password", map[string]string{"current": "Test12345!", "new": "New-Strong-Pass1"}); st != 200 {
		t.Fatalf("change: %d %v", st, body)
	}
	if r := fake.got[len(fake.got)-1]; r.Mailbox != "bob@test.local" || r.Current != "Test12345!" || r.New != "New-Strong-Pass1" || r.RemoteIP == "" {
		t.Fatalf("request: %+v", r)
	}
	if st, _ := c.do("GET", "/api/v1/auth/me", nil); st != 200 {
		t.Fatalf("own session dropped: %d", st)
	}
	if st, _ := other.do("GET", "/api/v1/auth/me", nil); st != 401 {
		t.Fatalf("other session survived: %d", st)
	}
}
