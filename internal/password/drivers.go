package password

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/ibuben/squareball/ext"
	"github.com/ibuben/squareball/internal/config"
)

// Request — тело запроса к внешнему драйверу (stdin для exec, JSON для http).
// Поля совпадают с контрактом хука панели: user, current, password, sso, remote.
type Request struct {
	User     string `json:"user"`
	Current  string `json:"current,omitempty"`
	Password string `json:"password"`
	SSO      bool   `json:"sso,omitempty"`
	Remote   string `json:"remote,omitempty"`
}

func newRequest(r ext.PasswordChange) Request {
	return Request{User: r.Mailbox, Current: r.Current, Password: r.New, SSO: r.SSOSkip, Remote: r.RemoteIP}
}

// Коды выхода команды exec.
const (
	ExitOK           = 0
	ExitWrongCurrent = 2
	ExitWeak         = 3
	ExitRateLimited  = 4
)

// New создаёт встроенный драйвер по password.driver. Для none и имён
// адаптеров возвращает nil: такие драйверы регистрирует адаптер.
func New(cfg *config.Password, log *slog.Logger) (ext.PasswordChanger, error) {
	switch cfg.Driver {
	case "exec":
		return &Exec{cfg: cfg.Exec, log: log}, nil
	case "http":
		return NewHTTP(cfg.HTTP, log)
	}
	return nil, nil
}

// Exec запускает внешнюю команду. Пароли передаются только через stdin —
// не в argv и не в окружении, где их видно в /proc.
type Exec struct {
	cfg config.PasswordExec
	log *slog.Logger
	mu  sync.Mutex // смены идут по одной: помощникам не нужно думать о гонках
}

func (e *Exec) Capabilities() ext.PasswordCaps {
	return ext.PasswordCaps{VerifiesCurrent: e.cfg.VerifiesCurrent}
}

func (e *Exec) Change(ctx context.Context, r ext.PasswordChange) error {
	body, err := json.Marshal(newRequest(r))
	if err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, e.cfg.Timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, e.cfg.Command[0], e.cfg.Command[1:]...)
	cmd.Env = []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C.UTF-8"}
	cmd.Stdin = bytes.NewReader(body)
	var out limitedBuffer
	cmd.Stdout, cmd.Stderr = &out, &out
	cmd.WaitDelay = 2 * time.Second
	err = cmd.Run()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return nil
	case ctx.Err() != nil:
		return fmt.Errorf("%w: %s: %v", ext.ErrUnavailable, e.cfg.Command[0], ctx.Err())
	case errors.As(err, &exitErr):
		switch exitErr.ExitCode() {
		case ExitWrongCurrent:
			return ext.ErrWrongPassword
		case ExitWeak:
			return ext.ErrWeakPassword
		case ExitRateLimited:
			return ext.ErrRateLimited
		}
	}
	return fmt.Errorf("%w: %s: %v: %s", ext.ErrUnavailable, e.cfg.Command[0], err, out.String())
}

// HTTP отправляет POST с JSON на API панели или сервиса учёток.
//
//	200..299 — пароль сменён; 403 — неверный текущий; 400, 422 — новый не подходит;
//	429 — слишком много попыток; остальное (в том числе 401) — сервис недоступен.
type HTTP struct {
	cfg    config.PasswordHTTP
	token  string
	client *http.Client
	log    *slog.Logger
}

func NewHTTP(cfg config.PasswordHTTP, log *slog.Logger) (*HTTP, error) {
	h := &HTTP{cfg: cfg, log: log}
	if cfg.TokenFile != "" {
		b, err := config.ReadSecret(cfg.TokenFile)
		if err != nil {
			return nil, fmt.Errorf("password.http.token_file: %w", err)
		}
		h.token = string(b)
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil // запрос идёт к своему сервису, не через прокси окружения
	tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: !cfg.TLSVerify}
	h.client = &http.Client{
		Transport: tr, Timeout: cfg.Timeout,
		// Перенаправление с паролями в теле не выполняем.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return h, nil
}

func (h *HTTP) Capabilities() ext.PasswordCaps {
	return ext.PasswordCaps{VerifiesCurrent: h.cfg.VerifiesCurrent}
}

func (h *HTTP) Change(ctx context.Context, r ext.PasswordChange) error {
	body, err := json.Marshal(newRequest(r))
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.cfg.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if h.token != "" {
		req.Header.Set("Authorization", "Bearer "+h.token)
	}
	res, err := h.client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ext.ErrUnavailable, err)
	}
	defer res.Body.Close()
	msg, _ := io.ReadAll(io.LimitReader(res.Body, 2048))
	switch c := res.StatusCode; {
	case c >= 200 && c < 300:
		return nil
	case c == http.StatusForbidden:
		return ext.ErrWrongPassword
	case c == http.StatusBadRequest || c == http.StatusUnprocessableEntity:
		return ext.ErrWeakPassword
	case c == http.StatusTooManyRequests:
		return ext.ErrRateLimited
	default:
		return fmt.Errorf("%w: HTTP %d: %s", ext.ErrUnavailable, c, strings.TrimSpace(string(msg)))
	}
}

// limitedBuffer хранит первые 2 КБ вывода команды для журнала.
type limitedBuffer struct{ bytes.Buffer }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if room := 2048 - b.Len(); room > 0 {
		b.Buffer.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

func (b *limitedBuffer) String() string { return strings.TrimSpace(b.Buffer.String()) }
