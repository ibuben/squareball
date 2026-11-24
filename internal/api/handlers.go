package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/ibuben/squareball/ext"
	"github.com/ibuben/squareball/internal/mail"
	"github.com/ibuben/squareball/internal/password"
	"github.com/ibuben/squareball/internal/sanitize"
)

func (s *Server) routes(static http.Handler) {
	m := s.mux
	m.HandleFunc("GET /api/v1/config", s.handleConfig)
	m.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "ok") })
	m.HandleFunc("POST /api/v1/auth/login", s.handleLogin)
	m.HandleFunc("POST /api/v1/auth/logout", s.authed(s.handleLogout))
	m.HandleFunc("GET /api/v1/auth/me", s.authed(s.handleMe))
	m.HandleFunc("GET /api/v1/folders", s.authed(s.handleFolders))
	m.HandleFunc("GET /api/v1/events", s.authed(s.handleEvents))
	m.HandleFunc("GET /api/v1/messages", s.authed(s.handleList))
	m.HandleFunc("GET /api/v1/message", s.authed(s.handleMessage))
	m.HandleFunc("GET /api/v1/part", s.authed(s.handlePart))
	m.HandleFunc("GET /api/v1/raw", s.authed(s.handleRaw))
	m.HandleFunc("POST /api/v1/messages/flags", s.authed(s.handleFlags))
	m.HandleFunc("POST /api/v1/messages/move", s.authed(s.handleMove))
	m.HandleFunc("POST /api/v1/messages/delete", s.authed(s.handleDelete))
	m.HandleFunc("GET /api/v1/sieve", s.authed(s.handleGetSieve))
	m.HandleFunc("PUT /api/v1/sieve", s.authed(s.handlePutSieve))
	s.contactRoutes()
	s.folderRoutes()
	s.prefRoutes()
	s.templateRoutes()
	m.HandleFunc("GET /api/v1/identities", s.authed(s.handleIdentities))
	m.HandleFunc("POST /api/v1/identities", s.authed(s.handleSaveIdentity))
	m.HandleFunc("POST /api/v1/identities/delete", s.authed(s.handleDeleteIdentity))
	m.HandleFunc("POST /api/v1/send", s.authed(s.handleSend))
	m.HandleFunc("POST /api/v1/drafts", s.authed(s.handleSaveDraft))
	m.HandleFunc("POST /api/v1/drafts/discard", s.authed(s.handleDiscardDraft))
	m.HandleFunc("GET /api/v1/account/password", s.authed(s.handlePasswordInfo))
	m.HandleFunc("POST /api/v1/account/password", s.authed(s.handlePassword))
	if static != nil {
		m.Handle("/", static)
	}
}

func (s *Server) brandFor(r *http.Request) ext.Brand {
	s.mu.RLock()
	bp := s.brand
	s.mu.RUnlock()
	if bp != nil {
		if b, err := bp.Brand(r.Context(), r.Host); err == nil {
			return b
		}
	}
	ub := s.cfg.UI.Brand
	return ext.Brand{Name: ub.Name, LogoURL: ub.LogoURL, CSSVars: ub.CSSVars}
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"brand":            s.brandFor(r),
		"theme":            s.cfg.UI.Theme,
		"default_language": s.cfg.UI.DefaultLanguage,
		"languages":        s.cfg.UI.Languages,
		"default_domain":   s.cfg.Login.DefaultDomain,
	})
}

// --- вход и выход ------------------------------------------------------------

func (s *Server) loginName(input string) (string, error) {
	u := strings.TrimSpace(strings.ToLower(input))
	if u == "" {
		return "", errors.New("empty")
	}
	if !strings.Contains(u, "@") && s.cfg.Login.DefaultDomain != "" {
		u += "@" + strings.ToLower(s.cfg.Login.DefaultDomain)
	}
	local, domain, _ := strings.Cut(u, "@")
	if len(s.cfg.Login.AllowedDomains) > 0 {
		ok := false
		for _, d := range s.cfg.Login.AllowedDomains {
			if strings.EqualFold(d, domain) {
				ok = true
			}
		}
		if !ok {
			return "", errors.New("domain not allowed")
		}
	}
	r := strings.NewReplacer("%u", u, "%l", local, "%d", domain)
	return r.Replace(s.cfg.Login.UsernameFormat), nil
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := readJSON(r, &in); err != nil || in.Password == "" {
		writeErr(w, http.StatusBadRequest, "auth.invalid", "")
		return
	}
	ip := s.ClientIP(r)
	if !s.sameOrigin(r) {
		writeErr(w, http.StatusForbidden, "csrf", "")
		return
	}
	user, err := s.loginName(in.Username)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "auth.failed", "")
		return
	}
	if !s.loginIP.Allow(ip) || !s.loginUser.Allow(user) {
		s.audit.Warn("login rate limited", "mailbox", user, "ip", ip)
		writeErr(w, http.StatusTooManyRequests, "auth.locked", "")
		return
	}
	c, err := mail.DialIMAP(&s.cfg.IMAP, mail.Creds{Username: user, Password: in.Password})
	if err != nil {
		if errors.Is(err, mail.ErrAuth) {
			s.audit.Info("login failed", "mailbox", user, "ip", ip)
			writeErr(w, http.StatusUnauthorized, "auth.failed", "")
		} else {
			s.log.Error("imap unavailable at login", "err", err)
			writeErr(w, http.StatusBadGateway, "mail.unavailable", "")
		}
		return
	}
	s.loginUser.Reset(user)
	sess, cookie, err := s.sessions.Create(user, in.Password, ext.AuthPassword, "password", ip, r.UserAgent())
	if err != nil {
		c.Close()
		writeErr(w, http.StatusInternalServerError, "internal", "")
		return
	}
	s.pool.Adopt(sess.ID, c)
	s.setCookie(w, cookie)
	s.audit.Info("login", "mailbox", user, "source", "password", "ip", ip)
	writeJSON(w, http.StatusOK, s.meBody(r, sess.Mailbox, sess.CSRF))
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	s.sessions.Delete(sess.ID)
	s.clearCookie(w)
	s.audit.Info("logout", "mailbox", sess.Mailbox, "ip", s.ClientIP(r))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) meBody(r *http.Request, mailbox, csrf string) map[string]any {
	pw := s.PasswordEnabled()
	return map[string]any{
		"mailbox": mailbox,
		"csrf":    csrf,
		"brand":   s.brandFor(r),
		"features": map[string]bool{
			"password_change": pw,
			"sieve":           s.cfg.Sieve.Enabled,
		},
		"limits": map[string]int64{
			"max_upload":     int64(s.cfg.UI.Attachments.MaxUpload),
			"max_recipients": int64(s.cfg.SMTP.RateLimit.MaxRecipients),
		},
		"remote_images": s.cfg.UI.Attachments.RemoteImages,
	}
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	writeJSON(w, http.StatusOK, s.meBody(r, sess.Mailbox, sess.CSRF))
}

// --- папки и письма ------------------------------------------------------------

func (s *Server) folders(c *imapclient.Client) ([]mail.Folder, error) {
	return mail.ListFolders(c, s.cfg.IMAP.Folders.Detect, s.cfg.IMAP.Folders.Fallback)
}

func (s *Server) handleFolders(w http.ResponseWriter, r *http.Request) {
	var out []mail.Folder
	if s.withIMAP(w, r, func(c *imapclient.Client) (err error) {
		out, err = s.folders(c)
		return err
	}) {
		writeJSON(w, http.StatusOK, map[string]any{"folders": out})
	}
}

func intParam(r *http.Request, key string, def, max int) int {
	v, err := strconv.Atoi(r.URL.Query().Get(key))
	if err != nil || v < 0 {
		return def
	}
	if max > 0 && v > max {
		return max
	}
	return v
}

func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	q := mail.ListQuery{
		Folder: r.URL.Query().Get("folder"),
		Offset: intParam(r, "offset", 0, 0),
		Limit:  intParam(r, "limit", 50, 200),
		Text:   r.URL.Query().Get("q"),
		Filter: r.URL.Query().Get("filter"),
	}
	if q.Folder == "" {
		q.Folder = "INBOX"
	}
	var page *mail.Page
	if s.withIMAP(w, r, func(c *imapclient.Client) (err error) {
		page, err = mail.ListMessages(c, q)
		return err
	}) {
		writeJSON(w, http.StatusOK, page)
	}
}

func uidParam(r *http.Request) (string, uint32, bool) {
	folder := r.URL.Query().Get("folder")
	uid, err := strconv.ParseUint(r.URL.Query().Get("uid"), 10, 32)
	return folder, uint32(uid), folder != "" && err == nil && uid > 0
}

func (s *Server) handleMessage(w http.ResponseWriter, r *http.Request) {
	folder, uid, ok := uidParam(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "invalid", "")
		return
	}
	var m *mail.Message
	if !s.withIMAP(w, r, func(c *imapclient.Client) (err error) {
		m, err = mail.GetMessage(c, folder, uid, r.URL.Query().Get("peek") != "1")
		return err
	}) {
		return
	}
	allowRemote := s.cfg.UI.Attachments.RemoteImages == "always" ||
		(s.cfg.UI.Attachments.RemoteImages == "ask" && r.URL.Query().Get("images") == "1")
	cids := map[string]int{}
	for _, a := range m.Attachments {
		if a.ContentID != "" {
			cids[a.ContentID] = a.Index
		}
	}
	partURL := func(cid string) string {
		i, ok := cids[cid]
		if !ok {
			return ""
		}
		return fmt.Sprintf("/api/v1/part?folder=%s&uid=%d&index=%d", url.QueryEscape(folder), uid, i)
	}
	var body sanitize.Result
	if m.HTML != "" {
		body = sanitize.Email(m.HTML, partURL, allowRemote)
	} else {
		body = sanitize.Result{HTML: sanitize.Text(m.Text)}
	}
	text := m.Text
	if text == "" && m.HTML != "" {
		text = mail.HTMLToText(m.HTML)
	}
	m.HTML, m.Text = "", ""
	writeJSON(w, http.StatusOK, map[string]any{
		"message":        m,
		"body_html":      body.HTML,
		"body_text":      text, // для цитирования при ответе
		"blocked_images": body.BlockedImages,
	})
}

func (s *Server) handlePart(w http.ResponseWriter, r *http.Request) {
	folder, uid, ok := uidParam(r)
	index, err := strconv.Atoi(r.URL.Query().Get("index"))
	if !ok || err != nil {
		writeErr(w, http.StatusBadRequest, "invalid", "")
		return
	}
	var att *mail.Attachment
	var data []byte
	if !s.withIMAP(w, r, func(c *imapclient.Client) (err error) {
		att, data, err = mail.GetPart(c, folder, uid, index)
		return err
	}) {
		return
	}
	ct := att.ContentType
	if ct == "" {
		ct = "application/octet-stream"
	}
	// Встраиваем только картинки; всё остальное — только скачиванием,
	// чтобы HTML/SVG из вложения не исполнялся на нашем origin.
	disp := "attachment"
	if r.URL.Query().Get("download") != "1" && strings.HasPrefix(ct, "image/") && ct != "image/svg+xml" {
		disp = "inline"
	} else if disp == "attachment" {
		ct = safeDownloadType(ct)
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disp, map[string]string{"filename": att.Filename}))
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.Write(data)
}

func safeDownloadType(ct string) string {
	if strings.HasPrefix(ct, "text/html") || strings.Contains(ct, "xml") || strings.Contains(ct, "javascript") {
		return "application/octet-stream"
	}
	return ct
}

func (s *Server) handleRaw(w http.ResponseWriter, r *http.Request) {
	folder, uid, ok := uidParam(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "invalid", "")
		return
	}
	var raw []byte
	if s.withIMAP(w, r, func(c *imapclient.Client) (err error) {
		raw, err = mail.GetRaw(c, folder, uid)
		return err
	}) {
		w.Header().Set("Content-Type", "message/rfc822")
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": fmt.Sprintf("message-%d.eml", uid)}))
		w.Write(raw)
	}
}

type batchReq struct {
	Folder string   `json:"folder"`
	UIDs   []uint32 `json:"uids"`
	Flags  []string `json:"flags,omitempty"`
	Add    bool     `json:"add,omitempty"`
	Dest   string   `json:"dest,omitempty"`
}

var allowedFlags = map[string]bool{
	string(imap.FlagSeen): true, string(imap.FlagFlagged): true, string(imap.FlagAnswered): true,
	string(imap.FlagForwarded): true, string(imap.FlagJunk): true, string(imap.FlagNotJunk): true,
}

func (s *Server) readBatch(w http.ResponseWriter, r *http.Request) (*batchReq, bool) {
	var in batchReq
	if err := readJSON(r, &in); err != nil || in.Folder == "" || len(in.UIDs) == 0 || len(in.UIDs) > 5000 {
		writeErr(w, http.StatusBadRequest, "invalid", "")
		return nil, false
	}
	return &in, true
}

func (s *Server) handleFlags(w http.ResponseWriter, r *http.Request) {
	in, ok := s.readBatch(w, r)
	if !ok {
		return
	}
	for _, f := range in.Flags {
		if !allowedFlags[f] {
			writeErr(w, http.StatusBadRequest, "invalid", "flag "+f)
			return
		}
	}
	if s.withIMAP(w, r, func(c *imapclient.Client) error {
		return mail.SetFlags(c, in.Folder, in.UIDs, in.Flags, in.Add)
	}) {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

func (s *Server) handleMove(w http.ResponseWriter, r *http.Request) {
	in, ok := s.readBatch(w, r)
	if !ok || in.Dest == "" {
		if ok {
			writeErr(w, http.StatusBadRequest, "invalid", "")
		}
		return
	}
	if s.withIMAP(w, r, func(c *imapclient.Client) error {
		if role, ok := strings.CutPrefix(in.Dest, "role:"); ok { // "role:archive" → имя папки
			fs, err := s.folders(c)
			if err != nil {
				return err
			}
			if in.Dest = mail.FolderByRole(fs, role); in.Dest == "" {
				return mail.ErrNotFound
			}
		}
		return mail.Move(c, in.Folder, in.UIDs, in.Dest)
	}) {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

// handleDelete: из обычной папки — в Корзину; из Корзины и Спама — навсегда.
func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	in, ok := s.readBatch(w, r)
	if !ok {
		return
	}
	permanent := false
	if s.withIMAP(w, r, func(c *imapclient.Client) error {
		fs, err := s.folders(c)
		if err != nil {
			return err
		}
		trash := mail.FolderByRole(fs, mail.RoleTrash)
		if trash == "" || in.Folder == trash || in.Folder == mail.FolderByRole(fs, mail.RoleJunk) {
			permanent = true
			return mail.Expunge(c, in.Folder, in.UIDs)
		}
		return mail.Move(c, in.Folder, in.UIDs, trash)
	}) {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true, "permanent": permanent})
	}
}

// --- отправка и черновики ------------------------------------------------------

type msgRef struct {
	Folder string `json:"folder"`
	UID    uint32 `json:"uid"`
}

type sendReq struct {
	To         []mail.Address `json:"to"`
	Cc         []mail.Address `json:"cc"`
	Bcc        []mail.Address `json:"bcc"`
	Subject    string         `json:"subject"`
	Text       string         `json:"text"`
	HTML       string         `json:"html"`
	InReplyTo  string         `json:"in_reply_to"`
	References []string       `json:"references"`
	// Исходное письмо: после отправки ему ставится \Answered или $Forwarded.
	Source *struct {
		msgRef
		Mode string `json:"mode"` // reply | forward
	} `json:"source"`
	// Прежняя версия черновика: заменяется при сохранении, удаляется после отправки.
	DraftRef *msgRef `json:"draft_ref"`
	// Индексы вложений из DraftRef, которые остаются в письме.
	KeepAttachments []int `json:"keep_attachments"`
	// Отправитель из /identities; 0 — по умолчанию.
	IdentityID int64 `json:"identity_id"`
	// Важность: 1 — высокая, 5 — низкая, 0 — обычная.
	Priority    int  `json:"priority"`
	ReadReceipt bool `json:"read_receipt"`
	DSN         bool `json:"dsn"`
}

// readCompose разбирает multipart-форму письма (поле draft + файлы attachments)
// и подтягивает оставленные вложения из прежнего черновика.
func (s *Server) readCompose(w http.ResponseWriter, r *http.Request, forDraft bool) (*sendReq, *mail.Draft, bool) {
	sess := sessionFrom(r.Context())
	maxUpload := int64(s.cfg.UI.Attachments.MaxUpload)
	r.Body = http.MaxBytesReader(w, r.Body, maxUpload+1<<20)
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		writeErr(w, http.StatusRequestEntityTooLarge, "send.too_large", "")
		return nil, nil, false
	}
	var in sendReq
	if err := json.Unmarshal([]byte(r.FormValue("draft")), &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid", "")
		return nil, nil, false
	}
	d := &mail.Draft{
		From: mail.Address{Email: sess.Mailbox}, To: in.To, Cc: in.Cc, Bcc: in.Bcc,
		Subject: in.Subject, Text: in.Text, InReplyTo: in.InReplyTo, References: in.References,
		KeepBcc: forDraft, ReadReceipt: in.ReadReceipt, DSN: in.DSN,
	}
	if in.Priority == 1 || in.Priority == 5 {
		d.Priority = in.Priority
	}
	if err := s.applyIdentity(r, in.IdentityID, d); err != nil {
		s.storeError(w, err)
		return nil, nil, false
	}
	var total int64
	if r.MultipartForm != nil {
		for _, fh := range r.MultipartForm.File["attachments"] {
			total += fh.Size
			if total > maxUpload {
				writeErr(w, http.StatusRequestEntityTooLarge, "send.too_large", "")
				return nil, nil, false
			}
			f, err := fh.Open()
			if err != nil {
				writeErr(w, http.StatusBadRequest, "invalid", "")
				return nil, nil, false
			}
			data, _ := io.ReadAll(f)
			f.Close()
			d.Attachments = append(d.Attachments, mail.Upload{Filename: fh.Filename, ContentType: fh.Header.Get("Content-Type"), Data: data})
		}
	}
	if in.DraftRef != nil && len(in.KeepAttachments) > 0 {
		ok := s.withIMAP(w, r, func(c *imapclient.Client) error {
			for _, idx := range in.KeepAttachments {
				att, data, err := mail.GetPart(c, in.DraftRef.Folder, in.DraftRef.UID, idx)
				if err != nil {
					return err
				}
				total += int64(len(data))
				d.Attachments = append(d.Attachments, mail.Upload{Filename: att.Filename, ContentType: att.ContentType, Data: data})
			}
			return nil
		})
		if !ok {
			return nil, nil, false
		}
		if total > maxUpload {
			writeErr(w, http.StatusRequestEntityTooLarge, "send.too_large", "")
			return nil, nil, false
		}
	}
	if in.HTML != "" {
		html, inline, size, ok := s.inlineImages(w, r, in.HTML)
		if !ok {
			return nil, nil, false
		}
		if total += size; total > maxUpload {
			writeErr(w, http.StatusRequestEntityTooLarge, "send.too_large", "")
			return nil, nil, false
		}
		d.HTML, d.Inline = html, inline
	}
	return &in, d, true
}

func (s *Server) handleSend(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	in, d, ok := s.readCompose(w, r, false)
	if !ok {
		return
	}
	rcpts := d.Recipients()
	if len(rcpts) == 0 {
		writeErr(w, http.StatusBadRequest, "send.no_recipients", "")
		return
	}
	if max := s.cfg.SMTP.RateLimit.MaxRecipients; max > 0 && len(rcpts) > max {
		writeErr(w, http.StatusBadRequest, "send.too_many_recipients", strconv.Itoa(max))
		return
	}
	if !s.sendUser.Allow(sess.Mailbox) {
		writeErr(w, http.StatusTooManyRequests, "send.rate_limited", "")
		return
	}
	raw, _, err := mail.Build(d)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid", err.Error())
		return
	}
	if int64(len(raw)) > int64(s.cfg.SMTP.MaxMessageSize) {
		writeErr(w, http.StatusRequestEntityTooLarge, "send.too_large", "")
		return
	}
	cr := s.smtpCreds(sess)
	if cr.Username == "" {
		writeErr(w, http.StatusForbidden, "send.not_allowed", "")
		return
	}
	if err := mail.Send(&s.cfg.SMTP, cr, d.From.Email, rcpts, raw, d.DSN); err != nil {
		s.mailError(w, r, err)
		return
	}
	s.audit.Info("message sent", "mailbox", sess.Mailbox, "recipients", len(rcpts), "size", len(raw), "ip", s.ClientIP(r))
	s.collectRecipients(r, d)

	// Копия в «Отправленные», флаг исходному письму, удаление черновика.
	// Ошибки здесь не отменяют отправку.
	warn := ""
	_ = s.pool.With(sess.ID, s.creds(sess), func(c *imapclient.Client) error {
		if s.cfg.SMTP.SaveSent {
			fs, err := s.folders(c)
			if err == nil {
				if sent := mail.FolderByRole(fs, mail.RoleSent); sent != "" {
					if _, err := mail.Append(c, sent, raw, imap.FlagSeen); err != nil {
						warn = "sent_copy_failed"
					}
				}
			}
		}
		if in.Source != nil && in.Source.UID > 0 {
			flag := string(imap.FlagAnswered)
			if in.Source.Mode == "forward" {
				flag = string(imap.FlagForwarded)
			}
			_ = mail.SetFlags(c, in.Source.Folder, []uint32{in.Source.UID}, []string{flag}, true)
		}
		if in.DraftRef != nil && in.DraftRef.UID > 0 {
			_ = mail.Expunge(c, in.DraftRef.Folder, []uint32{in.DraftRef.UID})
		}
		return nil
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "warning": warn})
}

// handleSaveDraft кладёт черновик в «Черновики» и удаляет его прежнюю версию.
// Получатели не обязательны. Возвращает новую ссылку на черновик.
func (s *Server) handleSaveDraft(w http.ResponseWriter, r *http.Request) {
	in, d, ok := s.readCompose(w, r, true)
	if !ok {
		return
	}
	raw, _, err := mail.Build(d)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid", err.Error())
		return
	}
	var ref msgRef
	if s.withIMAP(w, r, func(c *imapclient.Client) error {
		fs, err := s.folders(c)
		if err != nil {
			return err
		}
		drafts := mail.FolderByRole(fs, mail.RoleDrafts)
		if drafts == "" {
			return mail.ErrNoDrafts
		}
		uid, err := mail.Append(c, drafts, raw, imap.FlagDraft, imap.FlagSeen)
		if err != nil {
			return err
		}
		ref = msgRef{Folder: drafts, UID: uid}
		// Старую версию удаляем только после успешной записи новой.
		if in.DraftRef != nil && in.DraftRef.UID > 0 && in.DraftRef.UID != uid {
			_ = mail.Expunge(c, in.DraftRef.Folder, []uint32{in.DraftRef.UID})
		}
		return nil
	}) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "draft_ref": ref})
	}
}

// handleDiscardDraft удаляет черновик навсегда (как «Удалить черновик» в Roundcube).
func (s *Server) handleDiscardDraft(w http.ResponseWriter, r *http.Request) {
	var in msgRef
	if err := readJSON(r, &in); err != nil || in.Folder == "" || in.UID == 0 {
		writeErr(w, http.StatusBadRequest, "invalid", "")
		return
	}
	if s.withIMAP(w, r, func(c *imapclient.Client) error {
		return mail.Expunge(c, in.Folder, []uint32{in.UID})
	}) {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

// --- смена пароля ------------------------------------------------------------------

// handlePasswordInfo отдаёт интерфейсу правила нового пароля.
func (s *Server) handlePasswordInfo(w http.ResponseWriter, r *http.Request) {
	pc, ok := s.passwordChanger()
	if !ok {
		writeErr(w, http.StatusNotFound, "password.disabled", "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"policy": password.FromConfig(&s.cfg.Password, pc.Capabilities().Policy),
	})
}

func (s *Server) handlePassword(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	pc, ok := s.passwordChanger()
	if !ok {
		writeErr(w, http.StatusNotFound, "password.disabled", "")
		return
	}
	var in struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if err := readJSON(r, &in); err != nil || in.New == "" {
		writeErr(w, http.StatusBadRequest, "invalid", "")
		return
	}
	caps := pc.Capabilities()
	if in.Current == "" {
		writeErr(w, http.StatusBadRequest, "password.current_required", "")
		return
	}
	var v *password.Violation
	if err := password.FromConfig(&s.cfg.Password, caps.Policy).Check(sess.Mailbox, in.New); errors.As(err, &v) {
		writeErr(w, http.StatusBadRequest, "password.weak", v.Reason)
		return
	}
	if in.New == in.Current {
		writeErr(w, http.StatusBadRequest, "password.weak", "same")
		return
	}
	// Лимит — перед проверкой текущего пароля: это и есть попытка подбора.
	// Отказы по политике выше его не расходуют.
	if !s.loginUser.Allow("pw:" + sess.Mailbox) {
		writeErr(w, http.StatusTooManyRequests, "auth.locked", "")
		return
	}
	if !caps.VerifiesCurrent {
		c, err := mail.DialIMAP(&s.cfg.IMAP, mail.Creds{Username: sess.Mailbox, Password: in.Current})
		if err != nil {
			if errors.Is(err, mail.ErrAuth) {
				writeErr(w, http.StatusForbidden, "password.current", "")
			} else {
				s.log.Error("password: verify current", "err", err)
				writeErr(w, http.StatusBadGateway, "password.unavailable", "")
			}
			return
		}
		c.Close()
	}
	if s.cfg.Password.Policy.CheckBreached {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		leaked, err := password.Breached(ctx, s.pwnedClient, in.New)
		cancel()
		switch {
		case err != nil:
			// Сервис утечек недоступен — не мешаем смене пароля, только пишем в журнал.
			s.log.Warn("password: breach check failed", "err", err)
		case leaked:
			writeErr(w, http.StatusBadRequest, "password.weak", "breached")
			return
		}
	}
	// Смену не прерываем, если клиент закрыл соединение: драйвер ограничен
	// своим тайм-аутом, а половина смены хуже, чем лишняя секунда ожидания.
	err := pc.Change(context.WithoutCancel(r.Context()), ext.PasswordChange{
		Mailbox: sess.Mailbox, Current: in.Current, New: in.New, RemoteIP: s.ClientIP(r),
	})
	switch {
	case err == nil:
		if sess.AuthMode == ext.AuthPassword {
			_ = s.sessions.UpdatePassword(sess, in.New)
		}
		s.pool.Close(sess.ID) // следующий запрос переподключится с новым паролем
		s.sessions.DeleteMailbox(sess.Mailbox, sess.ID)
		s.audit.Info("password changed", "mailbox", sess.Mailbox, "ip", s.ClientIP(r))
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	case errors.Is(err, ext.ErrWrongPassword):
		s.audit.Info("password change rejected", "mailbox", sess.Mailbox, "reason", "current", "ip", s.ClientIP(r))
		writeErr(w, http.StatusForbidden, "password.current", "")
	case errors.Is(err, ext.ErrWeakPassword):
		writeErr(w, http.StatusBadRequest, "password.weak", "backend")
	case errors.Is(err, ext.ErrRateLimited):
		writeErr(w, http.StatusTooManyRequests, "auth.locked", "")
	default:
		s.log.Error("password change failed", "err", err, "mailbox", sess.Mailbox)
		writeErr(w, http.StatusBadGateway, "password.unavailable", "")
	}
}
