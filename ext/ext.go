// Package ext — публичные точки расширения ядра webmail.
//
// Адаптеры (например, интеграция с панелью хостинга) живут в отдельных
// Go-модулях и импортируют из ядра только пакеты ext и app. Ядро ничего
// не знает о конкретных адаптерах.
//
// Пакет версионируется по semver: несовместимые изменения интерфейсов —
// только в мажорной версии модуля.
package ext

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"
)

// Adapter — подключаемое расширение. Передаётся в app.Run и включается,
// только если в конфиге есть секция adapters.<Name()> с enabled: true.
type Adapter interface {
	Name() string
	// Init получает сырую секцию конфига адаптера (adapters.<name>) и Core.
	// Адаптер сам разбирает свою секцию через cfg.Decode.
	Init(ctx context.Context, cfg ConfigNode, core Core) error
}

// ConfigNode — секция конфига адаптера.
type ConfigNode interface {
	// Decode разбирает секцию в структуру; неизвестные ключи — ошибка.
	Decode(v any) error
}

// Core — то, что ядро предоставляет адаптеру. Секреты и сессии наружу не отдаются.
type Core interface {
	// Handle регистрирует маршрут адаптера, например "GET /sso/webbeard".
	// Маршруты под /api/ и /assets/ зарезервированы ядром.
	Handle(pattern string, h http.Handler)
	// StartSession создаёт сессию по проверенному handoff и ставит cookie.
	StartSession(w http.ResponseWriter, r *http.Request, g SessionGrant) error
	// ClientIP возвращает реальный IP клиента с учётом server.trusted_proxies.
	ClientIP(r *http.Request) string
	// BasePath — путь, под которым прокси публикует webmail для этого запроса
	// ("/webmail"; "" — корень). Ссылки и редиректы адаптера строятся от него.
	BasePath(r *http.Request) string
	// Lang — язык пользователя ("en", "ru"…) для страниц и текстов адаптера:
	// выбранный в интерфейсе, иначе из Accept-Language, иначе ui.default_language.
	Lang(r *http.Request) string
	Logger() *slog.Logger
	// SetPasswordChanger подменяет драйвер смены пароля (password.driver
	// должен указывать на имя, которое регистрирует адаптер).
	SetPasswordChanger(name string, pc PasswordChanger)
	SetBrandProvider(bp BrandProvider)
	// SetContactSource подключает внешние адресные книги (например, CardDAV).
	// Учётки, кэш контактов и синхронизацию ведёт ядро; источник отвечает
	// только за протокол. Один источник на установку.
	SetContactSource(cs ContactSource)
	// SetCalendarSource подключает календари (например, CalDAV). Ядро ничего
	// не кэширует: события запрашиваются у источника за нужный диапазон.
	SetCalendarSource(cs CalendarSource)
	// EndSessions завершает все сессии ящика: например, панель сменила
	// пароль или заблокировала ящик. Возвращает число завершённых сессий.
	EndSessions(mailbox string) int
	// DeleteMailboxData завершает сессии ящика и удаляет всё, что ядро о нём
	// хранит (отправители, контакты, настройки, шаблоны, словарь, учётки
	// внешних книг). Письма на IMAP-сервере не трогает.
	DeleteMailboxData(ctx context.Context, mailbox string) error
	// StaleContactPreset помечает учётки внешнего пресета (например "domain")
	// как устаревшие, чтобы клиент снова синхронизировал книги. domain пуст —
	// все ящики; иначе только *@domain. Возвращает число затронутых учёток.
	StaleContactPreset(ctx context.Context, preset, domain string) (int, error)
}

// AuthMode — способ, которым ядро аутентифицируется в IMAP/SMTP для сессии.
type AuthMode int

const (
	// AuthPassword — пароль пользователя (обычный вход).
	AuthPassword AuthMode = iota
	// AuthImpersonation — мастер-учётка через SASL PLAIN authzid.
	// Разрешено только при imap.impersonation.enabled и Source из allowed_sources.
	AuthImpersonation
)

// SessionGrant — результат проверенного handoff.
type SessionGrant struct {
	Mailbox  string // local@domain; ядро приводит его к виду, как при обычном входе
	AuthMode AuthMode
	Source   string // например "webbeard_sso" — для аудита и allowed_sources
}

// PasswordChange — запрос смены пароля.
type PasswordChange struct {
	Mailbox  string
	Current  string // пусто, если SSOSkip
	New      string
	SSOSkip  bool   // SSO-сессия в окне password.sso_window: текущий не спрашивается
	RemoteIP string // реальный IP пользователя
}

// PasswordPolicy — правила нового пароля.
type PasswordPolicy struct {
	MinLength      int    `json:"min_length"`
	MaxLength      int    `json:"max_length,omitempty"`
	RequireClasses int    `json:"require_classes,omitempty"`
	ForbidChars    string `json:"forbid_chars,omitempty"`
}

// PasswordCaps — возможности драйвера смены пароля.
type PasswordCaps struct {
	VerifiesCurrent   bool            // драйвер сам проверяет текущий пароль
	SSOWithoutCurrent bool            // умеет менять пароль без текущего в SSO-сессии
	Policy            *PasswordPolicy // правила драйвера; ядро применяет более строгие
}

type PasswordChanger interface {
	Capabilities() PasswordCaps
	Change(ctx context.Context, req PasswordChange) error
}

// Brand — оформление: имя, логотипы, favicon, цвет акцента и CSS-переменные темы.
// Провайдер может заполнить только часть полей: пустые берутся из ui.brand конфига,
// CSSVars дополняют конфиговые. Accent и AccentDark — hex-цвета (#rgb, #rrggbb).
type Brand struct {
	Name        string            `json:"name"`
	LogoURL     string            `json:"logo_url,omitempty"`
	LogoDarkURL string            `json:"logo_dark_url,omitempty"`
	FaviconURL  string            `json:"favicon_url,omitempty"`
	Accent      string            `json:"accent,omitempty"`
	AccentDark  string            `json:"accent_dark,omitempty"`
	CSSVars     map[string]string `json:"css_vars,omitempty"`
	// HideAbout убирает кнопку и окно «О программе». Провайдер может только скрыть его.
	HideAbout bool `json:"hide_about,omitempty"`
}

type BrandProvider interface {
	Brand(ctx context.Context, host string) (Brand, error)
}

// Типизированные ошибки смены пароля. Драйверы возвращают их (или оборачивают).
var (
	ErrWrongPassword = errors.New("wrong current password")
	ErrWeakPassword  = errors.New("password does not meet policy")
	ErrRateLimited   = errors.New("too many attempts")
	ErrUnavailable   = errors.New("service unavailable")
	ErrNotSupported  = errors.New("not supported")
)

// --- внешние адресные книги ------------------------------------------------------

// ContactAccount — учётка внешнего сервера контактов.
type ContactAccount struct {
	URL      string // адрес, введённый пользователем или из пресета
	Username string
	Password string
	// Trusted — учётка из конфига администратора (пресет). Для учёток,
	// добавленных пользователем, источник обязан запрещать обращения
	// во внутренние сети (SSRF), если администратор явно не разрешил.
	Trusted bool
}

// ContactPreset — книга, которую администратор подключает всем ящикам.
// Пароль: PasswordSession — пароль ящика из текущей сессии (не сохраняется),
// иначе — заданный в конфиге адаптера.
type ContactPreset struct {
	ID       string // постоянный идентификатор пресета в конфиге
	Name     string
	Account  ContactAccount
	ReadOnly bool
}

// PasswordSession — значение ContactAccount.Password у пресета, которое ядро
// заменяет паролем ящика из сессии.
const PasswordSession = "\x00session"

// RemoteBook — книга на сервере.
type RemoteBook struct {
	URL      string // абсолютный URL коллекции
	Name     string
	ReadOnly bool
}

// RemoteCard — карточка: адрес на сервере, ETag и vCard как есть.
type RemoteCard struct {
	Href  string
	ETag  string
	VCard []byte
}

type ContactSource interface {
	// Presets — книги, подключённые администратором для ящика (могут
	// зависеть от домена). Пусто — пресетов нет.
	Presets(mailbox string) []ContactPreset
	// AllowUserAccounts — может ли пользователь подключать свои серверы.
	AllowUserAccounts() bool
	// Discover находит книги учётки (по адресу сервера, принципала или самой книги).
	Discover(ctx context.Context, acc ContactAccount) ([]RemoteBook, error)
	// List возвращает href → ETag всех карточек книги.
	List(ctx context.Context, acc ContactAccount, book string) (map[string]string, error)
	// Get загружает карточки по href (до 100 за вызов).
	Get(ctx context.Context, acc ContactAccount, book string, hrefs []string) ([]RemoteCard, error)
	// Put создаёт карточку (href == "", тогда источник выбирает имя сам) или
	// заменяет её при совпадении ETag. Возвращает новые href и ETag (ETag может
	// быть пустым, если сервер его не прислал).
	Put(ctx context.Context, acc ContactAccount, book, href, etag string, vcard []byte) (newHref, newETag string, err error)
	// Delete удаляет карточку при совпадении ETag.
	Delete(ctx context.Context, acc ContactAccount, book, href, etag string) error
}

// Ошибки источника контактов.
var (
	ErrContactAuth      = errors.New("contact server rejected credentials")
	ErrContactConflict  = errors.New("contact changed on server")
	ErrContactReadOnly  = errors.New("contact book is read-only")
	ErrContactNotFound  = errors.New("contact book or card not found")
	ErrContactForbidden = errors.New("address is not allowed")
)

// --- календари ------------------------------------------------------------------

// CalendarPreset — календари, которые администратор подключает всем ящикам.
// Account — как у адресных книг (в том числе PasswordSession).
type CalendarPreset struct {
	ID       string
	Name     string
	Account  ContactAccount
	ReadOnly bool
}

// RemoteCalendar — календарь на сервере.
type RemoteCalendar struct {
	URL      string // абсолютный URL коллекции
	Name     string
	Color    string // #rrggbb, если сервер его хранит
	ReadOnly bool
}

// RemoteObject — объект календаря: адрес на сервере, ETag и iCalendar как есть.
type RemoteObject struct {
	Href string
	ETag string
	ICS  []byte
}

// CalendarSource — протокол календарей. Ошибки — те же, что у ContactSource
// (ErrContactAuth, ErrContactConflict, ErrContactReadOnly, ErrContactNotFound,
// ErrContactForbidden).
type CalendarSource interface {
	CalendarPresets(mailbox string) []CalendarPreset
	// DiscoverCalendars находит календари с событиями (VEVENT) по адресу
	// сервера, принципала или самого календаря.
	DiscoverCalendars(ctx context.Context, acc ContactAccount) ([]RemoteCalendar, error)
	// Query — объекты с событиями, которые пересекают [from, to); повторения
	// учитывает сервер.
	Query(ctx context.Context, acc ContactAccount, cal string, from, to time.Time) ([]RemoteObject, error)
	GetObject(ctx context.Context, acc ContactAccount, cal, href string) (RemoteObject, error)
	// FindUID — объект с событием UID; ErrContactNotFound, если его нет.
	FindUID(ctx context.Context, acc ContactAccount, cal, uid string) (RemoteObject, error)
	// PutObject создаёт объект (href == "", имя выбирает источник) или
	// заменяет его при совпадении ETag.
	PutObject(ctx context.Context, acc ContactAccount, cal, href, etag string, ics []byte) (newHref, newETag string, err error)
	DeleteObject(ctx context.Context, acc ContactAccount, cal, href, etag string) error
}
