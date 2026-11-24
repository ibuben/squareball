package contacts

import (
	"bytes"
	"strings"
	"testing"

	"golang.org/x/text/encoding/charmap"

	"github.com/ibuben/squareball/internal/store"
)

func TestVCardRoundTrip(t *testing.T) {
	in := []store.Contact{{
		First: "Иван", Last: "Петров", Org: "ООО «Ромашка»", Title: "Директор", Notes: "строка 1\nстрока 2; с точкой, запятой",
		Emails: []store.Labeled{{Value: "ivan@x.test", Label: "work"}, {Value: "i.petrov@y.test", Label: "home"}},
		Phones: []store.Labeled{{Value: "+7 900 000-00-00", Label: "mobile"}},
	}}
	var buf bytes.Buffer
	if err := WriteVCards(&buf, in); err != nil {
		t.Fatal(err)
	}
	out, err := Parse(buf.Bytes())
	if err != nil || len(out) != 1 {
		t.Fatalf("%v %+v\n%s", err, out, buf.String())
	}
	c := out[0]
	if c.First != "Иван" || c.Last != "Петров" || c.Org != "ООО «Ромашка»" || c.Notes != in[0].Notes ||
		len(c.Emails) != 2 || c.Emails[0].Label != "work" || c.Phones[0].Label != "mobile" {
		t.Fatalf("round trip: %+v\n%s", c, buf.String())
	}
}

// Экспорт из Google Контактов (vCard 3.0) и Apple (с item-группами).
func TestVCardThirdParty(t *testing.T) {
	src := "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Anna Smith\r\nN:Smith;Anna;;;\r\nitem1.EMAIL;TYPE=INTERNET:anna@x.test\r\n" +
		"TEL;TYPE=CELL:+1 555 0100\r\nORG:Acme;Sales\r\nEND:VCARD\r\n"
	out, err := Parse([]byte(src))
	if err != nil || len(out) != 1 || out[0].Emails[0].Value != "anna@x.test" || out[0].Org != "Acme" || out[0].Phones[0].Label != "mobile" {
		t.Fatalf("%v %+v", err, out)
	}
}

func TestCSVFormats(t *testing.T) {
	cases := map[string]struct {
		data  []byte
		first string
		mails int
	}{
		"outlook en": {[]byte("First Name,Last Name,E-mail Address,E-mail 2 Address,Mobile Phone,Company\nJohn,Doe,john@x.test,jd@y.test,+1 555,Acme\n"), "John", 2},
		"gmail":      {[]byte("Name,Given Name,Family Name,E-mail 1 - Value,Phone 1 - Value\nJane Roe,Jane,Roe,jane@x.test ::: j.roe@y.test,+1 556\n"), "Jane", 2},
		"outlook ru cp1251 ;": {func() []byte {
			b, _ := charmap.Windows1251.NewEncoder().Bytes([]byte("Имя;Фамилия;Адрес эл. почты;Мобильный телефон\nПётр;Сидоров;petr@x.test;+7 901\n"))
			return b
		}(), "Пётр", 1},
		"bom + quotes": {[]byte("\xef\xbb\xbfDisplay Name,Email,Notes\n\"Smith, Bob\",bob@x.test,\"multi\nline\"\n"), "", 1},
	}
	for name, tc := range cases {
		out, err := Parse(tc.data)
		if err != nil || len(out) != 1 {
			t.Errorf("%s: %v %+v", name, err, out)
			continue
		}
		if out[0].First != tc.first || len(out[0].Emails) != tc.mails {
			t.Errorf("%s: %+v", name, out[0])
		}
	}
	if _, err := Parse([]byte("foo,bar\n1,2\n")); err == nil {
		t.Error("CSV without name/email columns accepted")
	}
}

func TestCSVExportReimports(t *testing.T) {
	in := []store.Contact{{Name: "Смирнова, Ольга", First: "Ольга", Last: "Смирнова", Emails: []store.Labeled{{Value: "olga@x.test"}},
		Phones: []store.Labeled{{Value: "+7 902", Label: "work"}}, Notes: "с \"кавычками\""}}
	var buf bytes.Buffer
	if err := WriteCSV(&buf, in); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(buf.String(), "\xef\xbb\xbfFirst Name,") {
		t.Fatal("no BOM or wrong header")
	}
	out, err := Parse(buf.Bytes())
	if err != nil || len(out) != 1 || out[0].Name != "Смирнова, Ольга" || out[0].Phones[0].Label != "work" || out[0].Notes != in[0].Notes {
		t.Fatalf("%v %+v", err, out)
	}
}

func TestMergeVCardKeepsUnknownFields(t *testing.T) {
	raw := "BEGIN:VCARD\r\nVERSION:4.0\r\nUID:u1\r\nFN:Old\r\nN:Old;Name;Mid;;\r\nORG:Acme;Sales\r\n" +
		"EMAIL;TYPE=work;PREF=1:a@x.test\r\nEMAIL:gone@x.test\r\nTEL;TYPE=cell:+1 555\r\nADR:;;Street 1;City;;;\r\nBDAY:19900101\r\nX-CUSTOM:keep\r\nEND:VCARD\r\n"
	c := store.Contact{First: "Иван", Last: "Петров", Org: "Новая", Emails: []store.Labeled{{Value: "a@x.test", Label: "home"}, {Value: "new@x.test"}},
		Phones: []store.Labeled{{Value: "+1 555", Label: "mobile"}}}
	out, err := MergeVCard([]byte(raw), c)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{"VERSION:4.0", "UID:u1", "FN:Иван Петров", "N:Петров;Иван;Mid;;", "ORG:Новая;Sales", "PREF=1",
		"TYPE=home", "new@x.test", "TEL;TYPE=cell:+1 555", "ADR:;;Street 1;City;;;", "BDAY:19900101", "X-CUSTOM:keep", "REV:"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in\n%s", want, s)
		}
	}
	if strings.Contains(s, "gone@x.test") || strings.Contains(s, "TYPE=work") {
		t.Errorf("removed values remain:\n%s", s)
	}
	back, err := FromVCard(out)
	if err != nil || back.First != "Иван" || len(back.Emails) != 2 {
		t.Fatalf("round trip: %+v %v", back, err)
	}
	// Новая карточка получает UID и версию.
	fresh, _ := MergeVCard(nil, store.Contact{Name: "Новый"})
	if !strings.Contains(string(fresh), "UID:") || !strings.Contains(string(fresh), "VERSION:3.0") {
		t.Fatalf("new card:\n%s", fresh)
	}
	if !IsGroupCard([]byte("BEGIN:VCARD\r\nVERSION:4.0\r\nKIND:group\r\nFN:G\r\nEND:VCARD\r\n")) || IsGroupCard(fresh) {
		t.Fatal("IsGroupCard")
	}
}
