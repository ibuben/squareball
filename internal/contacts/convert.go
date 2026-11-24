// Package contacts — импорт и экспорт адресной книги: vCard (3.0/4.0) и CSV
// в вариантах Outlook, Gmail, Thunderbird.
package contacts

import (
	"bufio"
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/emersion/go-vcard"
	"golang.org/x/text/encoding/charmap"

	"github.com/ibuben/squareball/internal/store"
)

// MaxImport — предел числа контактов в одном файле.
const MaxImport = 5000

var ErrFormat = errors.New("unrecognized contacts file")

// Parse определяет формат по содержимому и разбирает файл.
func Parse(data []byte) ([]store.Contact, error) {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	if !utf8.Valid(data) {
		// Русский Outlook сохраняет CSV в Windows-1251.
		dec, err := charmap.Windows1251.NewDecoder().Bytes(data)
		if err != nil {
			return nil, ErrFormat
		}
		data = dec
	}
	head := strings.ToUpper(strings.TrimSpace(string(data[:min(len(data), 64)])))
	if strings.HasPrefix(head, "BEGIN:VCARD") {
		return ParseVCards(bytes.NewReader(data))
	}
	return ParseCSV(data)
}

// --- vCard -------------------------------------------------------------------------

func label(types []string) string {
	for _, t := range types {
		switch strings.ToLower(t) {
		case "work":
			return "work"
		case "home":
			return "home"
		case "cell", "mobile":
			return "mobile"
		}
	}
	return ""
}

func ParseVCards(r io.Reader) ([]store.Contact, error) {
	dec := vcard.NewDecoder(r)
	var out []store.Contact
	for {
		card, err := dec.Decode()
		if err == io.EOF {
			break
		}
		if err != nil {
			return out, fmt.Errorf("%w: %v", ErrFormat, err)
		}
		if len(out) >= MaxImport {
			return out, fmt.Errorf("more than %d contacts in file", MaxImport)
		}
		c := store.Contact{Name: card.Value(vcard.FieldFormattedName), Title: card.Value(vcard.FieldTitle), Notes: card.Value(vcard.FieldNote)}
		if n := card.Name(); n != nil {
			c.First, c.Last = n.GivenName, n.FamilyName
		}
		if org := card.Value(vcard.FieldOrganization); org != "" {
			c.Org = strings.TrimRight(strings.SplitN(org, ";", 2)[0], " ")
		}
		for _, f := range card[vcard.FieldEmail] {
			c.Emails = append(c.Emails, store.Labeled{Value: f.Value, Label: label(f.Params.Types())})
		}
		for _, f := range card[vcard.FieldTelephone] {
			c.Phones = append(c.Phones, store.Labeled{Value: strings.TrimPrefix(f.Value, "tel:"), Label: label(f.Params.Types())})
		}
		out = append(out, c)
	}
	return out, nil
}

func WriteVCards(w io.Writer, list []store.Contact) error {
	enc := vcard.NewEncoder(w)
	for _, c := range list {
		card := vcard.Card{}
		card.SetValue(vcard.FieldVersion, "3.0")
		card.SetValue(vcard.FieldFormattedName, c.DisplayName())
		card.SetName(&vcard.Name{GivenName: c.First, FamilyName: c.Last})
		for _, e := range c.Emails {
			f := &vcard.Field{Value: e.Value, Params: vcard.Params{}}
			if e.Label != "" && e.Label != "other" {
				f.Params.Set(vcard.ParamType, e.Label)
			}
			card.Add(vcard.FieldEmail, f)
		}
		for _, p := range c.Phones {
			f := &vcard.Field{Value: p.Value, Params: vcard.Params{}}
			switch p.Label {
			case "mobile":
				f.Params.Set(vcard.ParamType, "cell")
			case "work", "home":
				f.Params.Set(vcard.ParamType, p.Label)
			}
			card.Add(vcard.FieldTelephone, f)
		}
		if c.Org != "" {
			card.SetValue(vcard.FieldOrganization, c.Org)
		}
		if c.Title != "" {
			card.SetValue(vcard.FieldTitle, c.Title)
		}
		if c.Notes != "" {
			card.SetValue(vcard.FieldNote, c.Notes)
		}
		if err := enc.Encode(card); err != nil {
			return err
		}
	}
	return nil
}

// --- CSV ---------------------------------------------------------------------------

// Колонки известных форматов (Outlook en/ru, Gmail, Thunderbird). Регистр не важен.
var csvColumns = map[string][]string{
	"name":   {"name", "display name", "full name", "отображаемое имя", "полное имя"},
	"first":  {"first name", "given name", "имя"},
	"last":   {"last name", "family name", "surname", "фамилия"},
	"org":    {"company", "organization", "organization 1 - name", "организация", "компания"},
	"title":  {"job title", "title", "organization 1 - title", "должность"},
	"notes":  {"notes", "note", "заметки", "примечания"},
	"email":  {"e-mail address", "email", "e-mail", "email address", "primary email", "e-mail 1 - value", "адрес эл. почты", "электронная почта"},
	"email2": {"e-mail 2 address", "e-mail 2 - value", "secondary email", "адрес эл. почты 2"},
	"email3": {"e-mail 3 address", "e-mail 3 - value"},
	"mobile": {"mobile phone", "mobile number", "phone 1 - value", "мобильный телефон"},
	"work":   {"business phone", "work phone", "phone 2 - value", "рабочий телефон"},
	"home":   {"home phone", "phone 3 - value", "домашний телефон"},
	"phone":  {"phone", "primary phone", "телефон"},
}

func ParseCSV(data []byte) ([]store.Contact, error) {
	firstLine, _, _ := bufio.NewReader(bytes.NewReader(data)).ReadLine()
	comma := ','
	if strings.Count(string(firstLine), ";") > strings.Count(string(firstLine), ",") {
		comma = ';'
	}
	r := csv.NewReader(bytes.NewReader(data))
	r.Comma = comma
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	header, err := r.Read()
	if err != nil {
		return nil, ErrFormat
	}
	col := map[string]int{}
	for i, h := range header {
		h = strings.ToLower(strings.TrimSpace(h))
		for key, names := range csvColumns {
			for _, n := range names {
				if h == n {
					if _, dup := col[key]; !dup {
						col[key] = i
					}
				}
			}
		}
	}
	_, hasEmail := col["email"]
	_, hasName := col["name"]
	_, hasFirst := col["first"]
	if !hasEmail && !hasName && !hasFirst {
		return nil, fmt.Errorf("%w: no name or e-mail column in CSV header", ErrFormat)
	}
	var out []store.Contact
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return out, fmt.Errorf("%w: %v", ErrFormat, err)
		}
		if len(out) >= MaxImport {
			return out, fmt.Errorf("more than %d contacts in file", MaxImport)
		}
		get := func(k string) string {
			if i, ok := col[k]; ok && i < len(rec) {
				return strings.TrimSpace(rec[i])
			}
			return ""
		}
		c := store.Contact{Name: get("name"), First: get("first"), Last: get("last"), Org: get("org"), Title: get("title"), Notes: get("notes")}
		for _, k := range []string{"email", "email2", "email3"} {
			// Gmail кладёт несколько адресов в одну ячейку: "a@x ::: b@y".
			for _, e := range strings.Split(get(k), ":::") {
				if e = strings.TrimSpace(e); strings.Contains(e, "@") {
					c.Emails = append(c.Emails, store.Labeled{Value: e})
				}
			}
		}
		for _, k := range []string{"mobile", "work", "home", "phone"} {
			if v := get(k); v != "" {
				lbl := k
				if k == "phone" {
					lbl = ""
				}
				c.Phones = append(c.Phones, store.Labeled{Value: v, Label: lbl})
			}
		}
		out = append(out, c)
	}
	return out, nil
}

// WriteCSV — формат Outlook (его понимают Gmail, Thunderbird и Excel); с BOM,
// чтобы Excel правильно показал кириллицу.
func WriteCSV(w io.Writer, list []store.Contact) error {
	if _, err := w.Write([]byte("\xef\xbb\xbf")); err != nil {
		return err
	}
	cw := csv.NewWriter(w)
	cw.Write([]string{"First Name", "Last Name", "Display Name", "E-mail Address", "E-mail 2 Address", "E-mail 3 Address",
		"Mobile Phone", "Business Phone", "Home Phone", "Company", "Job Title", "Notes"})
	for _, c := range list {
		email := func(i int) string {
			if i < len(c.Emails) {
				return c.Emails[i].Value
			}
			return ""
		}
		phone := func(lbl string) string {
			for _, p := range c.Phones {
				if p.Label == lbl || (lbl == "mobile" && p.Label == "") {
					return p.Value
				}
			}
			return ""
		}
		cw.Write([]string{c.First, c.Last, c.DisplayName(), email(0), email(1), email(2),
			phone("mobile"), phone("work"), phone("home"), c.Org, c.Title, c.Notes})
	}
	cw.Flush()
	return cw.Error()
}
