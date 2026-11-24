// Package mail — работа с IMAP и SMTP от имени пользователя.
package mail

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-sasl"

	"github.com/ibuben/squareball/internal/config"
)

// ErrAuth — неверный логин или пароль (ответ сервера NO на LOGIN/AUTHENTICATE).
var ErrAuth = errors.New("authentication failed")

// Creds — как входить в IMAP для этой сессии.
type Creds struct {
	Username string
	Password string
	// MasterUser/MasterPassword заполнены для impersonation (SASL PLAIN authzid).
	MasterUser     string
	MasterPassword string
}

func tlsConfig(host string, verify bool, caFile string) (*tls.Config, error) {
	c := &tls.Config{ServerName: host, InsecureSkipVerify: !verify, MinVersion: tls.VersionTLS12}
	if caFile != "" {
		pem, err := os.ReadFile(caFile)
		if err != nil {
			return nil, err
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("%s: no certificates", caFile)
		}
		c.RootCAs = pool
	}
	return c, nil
}

// DialIMAP открывает соединение и выполняет вход.
func DialIMAP(cfg *config.IMAP, cr Creds) (*imapclient.Client, error) {
	tc, err := tlsConfig(cfg.Host, cfg.TLSVerify, cfg.CAFile)
	if err != nil {
		return nil, err
	}
	opts := &imapclient.Options{TLSConfig: tc}
	dialer := &net.Dialer{Timeout: cfg.Timeout}
	conn, err := dialer.Dial("tcp", cfg.Addr())
	if err != nil {
		return nil, fmt.Errorf("imap connect: %w", err)
	}
	var c *imapclient.Client
	switch cfg.Security {
	case "tls":
		tconn := tls.Client(conn, tc)
		if err := tconn.Handshake(); err != nil {
			conn.Close()
			return nil, fmt.Errorf("imap tls: %w", err)
		}
		c = imapclient.New(tconn, opts)
	case "starttls":
		c, err = imapclient.NewStartTLS(conn, opts)
		if err != nil {
			conn.Close()
			return nil, fmt.Errorf("imap starttls: %w", err)
		}
	default:
		c = imapclient.New(conn, opts)
	}
	if err := c.WaitGreeting(); err != nil {
		c.Close()
		return nil, fmt.Errorf("imap greeting: %w", err)
	}

	if cr.MasterUser != "" {
		err = c.Authenticate(sasl.NewPlainClient(cr.Username, cr.MasterUser, cr.MasterPassword))
	} else if c.Caps().Has(imap.AuthCap(sasl.Plain)) {
		err = c.Authenticate(sasl.NewPlainClient("", cr.Username, cr.Password))
	} else {
		err = c.Login(cr.Username, cr.Password).Wait()
	}
	if err != nil {
		c.Close()
		var ie *imap.Error
		if errors.As(err, &ie) && ie.Type == imap.StatusResponseTypeNo {
			return nil, ErrAuth
		}
		return nil, fmt.Errorf("imap login: %w", err)
	}
	return c, nil
}

// Pool держит одно IMAP-соединение на сессию и сериализует команды.
type Pool struct {
	cfg  *config.IMAP
	mu   sync.Mutex
	byID map[string]*entry
}

type entry struct {
	mu       sync.Mutex
	c        *imapclient.Client
	lastUsed time.Time
	closed   bool // запись удалена из пула; её соединение больше не используется
}

func NewPool(cfg *config.IMAP) *Pool {
	p := &Pool{cfg: cfg, byID: map[string]*entry{}}
	go p.reaper()
	return p
}

// With выполняет fn на соединении сессии; при обрыве переподключается один раз.
func (p *Pool) With(sessionID string, cr Creds, fn func(c *imapclient.Client) error) error {
	var e *entry
	for {
		p.mu.Lock()
		var ok bool
		if e, ok = p.byID[sessionID]; !ok {
			e = &entry{}
			p.byID[sessionID] = e
		}
		p.mu.Unlock()
		e.mu.Lock()
		if !e.closed {
			break
		}
		// Запись закрыли (reaper/выход), пока мы ждали блокировку, — берём новую.
		e.mu.Unlock()
	}
	defer e.mu.Unlock()
	for attempt := 0; attempt < 2; attempt++ {
		if e.c == nil || e.c.State() == imap.ConnStateLogout || e.c.State() == imap.ConnStateNone {
			c, err := DialIMAP(p.cfg, cr)
			if err != nil {
				return err
			}
			e.c = c
		}
		e.lastUsed = time.Now()
		err := fn(e.c)
		// Повторяем один раз, только если соединение оборвалось (сервер
		// закрыл его по таймауту простоя); ответы NO/BAD не повторяются.
		if err == nil || e.c.State() != imap.ConnStateLogout || attempt == 1 {
			return err
		}
		e.c.Close()
		e.c = nil
	}
	return nil
}

// Close закрывает соединение сессии (выход, истечение сессии).
func (p *Pool) Close(sessionID string) {
	p.mu.Lock()
	e, ok := p.byID[sessionID]
	delete(p.byID, sessionID)
	p.mu.Unlock()
	if ok {
		e.mu.Lock()
		if e.c != nil {
			_ = e.c.Logout().Wait()
			e.c.Close()
			e.c = nil
		}
		e.closed = true
		e.mu.Unlock()
	}
}

func (p *Pool) reaper() {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for range t.C {
		var idle []string
		p.mu.Lock()
		for id, e := range p.byID {
			if e.mu.TryLock() {
				if e.c != nil && time.Since(e.lastUsed) > p.cfg.Pool.IdleClose {
					idle = append(idle, id)
				}
				e.mu.Unlock()
			}
		}
		p.mu.Unlock()
		for _, id := range idle {
			p.Close(id)
		}
	}
}

func tlsClient(conn net.Conn, tc *tls.Config) net.Conn {
	return tls.Client(conn, tc)
}

// Adopt отдаёт пулу уже открытое при входе соединение, чтобы не логиниться повторно.
func (p *Pool) Adopt(sessionID string, c *imapclient.Client) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if old, ok := p.byID[sessionID]; ok {
		old.mu.Lock()
		if old.c != nil {
			old.c.Close()
			old.c = nil
		}
		old.closed = true
		old.mu.Unlock()
	}
	p.byID[sessionID] = &entry{c: c, lastUsed: time.Now()}
}
