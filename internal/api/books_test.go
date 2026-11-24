package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/ibuben/squareball/adapters/carddav"
	"github.com/ibuben/squareball/ext"
)

type yamlNode string

func (y yamlNode) Decode(v any) error {
	dec := yaml.NewDecoder(strings.NewReader(string(y)))
	dec.KnownFields(true)
	return dec.Decode(v)
}

// Внешняя книга CardDAV (Radicale стенда): пресет с паролем ящика из сессии.
func TestIntegrationContactBooks(t *testing.T) {
	if os.Getenv("WEBMAIL_IT") != "1" {
		t.Skip("set WEBMAIL_IT=1 with deploy/devstand and Radicale running")
	}
	srv := newServer(t, testConfig(t))
	err := carddav.New().Init(context.Background(), yamlNode(`
enabled: true
presets:
  - id: stand
    name: Radicale
    url: http://127.0.0.1:5232/%l/
    username: "%l"
    password: session
`), srv)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	jar, _ := cookiejar.New(nil)
	c := &client{t: t, base: ts.URL, hc: &http.Client{Jar: jar}}
	st, me := c.do("POST", "/api/v1/auth/login", map[string]string{"username": "alice", "password": "Test12345!"})
	if st != 200 {
		t.Fatalf("login: %d %v", st, me)
	}
	c.csrf = me["csrf"].(string)

	st, body := c.do("GET", "/api/v1/books", nil)
	if st != 200 || body["enabled"] != true || body["stale"] != true || len(body["accounts"].([]any)) != 1 {
		t.Fatalf("books: %d %v", st, body)
	}
	st, body = c.do("POST", "/api/v1/books/sync", map[string]any{"force": true})
	if st != 200 {
		t.Fatalf("sync: %d %v", st, body)
	}
	var bookID float64
	for _, b := range body["books"].([]any) {
		b := b.(map[string]any)
		if b["name"] == "Рабочие" {
			bookID = b["id"].(float64)
			if b["error"] != nil || b["count"].(float64) < 1 {
				t.Fatalf("book: %v", b)
			}
		}
	}
	if bookID == 0 {
		t.Fatalf("no Radicale book: %v", body)
	}
	bid := itoa(int(bookID))

	// Карточка с сервера видна в книге и в подсказках.
	st, body = c.do("GET", "/api/v1/contacts?book="+bid+"&q=карина", nil)
	if st != 200 || body["total"].(float64) != 1 {
		t.Fatalf("contacts: %d %v", st, body)
	}
	karina := body["contacts"].([]any)[0].(map[string]any)
	if _, sug := c.do("GET", "/api/v1/contacts/suggest?q=karina", nil); !strings.Contains(toJSON(sug), "karina@example.org") {
		t.Fatalf("suggest: %v", sug)
	}

	// Правка уходит на сервер, адрес (ADR) из карточки сохраняется.
	karina["org"] = "Squareball"
	if st, body = c.do("POST", "/api/v1/contacts", karina); st != 200 || body["org"] != "Squareball" {
		t.Fatalf("save remote: %d %v", st, body)
	}
	raw := radicaleGet(t, "/alice/contacts/seed-1.vcf")
	if !strings.Contains(raw, "ORG:Squareball") || !strings.Contains(raw, "Навои") {
		t.Fatalf("server card: %s", raw)
	}

	// Новый контакт в книге создаётся на сервере, удаление удаляет его там же.
	st, body = c.do("POST", "/api/v1/contacts", map[string]any{"book": bookID, "name": "Новый Из Вебмейла",
		"emails": []map[string]string{{"value": "new-it@example.org"}}})
	if st != 200 || body["book"] != bookID {
		t.Fatalf("create remote: %d %v", st, body)
	}
	newID := body["id"]
	st, body = c.do("POST", "/api/v1/books/sync", map[string]any{"force": true})
	if st != 200 {
		t.Fatalf("resync: %d", st)
	}
	if _, list := c.do("GET", "/api/v1/contacts?book="+bid+"&q=new-it", nil); list["total"].(float64) != 1 {
		t.Fatalf("created card not on server after resync: %v", list)
	}
	if st, body = c.do("POST", "/api/v1/contacts/delete", map[string]any{"ids": []any{newID}}); st != 200 || body["deleted"].(float64) != 1 {
		t.Fatalf("delete remote: %d %v", st, body)
	}
	c.do("POST", "/api/v1/books/sync", map[string]any{"force": true})
	if _, list := c.do("GET", "/api/v1/contacts?book="+bid+"&q=new-it", nil); list["total"].(float64) != 0 {
		t.Fatalf("deleted card came back: %v", list)
	}

	// Возврат исходной карточки и проверка, что пользовательские учётки не ходят во внутреннюю сеть.
	karina["org"] = ""
	c.do("POST", "/api/v1/contacts", karina)
	st, body = c.do("POST", "/api/v1/books/accounts", map[string]string{"url": "http://127.0.0.1:5232/", "username": "alice", "password": "Test12345!"})
	if st != 422 || body["error"] != "books.forbidden" {
		t.Fatalf("private network must be refused: %d %v", st, body)
	}
	if st, body = c.do("POST", "/api/v1/books/accounts/delete", map[string]any{"id": body["id"]}); st == 200 {
		t.Fatalf("preset deletion: %d", st)
	}
}

func radicaleGet(t *testing.T, path string) string {
	t.Helper()
	req, _ := http.NewRequest("GET", "http://127.0.0.1:5232"+path, nil)
	req.SetBasicAuth("alice", "Test12345!")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var sb strings.Builder
	_, _ = io.Copy(&sb, resp.Body)
	return sb.String()
}

var _ ext.ContactSource = (*carddav.Adapter)(nil)

func TestDavLoopback(t *testing.T) {
	for _, u := range []string{"http://127.0.0.1:5232/", "http://localhost/bob%40example.com/", "http://[::1]/"} {
		if !davLoopback(u) {
			t.Fatalf("want loopback: %s", u)
		}
	}
	for _, u := range []string{"https://dav.example.com/", "http://10.0.0.1/", "", "not-a-url"} {
		if davLoopback(u) {
			t.Fatalf("want remote: %s", u)
		}
	}
}

func toJSON(v any) string { b, _ := json.Marshal(v); return string(b) }
