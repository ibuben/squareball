// Package locale — языки интерфейса и текстов, которые формирует сервер.
//
// Встроенные языки — en и ru: их строки интерфейса собраны в web/src/locales,
// серверные — здесь. Остальные языки подключаются языковыми пакетами: файлами
// <код>.json в каталоге ui.locales_dir. Пакет устанавливается отдельно (например,
// вместе с переводом панели) и не требует новой сборки webmail:
//
//	{
//	  "name": "O'zbekcha",          // название на самом языке — для меню выбора
//	  "date_locale": "uz-Latn-UZ",  // BCP 47 для дат и чисел; по умолчанию — код
//	  "ui": {"login.title": "…"},   // ключи web/src/locales/en.ts
//	  "server": {"mdn.subject": "…"} // ключи Server["en"] ниже
//	}
//
// Непереведённая строка берётся из английского. Пакет может и заменить
// встроенный язык — его строки тогда идут поверх встроенных.
package locale

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// Builtin — языки, встроенные в сборку, в порядке показа.
var Builtin = []string{"en", "ru"}

var builtinNames = map[string]string{"en": "English", "ru": "Русский"}

// Pack — языковой пакет.
type Pack struct {
	Code       string            `json:"-"`
	Name       string            `json:"name"`
	DateLocale string            `json:"date_locale,omitempty"`
	UI         map[string]string `json:"ui"`
	Server     map[string]string `json:"server"`
}

// CodeRE — код языка: "uz", "pt-br", "uz-cyrl".
var CodeRE = regexp.MustCompile(`^[a-z]{2,3}(-[a-z0-9]{2,8})?$`)

var (
	mu    sync.RWMutex
	packs = map[string]*Pack{}
)

// Register добавляет пакет (заменяет пакет с тем же кодом).
func Register(p *Pack) error {
	if !CodeRE.MatchString(p.Code) {
		return fmt.Errorf("locale %q: bad code", p.Code)
	}
	if strings.TrimSpace(p.Name) == "" {
		if n, ok := builtinNames[p.Code]; ok {
			p.Name = n
		} else {
			return fmt.Errorf("locale %s: name is required", p.Code)
		}
	}
	mu.Lock()
	packs[p.Code] = p
	mu.Unlock()
	return nil
}

// Reset убирает все пакеты (для тестов).
func Reset() {
	mu.Lock()
	packs = map[string]*Pack{}
	mu.Unlock()
}

// Load читает пакеты <код>.json из каталога. Нет каталога — не ошибка.
// Битый файл не мешает остальным: ошибки возвращаются вместе с кодами загруженных.
func Load(dir string) ([]string, error) {
	if dir == "" {
		return nil, nil
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	var loaded []string
	var errs []error
	for _, f := range files {
		code := strings.ToLower(strings.TrimSuffix(filepath.Base(f), ".json"))
		b, err := os.ReadFile(f)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		p := &Pack{}
		if err := json.Unmarshal(b, p); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", f, err))
			continue
		}
		p.Code = code
		if err := Register(p); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", f, err))
			continue
		}
		loaded = append(loaded, code)
	}
	return loaded, errors.Join(errs...)
}

func pack(lang string) *Pack {
	mu.RLock()
	defer mu.RUnlock()
	return packs[lang]
}

// Known — есть ли язык: встроенный или из пакета.
func Known(lang string) bool {
	_, ok := builtinNames[lang]
	return ok || pack(lang) != nil
}

// All — все доступные языки: встроенные, затем пакеты по коду.
func All() []string {
	out := append([]string{}, Builtin...)
	mu.RLock()
	var extra []string
	for c := range packs {
		if _, ok := builtinNames[c]; !ok {
			extra = append(extra, c)
		}
	}
	mu.RUnlock()
	sort.Strings(extra)
	return append(out, extra...)
}

// Name — название языка на нём самом.
func Name(lang string) string {
	if p := pack(lang); p != nil {
		return p.Name
	}
	if n, ok := builtinNames[lang]; ok {
		return n
	}
	return lang
}

// DateLocale — локаль для дат и чисел в интерфейсе ("" — по коду языка).
func DateLocale(lang string) string {
	if p := pack(lang); p != nil {
		return p.DateLocale
	}
	return ""
}

// UI — строки интерфейса из пакета (nil — языка нет в пакетах; встроенные — в сборке).
func UI(lang string) map[string]string {
	if p := pack(lang); p != nil {
		return p.UI
	}
	return nil
}

// S — серверная строка: пакет → встроенный язык → английский.
func S(lang, key string) string {
	if p := pack(lang); p != nil {
		if s, ok := p.Server[key]; ok && s != "" {
			return s
		}
	}
	if s, ok := Server[lang][key]; ok {
		return s
	}
	return Server["en"][key]
}

// Server — встроенные серверные строки. Ключи "en" — эталон для пакетов.
var Server = map[string]map[string]string{
	"en": {
		// Уведомление о прочтении: тема, «отправлено», текст (%s: когда отправлено, кому, тема, когда показано).
		"mdn.subject":     "Read: ",
		"mdn.sent_on":     " sent on ",
		"mdn.body":        "Your message%s\r\n\r\n  To: %s\r\n  Subject: %s\r\n\r\nwas displayed to the recipient on %s.\r\nThis does not guarantee that the message has been read or understood.\r\n",
		"mdn.date_format": "2 Jan 2006 15:04",
		"mdn.now_format":  "2 Jan 2006 15:04 -0700",

		// Ответ на приглашение: тема и текст (%[1]s — кто, %[2]s — встреча).
		"invite.reply.accepted":       "Accepted",
		"invite.reply.tentative":      "Tentative",
		"invite.reply.declined":       "Declined",
		"invite.reply.body.accepted":  "%[1]s has accepted the invitation “%[2]s”.\r\n",
		"invite.reply.body.tentative": "%[1]s has tentatively accepted the invitation “%[2]s”.\r\n",
		"invite.reply.body.declined":  "%[1]s has declined the invitation “%[2]s”.\r\n",

		// Приглашение участникам.
		"invite.subject.new":    "Invitation",
		"invite.subject.update": "Updated invitation",
		"invite.subject.cancel": "Cancelled",
		"invite.when":           "When",
		"invite.where":          "Where",
		"invite.organizer":      "Organizer",
		"invite.attendees":      "Attendees",
		"invite.cancel":         "This meeting has been cancelled.",
		"invite.cancel_one":     "This occurrence of the meeting has been cancelled.",
		"invite.untitled":       "(untitled)",

		// Фильтры Sieve: имена служебных правил и шапка скрипта.
		"sieve.vacation": "Vacation",
		"sieve.forward":  "Forwarding",
		"sieve.header":   "# Mail filters. Rules in Roundcube managesieve format (# rule:[...]).\n",

		// Страница ошибки входа из панели.
		"sso.title":       "Mail sign-in",
		"sso.bad_link":    "The sign-in link is invalid or has expired. Open your mail from the control panel again.",
		"sso.unavailable": "Could not open the mailbox. Try again later or contact support.",
		"sso.password":    "Sign in with password",
	},
	"ru": {
		"mdn.subject":     "Прочитано: ",
		"mdn.sent_on":     " от ",
		"mdn.body":        "Ваше письмо%s\r\n\r\n  Кому: %s\r\n  Тема: %s\r\n\r\nбыло показано получателю %s.\r\nЭто не гарантирует, что письмо прочитано и понято.\r\n",
		"mdn.date_format": "02.01.2006 15:04",
		"mdn.now_format":  "02.01.2006 15:04 -0700",

		"invite.reply.accepted":       "Принято",
		"invite.reply.tentative":      "Под вопросом",
		"invite.reply.declined":       "Отклонено",
		"invite.reply.body.accepted":  "%[1]s принимает приглашение «%[2]s».\r\n",
		"invite.reply.body.tentative": "%[1]s под вопросом принимает приглашение «%[2]s».\r\n",
		"invite.reply.body.declined":  "%[1]s отклоняет приглашение «%[2]s».\r\n",

		"invite.subject.new":    "Приглашение",
		"invite.subject.update": "Изменено приглашение",
		"invite.subject.cancel": "Отменено",
		"invite.when":           "Когда",
		"invite.where":          "Где",
		"invite.organizer":      "Организатор",
		"invite.attendees":      "Участники",
		"invite.cancel":         "Встреча отменена.",
		"invite.cancel_one":     "Это повторение встречи отменено.",
		"invite.untitled":       "(без названия)",

		"sieve.vacation": "Автоответ",
		"sieve.forward":  "Пересылка",
		"sieve.header":   "# Фильтры почты. Правила в формате Roundcube managesieve (# rule:[...]).\n",

		"sso.title":       "Вход в почту",
		"sso.bad_link":    "Ссылка для входа недействительна или устарела. Откройте почту из панели ещё раз.",
		"sso.unavailable": "Не удалось открыть почтовый ящик. Попробуйте позже или обратитесь в поддержку.",
		"sso.password":    "Войти по паролю",
	},
}
