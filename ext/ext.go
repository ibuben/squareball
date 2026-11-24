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
	Logger() *slog.Logger
	// SetPasswordChanger подменяет драйвер смены пароля (password.driver
	// должен указывать на имя, которое регистрирует адаптер).
	SetPasswordChanger(name string, pc PasswordChanger)
	SetBrandProvider(bp BrandProvider)
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
	Mailbox  string // local@domain
	AuthMode AuthMode
	Source   string // например "webbeard_sso" — для аудита и allowed_sources
}

// PasswordChange — запрос смены пароля.
type PasswordChange struct {
	Mailbox  string
	Current  string // пусто, если SSOSkip
	New      string
	SSOSkip  bool   // SSO-сессия в окне password_window: текущий не проверяется
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

// Brand — оформление: имя, логотип и CSS-переменные темы.
type Brand struct {
	Name    string            `json:"name"`
	LogoURL string            `json:"logo_url,omitempty"`
	CSSVars map[string]string `json:"css_vars,omitempty"`
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
