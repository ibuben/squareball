package mail

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"

	"github.com/ibuben/squareball/internal/config"
)

// SendError — отказ сервера, который стоит показать пользователю (например,
// чужой адрес отправителя или превышен лимит).
type SendError struct {
	Code    int
	Message string
}

func (e *SendError) Error() string { return fmt.Sprintf("smtp %d: %s", e.Code, e.Message) }

// Send отправляет письмо через SMTP submission от имени пользователя.
func Send(cfg *config.SMTP, cr Creds, from string, rcpts []string, raw []byte) error {
	tc, err := tlsConfig(cfg.Host, cfg.TLSVerify, "")
	if err != nil {
		return err
	}
	dialer := &net.Dialer{Timeout: 15 * time.Second}
	conn, err := dialer.Dial("tcp", cfg.Addr())
	if err != nil {
		return fmt.Errorf("smtp connect: %w", err)
	}
	var c *smtp.Client
	switch cfg.Security {
	case "tls":
		c = smtp.NewClient(tlsClient(conn, tc))
	case "starttls":
		c, err = smtp.NewClientStartTLS(conn, tc)
		if err != nil {
			conn.Close()
			return fmt.Errorf("smtp starttls: %w", err)
		}
	default:
		c = smtp.NewClient(conn)
	}
	defer c.Close()

	if cfg.Auth == "session" {
		var auth sasl.Client
		if cr.MasterUser != "" {
			auth = sasl.NewPlainClient(cr.Username, cr.MasterUser, cr.MasterPassword)
		} else {
			auth = sasl.NewPlainClient("", cr.Username, cr.Password)
		}
		if err := c.Auth(auth); err != nil {
			return asSendError(err)
		}
	}
	if err := c.Mail(from, nil); err != nil {
		return asSendError(err)
	}
	for _, r := range rcpts {
		if err := c.Rcpt(r, nil); err != nil {
			return asSendError(err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return asSendError(err)
	}
	if _, err := bytes.NewReader(raw).WriteTo(w); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return asSendError(err)
	}
	return c.Quit()
}

func asSendError(err error) error {
	var se *smtp.SMTPError
	if errors.As(err, &se) {
		return &SendError{Code: se.Code, Message: se.Message}
	}
	return err
}

// Probe проверяет, что SMTP-сервер отвечает и TLS согласуется (для config check).
func Probe(cfg *config.SMTP) error {
	tc, err := tlsConfig(cfg.Host, cfg.TLSVerify, "")
	if err != nil {
		return err
	}
	conn, err := (&net.Dialer{Timeout: 10 * time.Second}).Dial("tcp", cfg.Addr())
	if err != nil {
		return err
	}
	var c *smtp.Client
	switch cfg.Security {
	case "tls":
		c = smtp.NewClient(tlsClient(conn, tc))
	case "starttls":
		if c, err = smtp.NewClientStartTLS(conn, tc); err != nil {
			conn.Close()
			return err
		}
	default:
		c = smtp.NewClient(conn)
	}
	defer c.Close()
	if err := c.Hello("localhost"); err != nil {
		return err
	}
	return c.Quit()
}
