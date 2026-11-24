package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/ibuben/squareball/internal/netguard"
)

// Прокси внешних картинок в письмах. Браузер не обращается к серверу
// отправителя напрямую: тот не узнаёт IP, браузер и время прочтения
// (трекинговые пиксели видят только сервер почты). Ссылки подписаны HMAC,
// поэтому прокси не открыт для произвольных адресов; запросы во внутренние
// сети запрещены (SSRF).

const (
	maxProxiedImage = 15 << 20
	proxyParallel   = 32
)

func (s *Server) imgKey() []byte {
	k := sha256.Sum256(append([]byte("webmail image proxy\x00"), s.sessionSecret...))
	return k[:]
}

func (s *Server) imgSig(u string) string {
	m := hmac.New(sha256.New, s.imgKey())
	m.Write([]byte(u))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil)[:18])
}

// proxyImageURL — ссылка на картинку через прокси.
func (s *Server) proxyImageURL(u string) string {
	return "/api/v1/img?u=" + url.QueryEscape(u) + "&s=" + s.imgSig(u)
}

// unproxyImageURL возвращает исходный адрес из ссылки прокси (для ответа
// с цитатой: получателю нужны настоящие адреса). ok=false — не ссылка прокси.
func (s *Server) unproxyImageURL(link string) (string, bool) {
	if !strings.HasPrefix(link, "/api/v1/img?") {
		return "", false
	}
	q, err := url.ParseQuery(link[len("/api/v1/img?"):])
	if err != nil {
		return "", false
	}
	u := q.Get("u")
	if u == "" || !hmac.Equal([]byte(q.Get("s")), []byte(s.imgSig(u))) {
		return "", false
	}
	return u, true
}

func (s *Server) handleImageProxy(w http.ResponseWriter, r *http.Request) {
	u := r.URL.Query().Get("u")
	if u == "" || !hmac.Equal([]byte(r.URL.Query().Get("s")), []byte(s.imgSig(u))) {
		writeErr(w, http.StatusForbidden, "forbidden", "")
		return
	}
	pu, err := url.Parse(u)
	if err != nil || (pu.Scheme != "http" && pu.Scheme != "https") || pu.Host == "" {
		writeErr(w, http.StatusBadRequest, "invalid", "")
		return
	}
	select {
	case s.imgSem <- struct{}{}:
		defer func() { <-s.imgSem }()
	default:
		writeErr(w, http.StatusServiceUnavailable, "busy", "")
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, u, nil)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid", "")
		return
	}
	// Ничего о пользователе: без cookie, Referer и языка браузера.
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; webmail-image-proxy)")
	req.Header.Set("Accept", "image/avif,image/webp,image/png,image/jpeg,image/gif,image/*;q=0.8")
	resp, err := s.imgClient.Do(req)
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, netguard.ErrPrivate) {
			status = http.StatusForbidden
		}
		if r.Context().Err() == nil {
			s.log.Warn("image proxy: fetch failed", "host", pu.Host, "err", err)
		}
		w.WriteHeader(status)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		s.log.Warn("image proxy: upstream status", "host", pu.Host, "status", resp.StatusCode)
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	ct, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	// SVG может содержать скрипты при открытии по прямой ссылке — не отдаём.
	if !strings.HasPrefix(ct, "image/") || strings.Contains(ct, "svg") {
		s.log.Info("image proxy: not an image", "host", pu.Host, "type", ct)
		w.WriteHeader(http.StatusUnsupportedMediaType)
		return
	}
	if resp.ContentLength > maxProxiedImage {
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		return
	}
	h := w.Header()
	h.Set("Content-Type", ct)
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	h.Set("Content-Disposition", "inline")
	// Перекрываем no-store из securityHeaders: картинку можно держать в кэше браузера.
	h.Set("Cache-Control", "private, max-age=86400")
	if resp.ContentLength > 0 {
		h.Set("Content-Length", strconv.FormatInt(resp.ContentLength, 10))
	}
	_, _ = io.Copy(w, io.LimitReader(resp.Body, maxProxiedImage))
}
