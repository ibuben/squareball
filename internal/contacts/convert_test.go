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
