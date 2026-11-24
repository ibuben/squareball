// Package app — точка входа ядра. Сторонние сборки (с адаптерами) вызывают
// app.Main(adapters...) из своего main; ядро про адаптеры не знает.
package app

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/ibuben/squareball/ext"
	"github.com/ibuben/squareball/internal/api"
	"github.com/ibuben/squareball/internal/config"
	"github.com/ibuben/squareball/internal/mail"
	"github.com/ibuben/squareball/internal/password"
	"github.com/ibuben/squareball/internal/store"
	"github.com/ibuben/squareball/internal/web"
)

// Version подставляется при сборке: -ldflags "-X github.com/ibuben/squareball/app.Version=..."
var Version = "dev"

const usage = `webmail — почтовый веб-клиент

Использование:
  webmail serve        [-config FILE]   запустить сервер
  webmail config check [-config FILE]   проверить конфиг и подключение к серверам
  webmail config show  [-config FILE]   показать итоговый конфиг (с env и значениями по умолчанию)
  webmail gen-secret                    напечатать случайный секрет для session.secret_file
  webmail passwd-file -file FILE [-verify-current]
                                        помощник для password.driver: exec — меняет хеш
                                        в Dovecot passwd-file (запрос JSON читается из stdin)
  webmail version

Путь к конфигу: -config, иначе $WEBMAIL_CONFIG, иначе /etc/webmail/config.yaml.
`

// Main разбирает аргументы командной строки и выходит с кодом ошибки.
func Main(adapters ...ext.Adapter) {
	if err := run(os.Args[1:], adapters); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string, adapters []ext.Adapter) error {
	if len(args) == 0 {
		fmt.Print(usage)
		return nil
	}
	cmd, rest := args[0], args[1:]
	if cmd == "passwd-file" {
		return passwdFile(rest)
	}
	if cmd == "config" && len(rest) > 0 {
		cmd, rest = "config "+rest[0], rest[1:]
	}
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	def := os.Getenv("WEBMAIL_CONFIG")
	if def == "" {
		def = "/etc/webmail/config.yaml"
	}
	cfgPath := fs.String("config", def, "path to config.yaml")
	if err := fs.Parse(rest); err != nil {
		return err
	}
	switch cmd {
	case "version":
		fmt.Println(Version)
		return nil
	case "gen-secret":
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			return err
		}
		fmt.Println(hex.EncodeToString(b))
		return nil
	case "config show":
		cfg, err := config.Load(*cfgPath)
		if err != nil {
			return err
		}
		out, err := cfg.Show()
		if err != nil {
			return err
		}
		os.Stdout.Write(out)
		return nil
	case "config check":
		return check(*cfgPath)
	case "serve":
		return Run(context.Background(), *cfgPath, adapters...)
	}
	fmt.Print(usage)
	return fmt.Errorf("unknown command %q", cmd)
}

// passwdFile — помощник драйвера exec. Ошибки пишет в stderr и выходит
// с кодом из протокола exec (2 — неверный текущий пароль, 3 — новый не подходит).
func passwdFile(args []string) error {
	fs := flag.NewFlagSet("passwd-file", flag.ContinueOnError)
	file := fs.String("file", "", "path to Dovecot passwd-file")
	verify := fs.Bool("verify-current", false, "check current password against the stored BLF-CRYPT hash")
	cost := fs.Int("cost", 10, "bcrypt cost")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *file == "" {
		return errors.New("passwd-file: -file is required")
	}
	pf := &password.PasswdFile{Path: *file, VerifyCurrent: *verify, Cost: *cost}
	if err := pf.Run(os.Stdin); err != nil {
		fmt.Fprintln(os.Stderr, "passwd-file:", err)
		os.Exit(password.ExitCode(err))
	}
	return nil
}

func check(path string) error {
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	fmt.Println("config: ok")
	if _, err := config.ReadSecret(cfg.Session.SecretFile); err != nil {
		return fmt.Errorf("session.secret_file: %w", err)
	}
	fmt.Println("session secret: ok")
	c, err := mail.DialIMAP(&cfg.IMAP, mail.Creds{Username: "config-check@invalid", Password: "x"})
	switch {
	case errors.Is(err, mail.ErrAuth):
		fmt.Printf("imap %s (%s): reachable, login rejected as expected\n", cfg.IMAP.Addr(), cfg.IMAP.Security)
	case err != nil:
		return fmt.Errorf("imap %s: %w", cfg.IMAP.Addr(), err)
	default:
		c.Close()
		fmt.Printf("imap %s: reachable\n", cfg.IMAP.Addr())
	}
	if err := mail.Probe(&cfg.SMTP); err != nil {
		return fmt.Errorf("smtp %s: %w", cfg.SMTP.Addr(), err)
	}
	fmt.Printf("smtp %s (%s): reachable\n", cfg.SMTP.Addr(), cfg.SMTP.Security)
	return nil
}

func newLogger(cfg *config.Config, w io.Writer) *slog.Logger {
	var lvl slog.Level
	_ = lvl.UnmarshalText([]byte(cfg.Log.Level))
	opts := &slog.HandlerOptions{Level: lvl}
	if cfg.Log.Format == "text" {
		return slog.New(slog.NewTextHandler(w, opts))
	}
	return slog.New(slog.NewJSONHandler(w, opts))
}

type configNode struct{ n yaml.Node }

func (c configNode) Decode(v any) error {
	out, err := yaml.Marshal(&c.n)
	if err != nil {
		return err
	}
	dec := yaml.NewDecoder(bytes.NewReader(out))
	dec.KnownFields(true)
	return dec.Decode(v)
}

// Run запускает сервер и блокируется до SIGINT/SIGTERM или ошибки.
func Run(ctx context.Context, cfgPath string, adapters ...ext.Adapter) error {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	log := newLogger(cfg, os.Stderr)
	audit := log.With("audit", true)
	if cfg.Log.AuditFile != "" {
		f, err := os.OpenFile(cfg.Log.AuditFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return fmt.Errorf("log.audit_file: %w", err)
		}
		defer f.Close()
		audit = newLogger(cfg, f)
	}
	secret, err := config.ReadSecret(cfg.Session.SecretFile)
	if err != nil {
		return fmt.Errorf("session.secret_file: %w", err)
	}
	dbPath, err := store.Path(cfg.Storage.DSNFile)
	if err != nil {
		return fmt.Errorf("storage: %w", err)
	}
	st, err := store.Open(dbPath)
	if err != nil {
		return err
	}
	defer st.Close()
	log.Info("storage", "driver", "sqlite", "path", dbPath)
	srv, err := api.New(api.Options{Config: cfg, Logger: log, Audit: audit, SessionSecret: secret, Store: st, Static: web.Handler()})
	if err != nil {
		return err
	}

	// Встроенные драйверы смены пароля (exec, http); none и драйверы адаптеров — nil.
	pc, err := password.New(&cfg.Password, log)
	if err != nil {
		return err
	}
	if pc != nil {
		srv.SetPasswordChanger(cfg.Password.Driver, pc)
		log.Info("password change", "driver", cfg.Password.Driver)
	}

	known := map[string]bool{}
	for _, a := range adapters {
		known[a.Name()] = true
		node, enabled := cfg.AdapterEnabled(a.Name())
		if !enabled {
			continue
		}
		if err := a.Init(ctx, configNode{node}, srv); err != nil {
			return fmt.Errorf("adapter %s: %w", a.Name(), err)
		}
		log.Info("adapter enabled", "name", a.Name())
	}
	for name := range cfg.Adapters {
		if !known[name] {
			return fmt.Errorf("adapters.%s: adapter is not compiled into this binary", name)
		}
	}
	if !srv.PasswordEnabled() && cfg.Password.Driver != "none" {
		log.Warn("adapter did not register password driver; password change is disabled", "driver", cfg.Password.Driver)
	}

	if cfg.Metrics.Listen != "" {
		mux := http.NewServeMux()
		mux.Handle("GET /metrics", srv.MetricsHandler(Version))
		ms := &http.Server{Addr: cfg.Metrics.Listen, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
		go func() {
			log.Info("metrics", "addr", cfg.Metrics.Listen)
			if err := ms.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Error("metrics", "err", err)
			}
		}()
		defer ms.Close()
	}

	hs := &http.Server{
		Addr: cfg.Server.Listen, Handler: srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 120 * time.Second,
	}
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	errCh := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", cfg.Server.Listen, "public_url", cfg.Server.PublicURL, "version", Version)
		if cfg.Server.TLS.CertFile != "" {
			errCh <- hs.ListenAndServeTLS(cfg.Server.TLS.CertFile, cfg.Server.TLS.KeyFile)
		} else {
			errCh <- hs.ListenAndServe()
		}
	}()
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}
	log.Info("shutting down")
	sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return hs.Shutdown(sctx)
}
