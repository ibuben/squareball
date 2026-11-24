package api

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/ibuben/squareball/ext"
	"github.com/ibuben/squareball/internal/config"
)

// brandAsset — файл бренда в памяти. Версия (часть хеша) входит в ссылку
// /brand/<name>?v=<version>, поэтому файл можно кешировать надолго.
type brandAsset struct {
	data    []byte
	ctype   string
	version string
}

func loadBrandAssets(b *config.Brand) (map[string]brandAsset, error) {
	out := map[string]brandAsset{}
	for _, a := range b.Assets() {
		if a.File == "" {
			continue
		}
		ctype := config.BrandFileTypes[strings.ToLower(filepath.Ext(a.File))]
		data, err := os.ReadFile(a.File)
		if err != nil {
			return nil, fmt.Errorf("ui.brand.%s_file: %w", a.Key, err)
		}
		if ctype == "" || len(data) > config.MaxBrandFile {
			return nil, fmt.Errorf("ui.brand.%s_file: unsupported type or too large", a.Key)
		}
		sum := sha256.Sum256(data)
		out[a.Name] = brandAsset{data: data, ctype: ctype, version: hex.EncodeToString(sum[:6])}
	}
	return out, nil
}

// configBrand — оформление из ui.brand; файлы превращаются в ссылки /brand/<name>.
func (s *Server) configBrand(base string) ext.Brand {
	ub := &s.cfg.UI.Brand
	b := ext.Brand{Name: ub.Name, Accent: ub.Accent, AccentDark: ub.AccentDark, CSSVars: ub.CSSVars, HideAbout: ub.HideAbout}
	urls := []*string{&b.LogoURL, &b.LogoDarkURL, &b.FaviconURL}
	for i, a := range ub.Assets() {
		*urls[i] = a.URL
		if f, ok := s.brandAssets[a.Name]; ok {
			*urls[i] = base + "/brand/" + a.Name + "?v=" + f.version
		}
	}
	return b
}

// brandFor — оформление для хоста запроса: поля провайдера адаптера поверх ui.brand.
// Некорректные значения провайдера отбрасываются — их проверяет только конфиг.
func (s *Server) brandFor(r *http.Request) ext.Brand {
	b := s.configBrand(s.BasePath(r))
	s.mu.RLock()
	bp := s.brand
	s.mu.RUnlock()
	if bp == nil {
		return b
	}
	p, err := bp.Brand(r.Context(), r.Host)
	if err != nil {
		s.log.Warn("brand provider failed", "host", r.Host, "err", err)
		return b
	}
	set := func(dst *string, v string, ok func(string) bool) {
		if v != "" && ok(v) {
			*dst = v
		}
	}
	always := func(string) bool { return true }
	set(&b.Name, strings.TrimSpace(p.Name), always)
	set(&b.LogoURL, p.LogoURL, config.ValidBrandURL)
	set(&b.LogoDarkURL, p.LogoDarkURL, config.ValidBrandURL)
	set(&b.FaviconURL, p.FaviconURL, config.ValidBrandURL)
	set(&b.Accent, p.Accent, config.ValidHexColor)
	set(&b.AccentDark, p.AccentDark, config.ValidHexColor)
	b.HideAbout = b.HideAbout || p.HideAbout
	if len(p.CSSVars) > 0 {
		vars := make(map[string]string, len(b.CSSVars)+len(p.CSSVars))
		for k, v := range b.CSSVars {
			vars[k] = v
		}
		for k, v := range p.CSSVars {
			if config.ValidCSSVar(k, v) {
				vars[k] = v
			}
		}
		b.CSSVars = vars
	}
	return b
}

func (s *Server) handleBrandAsset(w http.ResponseWriter, r *http.Request) {
	a, ok := s.brandAssets[r.PathValue("name")]
	if !ok {
		http.NotFound(w, r)
		return
	}
	h := w.Header()
	h.Set("Content-Type", a.ctype)
	// SVG, открытый напрямую, — документ: без скриптов и внешних ресурсов.
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	if r.URL.Query().Get("v") == a.version {
		h.Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		h.Set("Cache-Control", "no-cache")
	}
	etag := `"` + a.version + `"`
	h.Set("ETag", etag)
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	_, _ = w.Write(a.data)
}
