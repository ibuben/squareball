// Package password — встроенные драйверы смены пароля (exec, http),
// проверка политики нового пароля и помощник для Dovecot passwd-file.
package password

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ibuben/squareball/ext"
	"github.com/ibuben/squareball/internal/config"
)

// Policy — итоговые правила: из конфига ядра и более строгие правила драйвера.
type Policy struct {
	MinLength      int    `json:"min_length"`
	MaxLength      int    `json:"max_length"`
	RequireClasses int    `json:"require_classes"`
	ForbidChars    string `json:"forbid_chars,omitempty"`
	ForbidMailbox  bool   `json:"forbid_mailbox"`
}

// FromConfig собирает политику ядра и накладывает на неё правила драйвера:
// из двух значений берётся более строгое.
func FromConfig(c *config.Password, drv *ext.PasswordPolicy) Policy {
	p := Policy{
		MinLength: c.Policy.MinLength, MaxLength: c.Policy.MaxLength,
		RequireClasses: c.Policy.RequireClasses, ForbidMailbox: c.Policy.ForbidMailbox,
	}
	if drv == nil {
		return p
	}
	p.MinLength = max(p.MinLength, drv.MinLength)
	if drv.MaxLength > 0 && drv.MaxLength < p.MaxLength {
		p.MaxLength = drv.MaxLength
	}
	p.RequireClasses = max(p.RequireClasses, drv.RequireClasses)
	p.ForbidChars = drv.ForbidChars
	return p
}

// Violation — причина, по которой пароль не подходит. Reason уходит в API
// как detail ошибки password.weak, чтобы интерфейс показал понятный текст.
type Violation struct{ Reason string }

func (v *Violation) Error() string { return "password policy: " + v.Reason }

// Unwrap позволяет проверять errors.Is(err, ext.ErrWeakPassword).
func (v *Violation) Unwrap() error { return ext.ErrWeakPassword }

// Check проверяет новый пароль. Управляющие символы (переводы строк и т. п.)
// запрещены всегда: они ломают построчные форматы и протоколы бэкендов.
func (p Policy) Check(mailbox, pw string) error {
	if !utf8.ValidString(pw) {
		return &Violation{"chars"}
	}
	n := utf8.RuneCountInString(pw)
	switch {
	case n < p.MinLength:
		return &Violation{"too_short"}
	case p.MaxLength > 0 && n > p.MaxLength:
		return &Violation{"too_long"}
	}
	var lower, upper, digit, other bool
	for _, r := range pw {
		switch {
		case unicode.IsControl(r):
			return &Violation{"chars"}
		case strings.ContainsRune(p.ForbidChars, r):
			return &Violation{"chars"}
		case unicode.IsLower(r):
			lower = true
		case unicode.IsUpper(r):
			upper = true
		case unicode.IsDigit(r):
			digit = true
		default:
			other = true
		}
	}
	classes := 0
	for _, b := range []bool{lower, upper, digit, other} {
		if b {
			classes++
		}
	}
	if classes < p.RequireClasses {
		return &Violation{"classes"}
	}
	if p.ForbidMailbox {
		local, _, _ := strings.Cut(strings.ToLower(mailbox), "@")
		if len(local) >= 3 && strings.Contains(strings.ToLower(pw), local) {
			return &Violation{"mailbox"}
		}
	}
	return nil
}
