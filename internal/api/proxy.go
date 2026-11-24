package api

import (
	"net"
	"net/http"
	"regexp"
	"strings"
)

// prefixRE — допустимый X-Forwarded-Prefix: сегменты пути без "..", кодирования и "//".
var prefixRE = regexp.MustCompile(`^(/[A-Za-z0-9_~-][A-Za-z0-9._~-]*)+$`)

// fromProxy сообщает, что запрос пришёл непосредственно от доверенного прокси.
func (s *Server) fromProxy(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && s.isTrusted(ip)
}

// BasePath — путь, под которым прокси публикует webmail (X-Forwarded-Prefix
// от доверенного прокси, например "/webmail"), без "/" в конце; "" — корень.
// Прокси отрезает префикс сам: ядро видит пути от корня.
func (s *Server) BasePath(r *http.Request) string {
	p := strings.TrimRight(r.Header.Get("X-Forwarded-Prefix"), "/")
	if p == "" || !prefixRE.MatchString(p) || strings.Contains(p, "/.") || !s.fromProxy(r) {
		return ""
	}
	return p
}

// requestOrigin — схема и адрес, по которым браузер открыл webmail, если
// server.multi_host включён и запрос пришёл от доверенного прокси.
func (s *Server) requestOrigin(r *http.Request) (scheme, host string, ok bool) {
	if !s.cfg.Server.MultiHost || !s.fromProxy(r) || r.Host == "" {
		return "", "", false
	}
	scheme = "http"
	if s.secure {
		scheme = "https"
	}
	switch p := strings.ToLower(r.Header.Get("X-Forwarded-Proto")); p {
	case "http", "https":
		scheme = p
	}
	return scheme, r.Host, true
}
