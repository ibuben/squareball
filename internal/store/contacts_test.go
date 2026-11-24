package store

import (
	"context"
	"testing"
)

func TestContactsCRUDAndSearch(t *testing.T) {
	s, ctx := open(t), context.Background()
	const mb = "bob@x.test"
	g, err := s.SaveGroup(ctx, mb, Group{Name: "Коллеги"})
	if err != nil {
		t.Fatal(err)
	}
	alien, _ := s.SaveGroup(ctx, "eve@x.test", Group{Name: "Чужая"})

	ivan, err := s.SaveContact(ctx, mb, Contact{
		First: "Иван", Last: "Петров", Org: "ООО Ромашка",
		Emails: []Labeled{{Value: " Ivan.Petrov@Example.com ", Label: "work"}, {Value: "ivan.petrov@example.com"}},
		Phones: []Labeled{{Value: "+7 900 000-00-00", Label: "mobile"}},
		Groups: []int64{g.ID, alien.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(ivan.Emails) != 1 || ivan.Emails[0].Value != "ivan.petrov@example.com" {
		t.Errorf("emails not normalized: %+v", ivan.Emails)
	}
	if len(ivan.Groups) != 1 || ivan.Groups[0] != g.ID {
		t.Errorf("foreign group attached: %+v", ivan.Groups)
	}
	if _, err := s.SaveContact(ctx, mb, Contact{Name: "Анна", Emails: []Labeled{{Value: "anna@example.com"}}}); err != nil {
		t.Fatal(err)
	}

	for q, want := range map[string]int{"петров": 1, "ромашка": 1, "example.com": 2, "900": 1, "нет такого": 0, "%": 0} {
		if _, n, err := s.Contacts(ctx, mb, q, 0, 50, 0); err != nil || n != want {
			t.Errorf("search %q: got %d, want %d (%v)", q, n, want, err)
		}
	}
	if list, n, _ := s.Contacts(ctx, mb, "", g.ID, 50, 0); n != 1 || list[0].ID != ivan.ID {
		t.Errorf("group filter: %d %+v", n, list)
	}
	if list, _, _ := s.Contacts(ctx, mb, "", 0, 50, 0); list[0].DisplayName() != "Анна" {
		t.Errorf("sort by name: %s first", list[0].DisplayName())
	}
	if _, n, _ := s.Contacts(ctx, "eve@x.test", "", 0, 50, 0); n != 0 {
		t.Errorf("eve sees bob's contacts: %d", n)
	}
	if _, err := s.Contact(ctx, "eve@x.test", ivan.ID); err != ErrNotFound {
		t.Errorf("cross-mailbox read: %v", err)
	}

	groups, _ := s.Groups(ctx, mb)
	if len(groups) != 1 || groups[0].Count != 1 {
		t.Errorf("groups: %+v", groups)
	}
	if n, _ := s.DeleteContacts(ctx, "eve@x.test", []int64{ivan.ID}); n != 0 {
		t.Error("eve deleted bob's contact")
	}
	if n, _ := s.DeleteContacts(ctx, mb, []int64{ivan.ID}); n != 1 {
		t.Error("delete failed")
	}
	if groups, _ := s.Groups(ctx, mb); groups[0].Count != 0 {
		t.Errorf("membership not cascaded: %+v", groups)
	}
}

func TestImportSkipsDuplicates(t *testing.T) {
	s, ctx := open(t), context.Background()
	const mb = "bob@x.test"
	s.SaveContact(ctx, mb, Contact{Name: "Есть", Emails: []Labeled{{Value: "a@x.test"}}})
	added, skipped, err := s.ImportContacts(ctx, mb, []Contact{
		{Name: "Дубль", Emails: []Labeled{{Value: "A@x.test"}}},
		{Name: "Новый", Emails: []Labeled{{Value: "b@x.test"}}},
		{Name: "Новый ещё раз", Emails: []Labeled{{Value: "b@x.test"}}},
		{},
	})
	if err != nil || added != 1 || skipped != 3 {
		t.Fatalf("added=%d skipped=%d err=%v", added, skipped, err)
	}
}

func TestSuggest(t *testing.T) {
	s, ctx := open(t), context.Background()
	const mb = "bob@x.test"
	g, _ := s.SaveGroup(ctx, mb, Group{Name: "Маркетинг"})
	s.SaveContact(ctx, mb, Contact{First: "Мария", Last: "Иванова", Emails: []Labeled{{Value: "maria@x.test"}}, Groups: []int64{g.ID}})
	s.SaveContact(ctx, mb, Contact{Name: "Анна Мартынова", Emails: []Labeled{{Value: "anna@x.test"}}})
	s.Collect(ctx, mb, []Collected{{Email: "Maria@x.test", Name: "Мария"}, {Email: "mark@y.test"}, {Email: mb}})
	s.Collect(ctx, mb, []Collected{{Email: "mark@y.test", Name: "Марк"}})

	got, err := s.Suggest(ctx, mb, "мар", 10)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	emails := map[string]int{}
	for _, g := range got {
		kinds = append(kinds, g.Kind+":"+g.Name)
		emails[g.Email]++
	}
	if len(got) != 4 {
		t.Fatalf("suggestions: %v", kinds)
	}
	if got[0].Kind != "group" || len(got[0].Members) != 1 || got[0].Members[0].Email != "maria@x.test" {
		t.Errorf("group should come first with members: %+v", got[0])
	}
	if emails["maria@x.test"] != 1 {
		t.Errorf("collected duplicate of a contact: %v", kinds)
	}
	if got[len(got)-1].Name != "Анна Мартынова" {
		t.Errorf("mid-word match should rank last: %v", kinds)
	}
	list, _ := s.CollectedList(ctx, mb, "", 10)
	for _, c := range list {
		if c.Email == mb {
			t.Error("own mailbox collected")
		}
		if c.Email == "mark@y.test" && (c.Uses != 2 || c.Name != "Марк") {
			t.Errorf("collected upsert: %+v", c)
		}
	}
}
