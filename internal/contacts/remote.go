package contacts

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/emersion/go-vcard"

	"github.com/ibuben/squareball/internal/store"
)

// MaxCardSize — предел одной карточки внешней книги (с фото).
const MaxCardSize = 1 << 20

// FromVCard разбирает одну карточку внешней книги.
func FromVCard(raw []byte) (store.Contact, error) {
	if len(raw) > MaxCardSize {
		return store.Contact{}, errors.New("vcard too large")
	}
	list, err := ParseVCards(bytes.NewReader(raw))
	if err != nil {
		return store.Contact{}, err
	}
	if len(list) != 1 {
		return store.Contact{}, ErrFormat
	}
	return list[0], nil
}

// IsGroupCard — карточка-группа (vCard 4 KIND:group или Apple
// X-ADDRESSBOOKSERVER-KIND:group): в списке контактов её не показываем.
func IsGroupCard(raw []byte) bool {
	card, err := vcard.NewDecoder(bytes.NewReader(raw)).Decode()
	if err != nil {
		return false
	}
	return strings.EqualFold(card.Value(vcard.FieldKind), "group") ||
		strings.EqualFold(card.Value("X-ADDRESSBOOKSERVER-KIND"), "group")
}

// MergeVCard переносит поля формы в исходную карточку: имя, организацию,
// должность, заметку, адреса и телефоны. Остальные свойства (фото, адреса,
// дни рождения, поля других клиентов) остаются как были. raw == nil — новая карточка.
func MergeVCard(raw []byte, c store.Contact) ([]byte, error) {
	card := vcard.Card{}
	if len(raw) > 0 {
		var err error
		if card, err = vcard.NewDecoder(bytes.NewReader(raw)).Decode(); err != nil {
			return nil, err
		}
	}
	if card.Value(vcard.FieldVersion) == "" {
		card.SetValue(vcard.FieldVersion, "3.0")
	}
	if card.Value(vcard.FieldUID) == "" {
		b := make([]byte, 16)
		_, _ = rand.Read(b)
		card.SetValue(vcard.FieldUID, hex.EncodeToString(b))
	}
	card.SetValue(vcard.FieldFormattedName, c.DisplayName())
	n := card.Name()
	if n == nil {
		n = &vcard.Name{}
	}
	n.GivenName, n.FamilyName = c.First, c.Last
	card.SetName(n)
	setOrDel := func(k, v string) {
		if v == "" {
			delete(card, k)
		} else {
			card.SetValue(k, v)
		}
	}
	// ORG: подразделения после первого компонента сохраняем.
	org := c.Org
	if old := card.Value(vcard.FieldOrganization); org != "" && old != "" {
		if i := strings.Index(old, ";"); i >= 0 {
			org += old[i:]
		}
	}
	setOrDel(vcard.FieldOrganization, org)
	setOrDel(vcard.FieldTitle, c.Title)
	setOrDel(vcard.FieldNote, c.Notes)

	// Адреса и телефоны: у совпавших значений сохраняем параметры (PREF и т. п.),
	// метку берём из формы.
	relabel := func(k string, list []store.Labeled, typeOf func(string) string, norm func(string) string) {
		old := map[string]*vcard.Field{}
		for _, f := range card[k] {
			old[norm(f.Value)] = f
		}
		var out []*vcard.Field
		for _, l := range list {
			f := old[norm(l.Value)]
			if f == nil {
				f = &vcard.Field{Value: l.Value}
			}
			if f.Params == nil {
				f.Params = vcard.Params{}
			}
			types := []string{}
			for _, t := range f.Params.Types() {
				switch strings.ToLower(t) {
				case "work", "home", "cell", "mobile", "other":
				default:
					types = append(types, t)
				}
			}
			if t := typeOf(l.Label); t != "" {
				types = append(types, t)
			}
			delete(f.Params, vcard.ParamType)
			if len(types) > 0 {
				f.Params[vcard.ParamType] = types
			}
			out = append(out, f)
		}
		if len(out) == 0 {
			delete(card, k)
		} else {
			card[k] = out
		}
	}
	plain := func(l string) string {
		if l == "work" || l == "home" {
			return l
		}
		return ""
	}
	relabel(vcard.FieldEmail, c.Emails, plain, strings.ToLower)
	relabel(vcard.FieldTelephone, c.Phones, func(l string) string {
		if l == "mobile" {
			return "cell"
		}
		return plain(l)
	}, func(v string) string { return strings.TrimPrefix(v, "tel:") })
	card.SetValue(vcard.FieldRevision, time.Now().UTC().Format("20060102T150405Z"))

	var buf bytes.Buffer
	if err := vcard.NewEncoder(&buf).Encode(card); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
