// Package config — загрузка и проверка конфига по контракту version: 1.
//
// Правила: неизвестный ключ — ошибка; любой скалярный ключ переопределяется
// переменной окружения WEBMAIL_<ПУТЬ> с "__" между уровнями
// (WEBMAIL_IMAP__HOST); секреты задаются только файлами (*_file).
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Version    int                  `yaml:"version"`
	Server     Server               `yaml:"server"`
	Session    Session              `yaml:"session"`
	Storage    Storage              `yaml:"storage"`
	Login      Login                `yaml:"login"`
	IMAP       IMAP                 `yaml:"imap"`
	SMTP       SMTP                 `yaml:"smtp"`
	Sieve      Sieve                `yaml:"sieve"`
	Password   Password             `yaml:"password"`
	Identities Identities           `yaml:"identities"`
	UI         UI                   `yaml:"ui"`
	Spellcheck Spellcheck           `yaml:"spellcheck"`
	Log        Log                  `yaml:"log"`
	Metrics    Metrics              `yaml:"metrics"`
	Adapters   map[string]yaml.Node `yaml:"adapters"`
}

type Server struct {
	Listen         string   `yaml:"listen"`
	PublicURL      string   `yaml:"public_url"`
	TrustedProxies []string `yaml:"trusted_proxies"`
	TLS            struct {
		CertFile string `yaml:"cert_file"`
		KeyFile  string `yaml:"key_file"`
	} `yaml:"tls"`
	FrameAncestors []string `yaml:"frame_ancestors"`
}

type Session struct {
	Store           string        `yaml:"store"`
	RedisURLFile    string        `yaml:"redis_url_file"`
	CookieName      string        `yaml:"cookie_name"`
	IdleTimeout     time.Duration `yaml:"idle_timeout"`
	AbsoluteTimeout time.Duration `yaml:"absolute_timeout"`
	SecretFile      string        `yaml:"secret_file"`
}

type Storage struct {
	Driver  string `yaml:"driver"`
	DSNFile string `yaml:"dsn_file"`
}

type Login struct {
	UsernameFormat       string   `yaml:"username_format"`
	DefaultDomain        string   `yaml:"default_domain"`
	AllowedDomains       []string `yaml:"allowed_domains"`
	RateLimit            Limits   `yaml:"rate_limit"`
	CaptchaAfterFailures int      `yaml:"captcha_after_failures"`
}

type Limits struct {
	PerIP   Rate `yaml:"per_ip"`
	PerUser Rate `yaml:"per_user"`
}

type Endpoint struct {
	Host      string `yaml:"host"`
	Port      int    `yaml:"port"`
	Security  string `yaml:"security"` // tls | starttls | none
	TLSVerify bool   `yaml:"tls_verify"`
}

func (e Endpoint) Addr() string { return net.JoinHostPort(e.Host, strconv.Itoa(e.Port)) }

type IMAP struct {
	Endpoint       `yaml:",inline"`
	CAFile         string        `yaml:"ca_file"`
	AuthMechanisms []string      `yaml:"auth_mechanisms"`
	Timeout        time.Duration `yaml:"timeout"`
	Pool           struct {
		MaxConnsPerSession int           `yaml:"max_conns_per_session"`
		IdleClose          time.Duration `yaml:"idle_close"`
	} `yaml:"pool"`
	Folders struct {
		Detect        string            `yaml:"detect"`
		Fallback      map[string]string `yaml:"fallback"`
		CreateMissing bool              `yaml:"create_missing"`
	} `yaml:"folders"`
	Impersonation Impersonation `yaml:"impersonation"`
}

type Impersonation struct {
	Enabled            bool     `yaml:"enabled"`
	Mechanism          string   `yaml:"mechanism"`
	MasterUser         string   `yaml:"master_user"`
	MasterPasswordFile string   `yaml:"master_password_file"`
	AllowedSources     []string `yaml:"allowed_sources"`
	ApplyTo            []string `yaml:"apply_to"`
}

type SMTP struct {
	Endpoint       `yaml:",inline"`
	Auth           string   `yaml:"auth"` // session | none
	MaxMessageSize ByteSize `yaml:"max_message_size"`
	RateLimit      struct {
		PerUser       Rate `yaml:"per_user"`
		MaxRecipients int  `yaml:"max_recipients"`
	} `yaml:"rate_limit"`
	SaveSent bool `yaml:"save_sent"`
}

type Sieve struct {
	Enabled  bool `yaml:"enabled"`
	Endpoint `yaml:",inline"`
}

// Identities — какие адреса отправителя пользователь может задавать сам.
type Identities struct {
	Level string `yaml:"level"` // single | domain | any
}

// Password — смена пароля ящика. driver: none | exec | http | имя адаптера.
type Password struct {
	Driver string `yaml:"driver"`
	Policy struct {
		MinLength      int  `yaml:"min_length"`
		MaxLength      int  `yaml:"max_length"`
		RequireClasses int  `yaml:"require_classes"` // из 4: строчные, заглавные, цифры, прочие
		ForbidMailbox  bool `yaml:"forbid_mailbox"`  // пароль не должен содержать имя ящика
		CheckBreached  bool `yaml:"check_breached"`  // Have I Been Pwned, k-anonymity
	} `yaml:"policy"`
	Exec PasswordExec `yaml:"exec"`
	HTTP PasswordHTTP `yaml:"http"`
}

// PasswordExec — внешняя команда. Запрос приходит JSON в stdin, ответ — код выхода.
type PasswordExec struct {
	Command         []string      `yaml:"command"` // argv, без оболочки
	Timeout         time.Duration `yaml:"timeout"`
	VerifiesCurrent bool          `yaml:"verifies_current"`
}

// PasswordHTTP — POST JSON на API панели или сервиса учёток.
type PasswordHTTP struct {
	URL             string        `yaml:"url"`
	TokenFile       string        `yaml:"token_file"` // Bearer, права 0600
	Timeout         time.Duration `yaml:"timeout"`
	TLSVerify       bool          `yaml:"tls_verify"`
	VerifiesCurrent bool          `yaml:"verifies_current"`
}

type UI struct {
	DefaultLanguage string   `yaml:"default_language"`
	Languages       []string `yaml:"languages"`
	Theme           string   `yaml:"theme"`
	Brand           Brand    `yaml:"brand"`
	Attachments     struct {
		MaxUpload    ByteSize `yaml:"max_upload"`
		RemoteImages string   `yaml:"remote_images"` // ask | always | never
	} `yaml:"attachments"`
}

// Brand — оформление интерфейса: название, логотипы, favicon и цвет акцента.
// Картинка задаётся либо ссылкой (*_url), либо локальным файлом (*_file),
// который сервер раздаёт сам по /brand/<имя>. accent — основной цвет (#rgb или
// #rrggbb); производные оттенки (фон выделения, цвет текста на кнопке) интерфейс
// считает сам. accent_dark — цвет для тёмной темы, по умолчанию выводится из accent.
// css_vars — тонкая настройка: любые CSS-переменные поверх рассчитанных, в обеих темах.
type Brand struct {
	Name         string            `yaml:"name"`
	LogoURL      string            `yaml:"logo_url"`
	LogoFile     string            `yaml:"logo_file"`
	LogoDarkURL  string            `yaml:"logo_dark_url"`
	LogoDarkFile string            `yaml:"logo_dark_file"`
	FaviconURL   string            `yaml:"favicon_url"`
	FaviconFile  string            `yaml:"favicon_file"`
	Accent       string            `yaml:"accent"`
	AccentDark   string            `yaml:"accent_dark"`
	CSSVars      map[string]string `yaml:"css_vars"`
}

// BrandAsset — картинка бренда: имя в /brand/<name>, ссылка и файл из конфига.
type BrandAsset struct{ Name, Key, URL, File string }

// Assets — картинки бренда в порядке logo, logo_dark, favicon.
func (b *Brand) Assets() []BrandAsset {
	return []BrandAsset{
		{"logo", "logo", b.LogoURL, b.LogoFile},
		{"logo-dark", "logo_dark", b.LogoDarkURL, b.LogoDarkFile},
		{"favicon", "favicon", b.FaviconURL, b.FaviconFile},
	}
}

// MaxBrandFile — предельный размер файла логотипа или favicon.
const MaxBrandFile = 1 << 20

// BrandFileTypes — допустимые расширения файлов бренда и их Content-Type.
var BrandFileTypes = map[string]string{
	".svg": "image/svg+xml", ".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg",
	".webp": "image/webp", ".gif": "image/gif", ".ico": "image/x-icon",
}

var (
	hexColor   = regexp.MustCompile(`^#([0-9a-fA-F]{3}|[0-9a-fA-F]{6})$`)
	cssVarName = regexp.MustCompile(`^--[a-z0-9-]{1,64}$`)
)

// ValidCSSValue — значение CSS-переменной без символов, ломающих декларацию.
func ValidCSSValue(v string) bool {
	return v != "" && len(v) <= 200 && !strings.ContainsAny(v, ";{}<>\\\"'\n\r") &&
		!strings.Contains(strings.ToLower(v), "url(")
}

// ValidHexColor — цвет вида #rgb или #rrggbb.
func ValidHexColor(v string) bool { return hexColor.MatchString(v) }

// ValidCSSVar — имя --lower-case и безопасное значение.
func ValidCSSVar(k, v string) bool { return cssVarName.MatchString(k) && ValidCSSValue(v) }

// ValidBrandURL — ссылка на картинку: https/http или путь от корня этого сайта.
func ValidBrandURL(u string) bool {
	if strings.HasPrefix(u, "/") {
		return !strings.HasPrefix(u, "//")
	}
	p, err := url.Parse(u)
	return err == nil && (p.Scheme == "https" || p.Scheme == "http") && p.Host != ""
}

func (b *Brand) validate(add func(string, ...any)) {
	if strings.TrimSpace(b.Name) == "" || len(b.Name) > 64 {
		add("ui.brand.name: required, at most 64 bytes")
	}
	for _, a := range b.Assets() {
		if a.URL != "" && a.File != "" {
			add("ui.brand: %s_url and %s_file are mutually exclusive", a.Key, a.Key)
		}
		if a.URL != "" && !ValidBrandURL(a.URL) {
			add("ui.brand.%s_url: must be an http(s) URL or a path starting with /", a.Key)
		}
		if a.File != "" {
			if err := checkBrandFile(a.File); err != nil {
				add("ui.brand.%s_file: %v", a.Key, err)
			}
		}
	}
	for k, v := range map[string]string{"accent": b.Accent, "accent_dark": b.AccentDark} {
		if v != "" && !hexColor.MatchString(v) {
			add("ui.brand.%s: must be a hex color like #0b7a5c", k)
		}
	}
	for k, v := range b.CSSVars {
		if !cssVarName.MatchString(k) {
			add("ui.brand.css_vars: bad variable name %q, want --lower-case", k)
		} else if !ValidCSSValue(v) {
			add("ui.brand.css_vars.%s: empty, too long or contains forbidden characters", k)
		}
	}
}

func checkBrandFile(path string) error {
	if _, ok := BrandFileTypes[strings.ToLower(filepath.Ext(path))]; !ok {
		return errors.New("unsupported type, want svg, png, jpg, webp, gif or ico")
	}
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() {
		return errors.New("not a regular file")
	}
	if st.Size() > MaxBrandFile {
		return fmt.Errorf("larger than %d bytes", MaxBrandFile)
	}
	return nil
}

// Spellcheck — проверка орфографии в редакторе письма.
// engine: browser — встроенная проверка браузера; hunspell — на сервере,
// со словарями из списка и личным словарём пользователя; off — выключена.
type Spellcheck struct {
	Engine   string `yaml:"engine"`
	Hunspell struct {
		Command      string        `yaml:"command"`
		Dictionaries []string      `yaml:"dictionaries"`
		Timeout      time.Duration `yaml:"timeout"`
	} `yaml:"hunspell"`
}

type Log struct {
	Level     string `yaml:"level"`
	Format    string `yaml:"format"`
	AuditFile string `yaml:"audit_file"`
}

type Metrics struct {
	Listen string `yaml:"listen"`
}

// Defaults — значения по умолчанию из контракта (раздел 6.5 ТЗ).
func Defaults() *Config {
	c := &Config{Version: 1}
	c.Server.Listen = "127.0.0.1:8080"
	c.Server.TrustedProxies = []string{"127.0.0.1/32", "::1/128"}
	c.Session.Store = "memory"
	c.Session.CookieName = "wm_session"
	c.Session.IdleTimeout = 30 * time.Minute
	c.Session.AbsoluteTimeout = 12 * time.Hour
	c.Storage.Driver = "sqlite"
	c.Login.UsernameFormat = "%u"
	c.Login.RateLimit = Limits{PerIP: Rate{20, 10 * time.Minute}, PerUser: Rate{5, 15 * time.Minute}}
	c.Login.CaptchaAfterFailures = 3
	c.IMAP.Port = 993
	c.IMAP.Security = "tls"
	c.IMAP.TLSVerify = true
	c.IMAP.AuthMechanisms = []string{"plain", "login"}
	c.IMAP.Timeout = 15 * time.Second
	c.IMAP.Pool.MaxConnsPerSession = 3
	c.IMAP.Pool.IdleClose = 5 * time.Minute
	c.IMAP.Folders.Detect = "special_use"
	c.IMAP.Folders.Fallback = map[string]string{
		"drafts": "Drafts", "sent": "Sent", "junk": "Junk", "trash": "Trash", "archive": "Archive",
	}
	c.SMTP.Port = 587
	c.SMTP.Security = "starttls"
	c.SMTP.TLSVerify = true
	c.SMTP.Auth = "session"
	c.SMTP.MaxMessageSize = 25 << 20
	c.SMTP.RateLimit.PerUser = Rate{100, time.Hour}
	c.SMTP.RateLimit.MaxRecipients = 50
	c.SMTP.SaveSent = true
	c.Sieve.Port = 4190
	c.Sieve.Security = "starttls"
	c.Sieve.TLSVerify = true
	c.Identities.Level = "single"
	c.Password.Driver = "none"
	c.Password.Policy.MinLength = 10
	c.Password.Policy.RequireClasses = 3
	c.Password.Policy.MaxLength = 128
	c.Password.Policy.ForbidMailbox = true
	c.Password.Exec.Timeout = 10 * time.Second
	c.Password.HTTP.Timeout = 10 * time.Second
	c.Password.HTTP.TLSVerify = true
	c.UI.DefaultLanguage = "ru"
	c.UI.Languages = []string{"ru", "en", "uz"}
	c.UI.Theme = "system"
	c.UI.Brand.Name = "Webmail"
	c.UI.Attachments.MaxUpload = 25 << 20
	c.UI.Attachments.RemoteImages = "ask"
	c.Spellcheck.Engine = "browser"
	c.Spellcheck.Hunspell.Command = "hunspell"
	c.Spellcheck.Hunspell.Dictionaries = []string{"ru_RU", "en_US"}
	c.Spellcheck.Hunspell.Timeout = 5 * time.Second
	c.Log.Level = "info"
	c.Log.Format = "json"
	return c
}

// Load читает YAML поверх значений по умолчанию, применяет env и проверяет.
func Load(path string) (*Config, error) {
	c := Defaults()
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		dec := yaml.NewDecoder(bytes.NewReader(data))
		dec.KnownFields(true)
		if err := dec.Decode(c); err != nil && !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
	}
	if err := applyEnv(c, os.Environ()); err != nil {
		return nil, err
	}
	if c.Sieve.Host == "" {
		c.Sieve.Host = c.IMAP.Host
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return c, nil
}

// spellDict — имя словаря hunspell: ru_RU, en_US, de_DE_frami.
var spellDict = regexp.MustCompile(`^[A-Za-z]{2,3}(_[A-Za-z0-9]{2,8})*$`)

// Validate проверяет обязательные поля и допустимые значения.
func (c *Config) Validate() error {
	var errs []string
	add := func(f string, a ...any) { errs = append(errs, fmt.Sprintf(f, a...)) }

	if c.Version != 1 {
		add("version: unsupported %d, want 1", c.Version)
	}
	if c.Server.PublicURL == "" {
		add("server.public_url: required")
	} else if u, err := url.Parse(c.Server.PublicURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		add("server.public_url: must be http(s)://host")
	}
	for _, p := range c.Server.TrustedProxies {
		if _, _, err := net.ParseCIDR(p); err != nil {
			add("server.trusted_proxies: %q is not a CIDR", p)
		}
	}
	if c.Session.SecretFile == "" {
		add("session.secret_file: required (generate with: webmail gen-secret > FILE)")
	}
	if c.Storage.Driver != "sqlite" {
		add("storage.driver: %q is not implemented yet, use sqlite", c.Storage.Driver)
	}
	c.validatePassword(add)
	if !contains([]string{"single", "domain", "any"}, c.Identities.Level) {
		add("identities.level: must be single, domain or any")
	}
	if c.Session.Store != "memory" {
		add("session.store: %q is not implemented yet, use memory", c.Session.Store)
	}
	checkEndpoint := func(name string, e Endpoint, allowed ...string) {
		if e.Host == "" {
			add("%s.host: required", name)
		}
		if e.Port <= 0 || e.Port > 65535 {
			add("%s.port: invalid", name)
		}
		if !contains(allowed, e.Security) {
			add("%s.security: must be one of %s", name, strings.Join(allowed, ", "))
		}
		if e.Security == "none" && !isLoopback(e.Host) {
			add("%s.security: none is allowed only for loopback hosts", name)
		}
	}
	checkEndpoint("imap", c.IMAP.Endpoint, "tls", "starttls", "none")
	checkEndpoint("smtp", c.SMTP.Endpoint, "tls", "starttls", "none")
	if c.Sieve.Enabled {
		checkEndpoint("sieve", c.Sieve.Endpoint, "starttls", "none")
	}
	if c.Metrics.Listen != "" {
		if _, _, err := net.SplitHostPort(c.Metrics.Listen); err != nil {
			add("metrics.listen: must be host:port (e.g. 127.0.0.1:9180)")
		}
	}
	switch c.Spellcheck.Engine {
	case "browser", "off":
	case "hunspell":
		if len(c.Spellcheck.Hunspell.Dictionaries) == 0 {
			add("spellcheck.hunspell.dictionaries: at least one is required")
		}
		for _, d := range c.Spellcheck.Hunspell.Dictionaries {
			if !spellDict.MatchString(d) {
				add("spellcheck.hunspell.dictionaries: invalid name %q", d)
			}
		}
		if c.Spellcheck.Hunspell.Command == "" {
			add("spellcheck.hunspell.command: required")
		}
		if c.Spellcheck.Hunspell.Timeout <= 0 {
			add("spellcheck.hunspell.timeout: must be positive")
		}
	default:
		add("spellcheck.engine: must be browser, hunspell or off")
	}
	if !contains([]string{"session", "none"}, c.SMTP.Auth) {
		add("smtp.auth: must be session or none")
	}
	if c.IMAP.Impersonation.Enabled {
		im := c.IMAP.Impersonation
		if im.Mechanism != "sasl_plain_authzid" {
			add("imap.impersonation.mechanism: only sasl_plain_authzid is supported")
		}
		if im.MasterUser == "" || im.MasterPasswordFile == "" {
			add("imap.impersonation: master_user and master_password_file are required")
		}
		if len(im.AllowedSources) == 0 {
			add("imap.impersonation.allowed_sources: at least one source required")
		}
	}
	if !contains([]string{"special_use", "names"}, c.IMAP.Folders.Detect) {
		add("imap.folders.detect: must be special_use or names")
	}
	if !contains([]string{"ask", "always", "never"}, c.UI.Attachments.RemoteImages) {
		add("ui.attachments.remote_images: must be ask, always or never")
	}
	c.UI.Brand.validate(add)
	if !contains([]string{"light", "dark", "system"}, c.UI.Theme) {
		add("ui.theme: must be light, dark or system")
	}
	if !contains([]string{"debug", "info", "warn", "error"}, c.Log.Level) {
		add("log.level: must be debug, info, warn or error")
	}
	if !contains([]string{"json", "text"}, c.Log.Format) {
		add("log.format: must be json or text")
	}
	for name, node := range c.Adapters {
		var hdr struct {
			Enabled bool `yaml:"enabled"`
		}
		if err := node.Decode(&hdr); err != nil {
			add("adapters.%s: %v", name, err)
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("config errors:\n  - %s", strings.Join(errs, "\n  - "))
	}
	return nil
}

func (c *Config) validatePassword(add func(string, ...any)) {
	p := &c.Password
	pol := p.Policy
	if pol.MinLength < 6 || pol.MinLength > 128 {
		add("password.policy.min_length: must be 6..128")
	}
	if pol.MaxLength < pol.MinLength || pol.MaxLength > 1024 {
		add("password.policy.max_length: must be min_length..1024")
	}
	if pol.RequireClasses < 0 || pol.RequireClasses > 4 {
		add("password.policy.require_classes: must be 0..4")
	}
	switch p.Driver {
	case "none":
	case "exec":
		if len(p.Exec.Command) == 0 || !filepath.IsAbs(p.Exec.Command[0]) {
			add("password.exec.command: required, the first element must be an absolute path")
		}
		if p.Exec.Timeout <= 0 || p.Exec.Timeout > time.Minute {
			add("password.exec.timeout: must be 1s..1m")
		}
	case "http":
		u, err := url.Parse(p.HTTP.URL)
		if p.HTTP.URL == "" || err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			add("password.http.url: required, http(s)://host/path")
		} else if u.Scheme == "http" && !isLoopback(u.Hostname()) {
			add("password.http.url: plain http is allowed only for loopback hosts")
		}
		if p.HTTP.Timeout <= 0 || p.HTTP.Timeout > time.Minute {
			add("password.http.timeout: must be 1s..1m")
		}
	default:
		if _, ok := c.AdapterEnabled(p.Driver); !ok {
			add("password.driver: must be none, exec, http or the name of an enabled adapter")
		}
	}
}

// Show возвращает итоговый конфиг в YAML. Секретов в нём нет: только пути к файлам.
func (c *Config) Show() ([]byte, error) { return yaml.Marshal(c) }

// AdapterEnabled сообщает, включена ли секция адаптера.
func (c *Config) AdapterEnabled(name string) (yaml.Node, bool) {
	node, ok := c.Adapters[name]
	if !ok {
		return node, false
	}
	var hdr struct {
		Enabled bool `yaml:"enabled"`
	}
	_ = node.Decode(&hdr)
	return node, hdr.Enabled
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// applyEnv применяет WEBMAIL_A__B=value к скалярным полям по yaml-тегам.
func applyEnv(c *Config, environ []string) error {
	for _, kv := range environ {
		if !strings.HasPrefix(kv, "WEBMAIL_") {
			continue
		}
		k, v, _ := strings.Cut(kv, "=")
		path := strings.Split(strings.ToLower(strings.TrimPrefix(k, "WEBMAIL_")), "__")
		if len(path) < 2 {
			// Ключи верхнего уровня — секции; переменные без "__" (WEBMAIL_CONFIG,
			// WEBMAIL_IT для тестов и т. п.) к конфигу не относятся.
			continue
		}
		if err := setPath(reflect.ValueOf(c).Elem(), path, v); err != nil {
			return fmt.Errorf("%s: %w", k, err)
		}
	}
	return nil
}

func setPath(v reflect.Value, path []string, val string) error {
	if len(path) == 0 {
		return setScalar(v, val)
	}
	if v.Kind() != reflect.Struct {
		return fmt.Errorf("%w %q", errUnknownKey, path[0])
	}
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := strings.Split(f.Tag.Get("yaml"), ",")
		if len(tag) > 1 && tag[1] == "inline" {
			err := setPath(v.Field(i), path, val)
			if !errors.Is(err, errUnknownKey) {
				return err // нашли ключ во встроенной структуре (успех или ошибка значения)
			}
			continue
		}
		if tag[0] == path[0] {
			return setPath(v.Field(i), path[1:], val)
		}
	}
	return fmt.Errorf("%w %q", errUnknownKey, path[0])
}

var errUnknownKey = errors.New("unknown key")

func setScalar(v reflect.Value, s string) error {
	switch v.Interface().(type) {
	case time.Duration:
		d, err := time.ParseDuration(s)
		if err != nil {
			return err
		}
		v.Set(reflect.ValueOf(d))
		return nil
	case ByteSize:
		b, err := ParseByteSize(s)
		if err != nil {
			return err
		}
		v.Set(reflect.ValueOf(b))
		return nil
	case Rate:
		r, err := ParseRate(s)
		if err != nil {
			return err
		}
		v.Set(reflect.ValueOf(r))
		return nil
	}
	switch v.Kind() {
	case reflect.String:
		v.SetString(s)
	case reflect.Int:
		n, err := strconv.Atoi(s)
		if err != nil {
			return err
		}
		v.SetInt(int64(n))
	case reflect.Bool:
		b, err := strconv.ParseBool(s)
		if err != nil {
			return err
		}
		v.SetBool(b)
	case reflect.Slice:
		if v.Type().Elem().Kind() != reflect.String {
			return errors.New("unsupported list type")
		}
		var parts []string
		for _, p := range strings.Split(s, ",") {
			if p = strings.TrimSpace(p); p != "" {
				parts = append(parts, p)
			}
		}
		v.Set(reflect.ValueOf(parts))
	default:
		return fmt.Errorf("key is not a scalar")
	}
	return nil
}

// ReadSecret читает секрет из файла и предупреждает о слишком открытых правах.
func ReadSecret(path string) ([]byte, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if st.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%s: permissions %o are too open, want 0600", path, st.Mode().Perm())
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return bytes.TrimSpace(b), nil
}
