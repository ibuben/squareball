package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestIdentitiesLifecycle(t *testing.T) {
	s, ctx := open(t), context.Background()
	ids, err := s.Identities(ctx, "bob@x.test")
	if err != nil || len(ids) != 1 || ids[0].Email != "bob@x.test" || !ids[0].IsDefault {
		t.Fatalf("default identity: %+v %v", ids, err)
	}
	if _, err := s.Identities(ctx, "bob@x.test"); err != nil {
		t.Fatal(err)
	}
	if ids, _ := s.Identities(ctx, "bob@x.test"); len(ids) != 1 {
		t.Fatalf("default created twice: %d", len(ids))
	}

	sales, err := s.SaveIdentity(ctx, "bob@x.test", Identity{Name: "Отдел продаж", Email: "sales@x.test", Signature: "<p>--</p>", IsDefault: true})
	if err != nil {
		t.Fatal(err)
	}
	ids, _ = s.Identities(ctx, "bob@x.test")
	if len(ids) != 2 || ids[0].ID != sales.ID || ids[1].IsDefault {
		t.Fatalf("default not switched: %+v", ids)
	}

	// Чужой ящик не видит и не меняет отправителей bob.
	if _, err := s.Identity(ctx, "eve@x.test", sales.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-mailbox read: %v", err)
	}
	if _, err := s.SaveIdentity(ctx, "eve@x.test", Identity{ID: sales.ID, Email: "evil@x.test"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-mailbox update: %v", err)
	}
	if err := s.DeleteIdentity(ctx, "eve@x.test", sales.ID); err == nil {
		t.Fatal("cross-mailbox delete allowed")
	}

	if err := s.DeleteIdentity(ctx, "bob@x.test", sales.ID); err != nil {
		t.Fatal(err)
	}
	ids, _ = s.Identities(ctx, "bob@x.test")
	if len(ids) != 1 || !ids[0].IsDefault {
		t.Fatalf("default not restored after delete: %+v", ids)
	}
	if err := s.DeleteIdentity(ctx, "bob@x.test", ids[0].ID); !errors.Is(err, ErrLastOne) {
		t.Fatalf("last identity deleted: %v", err)
	}
}

func TestMigrationsIdempotent(t *testing.T) {
	p := filepath.Join(t.TempDir(), "m.db")
	for i := 0; i < 2; i++ {
		s, err := Open(p)
		if err != nil {
			t.Fatalf("open #%d: %v", i, err)
		}
		s.Close()
	}
}

func TestTemplatesAndPrefs(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	a, err := s.SaveTemplate(ctx, "a@x", Template{Name: " Спасибо ", HTML: "<p>Спасибо!</p>"})
	if err != nil || a.ID == 0 || a.Name != "Спасибо" {
		t.Fatalf("save: %+v %v", a, err)
	}
	s.SaveTemplate(ctx, "a@x", Template{Name: "Адрес", HTML: "<p>ул. Ленина</p>"})
	s.SaveTemplate(ctx, "b@x", Template{Name: "Чужой"})
	list, _ := s.Templates(ctx, "a@x")
	if len(list) != 2 || list[0].Name != "Адрес" {
		t.Fatalf("list: %+v", list)
	}
	if _, err := s.SaveTemplate(ctx, "b@x", Template{ID: a.ID, Name: "взлом"}); err != ErrNotFound {
		t.Errorf("foreign update: %v", err)
	}
	if err := s.DeleteTemplate(ctx, "b@x", a.ID); err != ErrNotFound {
		t.Errorf("foreign delete: %v", err)
	}
	if err := s.DeleteTemplate(ctx, "a@x", a.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPrefs(ctx, "a@x", map[string]string{"k": "v"}); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.Pref(ctx, "a@x", "k"); v != "v" {
		t.Errorf("pref: %q", v)
	}
	s.SetPrefs(ctx, "a@x", map[string]string{"k": ""})
	if p, _ := s.Prefs(ctx, "a@x"); len(p) != 0 {
		t.Errorf("pref not cleared: %v", p)
	}
	s.DeleteMailbox(ctx, "a@x")
	if list, _ := s.Templates(ctx, "a@x"); len(list) != 0 {
		t.Errorf("templates survive DeleteMailbox: %v", list)
	}
}

func TestSpellWords(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	s.AddSpellWord(ctx, "a@x", "Webbeard")
	s.AddSpellWord(ctx, "a@x", "Webbeard")
	s.AddSpellWord(ctx, "a@x", "Ташкентский")
	s.AddSpellWord(ctx, "b@x", "чужое")
	if list, _ := s.SpellWords(ctx, "a@x"); len(list) != 2 || list[0] != "Webbeard" {
		t.Fatalf("list: %v", list)
	}
	s.DeleteSpellWord(ctx, "a@x", "Webbeard")
	if err := s.DeleteMailbox(ctx, "b@x"); err != nil {
		t.Fatal(err)
	}
	a, _ := s.SpellWords(ctx, "a@x")
	b, _ := s.SpellWords(ctx, "b@x")
	if len(a) != 1 || len(b) != 0 {
		t.Fatalf("after delete: %v %v", a, b)
	}
}

func TestAddressBooks(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	a, err := s.AddDavAccount(ctx, "a@x", DavAccount{Name: "Cloud", URL: "https://c/", Secret: []byte{1}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetAccountBooks(ctx, "a@x", a.ID, []RemoteBookInfo{{URL: "https://c/1/", Name: "Один"}, {URL: "https://c/2/", Name: "Два", ReadOnly: true}}); err != nil {
		t.Fatal(err)
	}
	books, _ := s.Books(ctx, "a@x")
	if len(books) != 2 || books[0].Name != "Два" || !books[0].ReadOnly {
		t.Fatalf("books: %+v", books)
	}
	b1 := books[1].ID
	s.UpsertCard(ctx, "a@x", b1, "/1/x.vcf", "e1", []byte("v1"), Contact{Name: "Икс", Emails: []Labeled{{Value: "X@Y.test"}}})
	g, _ := s.SaveGroup(ctx, "a@x", Group{Name: "Г"})
	list, _, _ := s.ContactsIn(ctx, "a@x", "", 0, b1, 10, 0)
	c := list[0]
	c.Groups = []int64{g.ID}
	s.SaveContact(ctx, "a@x", c)
	// Обновление с сервера сохраняет id и группы.
	s.UpsertCard(ctx, "a@x", b1, "/1/x.vcf", "e2", []byte("v2"), Contact{Name: "Икс 2", Emails: []Labeled{{Value: "x@y.test"}}})
	got, _ := s.Contact(ctx, "a@x", c.ID)
	if got.Name != "Икс 2" || got.Book != b1 || len(got.Groups) != 1 {
		t.Fatalf("upsert: %+v", got)
	}
	if refs, _ := s.BookCards(ctx, b1); refs["/1/x.vcf"].ETag != "e2" {
		t.Fatalf("cards: %v", refs)
	}
	if _, n, _ := s.ContactsIn(ctx, "a@x", "", 0, 0, 10, 0); n != 0 {
		t.Fatalf("personal book must be empty: %d", n)
	}
	// Книга пропала на сервере — уходит с карточками.
	s.SetAccountBooks(ctx, "a@x", a.ID, []RemoteBookInfo{{URL: "https://c/2/", Name: "Два"}})
	if _, n, _ := s.Contacts(ctx, "a@x", "", 0, 10, 0); n != 0 {
		t.Fatalf("cards of removed book remain: %d", n)
	}
	s.SetAccountBooks(ctx, "a@x", a.ID, []RemoteBookInfo{{URL: "https://c/1/", Name: "Один"}})
	books, _ = s.Books(ctx, "a@x")
	s.UpsertCard(ctx, "a@x", books[0].ID, "/1/y.vcf", "e", nil, Contact{Name: "Игрек"})
	if err := s.DeleteDavAccount(ctx, "a@x", a.ID); err != nil {
		t.Fatal(err)
	}
	if b, _ := s.Books(ctx, "a@x"); len(b) != 0 {
		t.Fatalf("books after account delete: %v", b)
	}
	if _, n, _ := s.Contacts(ctx, "a@x", "", 0, 10, 0); n != 0 {
		t.Fatalf("cards after account delete: %d", n)
	}
}
