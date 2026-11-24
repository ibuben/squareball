package carddav

import (
	"context"
	"errors"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ibuben/squareball/adapters/internal/dav"
	"github.com/ibuben/squareball/ext"
)

func TestPresets(t *testing.T) {
	a := &Adapter{cfg: Config{Presets: []Preset{
		{ID: "all", Name: "Все", URL: "https://dav.example.com/%u/", Username: "%l", Password: "session"},
		{ID: "corp", URL: "https://dav.corp/%d/", Username: "svc", PasswordFile: "x", Secret: "s3", Domains: []string{"Corp.Test"}},
	}}}
	got := a.Presets("ivan petrov@corp.test")
	if len(got) != 2 || got[0].Account.URL != "https://dav.example.com/ivan%20petrov@corp.test/" || got[0].Account.Username != "ivan petrov" ||
		got[0].Account.Password != ext.PasswordSession || !got[0].Account.Trusted || got[1].Account.Password != "s3" {
		t.Fatalf("presets: %+v", got)
	}
	if got := a.Presets("a@other.test"); len(got) != 1 {
		t.Fatalf("domain filter: %+v", got)
	}
}

func TestPrivateNetworksBlocked(t *testing.T) {
	for ip, ok := range map[string]bool{"8.8.8.8": true, "127.0.0.1": false, "10.1.2.3": false, "192.168.0.1": false,
		"169.254.169.254": false, "100.64.1.1": false, "::1": false, "fd00::1": false, "2a00:1450::1": true} {
		if publicIP(net.ParseIP(ip)) != ok {
			t.Errorf("publicIP(%s) != %v", ip, ok)
		}
	}
	a := &Adapter{hc: dav.Clients{Public: newHTTPClient(5*time.Second, false), Trusted: newHTTPClient(5*time.Second, true), Agent: "test"}}
	_, err := a.Discover(context.Background(), ext.ContactAccount{URL: "http://127.0.0.1:5232/alice/", Username: "alice", Password: "x"})
	if !errors.Is(err, ext.ErrContactForbidden) {
		t.Fatalf("loopback must be forbidden for user accounts: %v", err)
	}
}

// С Radicale стенда (deploy/devstand): alice / Test12345!, книга /alice/contacts/.
func TestRadicale(t *testing.T) {
	if os.Getenv("WEBMAIL_IT") != "1" {
		t.Skip("set WEBMAIL_IT=1 with Radicale on 127.0.0.1:5232")
	}
	ctx := context.Background()
	a := &Adapter{hc: dav.Clients{Public: newHTTPClient(10*time.Second, true), Trusted: newHTTPClient(10*time.Second, true), Agent: "test"}}
	acc := ext.ContactAccount{URL: "http://127.0.0.1:5232/", Username: "alice", Password: "Test12345!"}
	if _, err := a.Discover(ctx, ext.ContactAccount{URL: acc.URL, Username: "alice", Password: "bad"}); !errors.Is(err, ext.ErrContactAuth) {
		t.Fatalf("bad password: %v", err)
	}
	var book string
	for _, u := range []string{"http://127.0.0.1:5232/", "http://127.0.0.1:5232/alice/", "http://127.0.0.1:5232/alice/contacts/"} {
		acc.URL = u
		books, err := a.Discover(ctx, acc)
		if err != nil || len(books) == 0 {
			t.Fatalf("discover %s: %v %v", u, books, err)
		}
		for _, b := range books {
			if strings.HasSuffix(b.URL, "/alice/contacts/") && b.Name == "Рабочие" {
				book = b.URL
			}
		}
		if book == "" {
			t.Fatalf("discover %s: no contacts book in %+v", u, books)
		}
	}
	card := []byte("BEGIN:VCARD\r\nVERSION:3.0\r\nUID:it-test\r\nFN:Тест Тестов\r\nEMAIL:it@example.org\r\nEND:VCARD\r\n")
	href, etag, err := a.Put(ctx, acc, book, "", "", card)
	if err != nil || href == "" {
		t.Fatalf("put: %q %q %v", href, etag, err)
	}
	list, err := a.List(ctx, acc, book)
	if err != nil || list[href] == "" {
		t.Fatalf("list: %v %v", list, err)
	}
	if etag != "" && list[href] != etag {
		t.Fatalf("etag mismatch: put %q list %q", etag, list[href])
	}
	etag = list[href]
	cards, err := a.Get(ctx, acc, book, []string{href})
	if err != nil || len(cards) != 1 || !strings.Contains(string(cards[0].VCard), "Тест Тестов") || cards[0].ETag != etag {
		t.Fatalf("get: %+v %v", cards, err)
	}
	if _, _, err := a.Put(ctx, acc, book, href, "stale", card); !errors.Is(err, ext.ErrContactConflict) {
		t.Fatalf("stale etag: %v", err)
	}
	if _, _, err := a.Put(ctx, acc, book, href, etag, []byte(strings.Replace(string(card), "Тестов", "Изменённый", 1))); err != nil {
		t.Fatalf("update: %v", err)
	}
	list, _ = a.List(ctx, acc, book)
	if err := a.Delete(ctx, acc, book, href, list[href]); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := a.Delete(ctx, acc, book, href, ""); !errors.Is(err, ext.ErrContactNotFound) {
		t.Fatalf("delete twice: %v", err)
	}
}
