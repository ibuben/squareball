package api

import (
	"bytes"
	"errors"
	"io"
	"mime"
	"net/http"
	netmail "net/mail"
	"path"
	"strconv"
	"strings"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/ibuben/squareball/internal/mail"
)

const (
	maxImportSize    = 2 << 30  // весь запрос импорта
	maxImportMessage = 64 << 20 // одно письмо
)

func (s *Server) mboxRoutes() {
	s.mux.HandleFunc("GET /api/v1/folders/export", s.authed(s.handleExportMbox))
	s.mux.HandleFunc("POST /api/v1/folders/import", s.authed(s.handleImportMbox))
}

// lazyHeaders выставляет заголовки ответа только перед первой записью:
// если IMAP откажет раньше, клиент получит обычную JSON-ошибку.
type lazyHeaders struct {
	w     http.ResponseWriter
	set   func(http.Header)
	wrote bool
}

func (l *lazyHeaders) Write(b []byte) (int, error) {
	if !l.wrote {
		l.wrote = true
		l.set(l.w.Header())
	}
	return l.w.Write(b)
}

// handleExportMbox отдаёт папку (или выбранные письма, uids=1,2,3) файлом mbox.
func (s *Server) handleExportMbox(w http.ResponseWriter, r *http.Request) {
	folder := r.URL.Query().Get("folder")
	if folder == "" {
		writeErr(w, http.StatusBadRequest, "invalid", "")
		return
	}
	var uids []uint32
	if v := r.URL.Query().Get("uids"); v != "" {
		for _, p := range strings.Split(v, ",") {
			n, err := strconv.ParseUint(strings.TrimSpace(p), 10, 32)
			if err != nil || n == 0 || len(uids) >= 10000 {
				writeErr(w, http.StatusBadRequest, "invalid", "")
				return
			}
			uids = append(uids, uint32(n))
		}
	}
	name := folder
	if i := strings.LastIndexAny(name, "/."); i >= 0 && i < len(name)-1 {
		name = name[i+1:]
	}
	name = mail.SafeFileName(name) + ".mbox"
	out := &lazyHeaders{w: w, set: func(h http.Header) {
		h.Set("Content-Type", "application/mbox")
		h.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
		h.Set("Cache-Control", "private, no-store")
	}}
	n := 0
	err := s.pool.With(sessionFrom(r.Context()).ID, s.creds(sessionFrom(r.Context())), func(c *imapclient.Client) (err error) {
		n, err = mail.WriteMbox(c, folder, uids, out)
		return err
	})
	switch {
	case err != nil && !out.wrote:
		s.mailError(w, r, err)
	case err != nil:
		s.log.Error("mbox export interrupted", "err", err, "written", n) // заголовки уже ушли
	case n == 0:
		out.Write(nil) // пустой файл — тоже корректный mbox
	default:
		s.audit.Info("mbox exported", "mailbox", sessionFrom(r.Context()).Mailbox, "folder", folder, "messages", n)
	}
}

type importResult struct {
	Imported   int `json:"imported"`
	Duplicates int `json:"duplicates"`
	TooLarge   int `json:"too_large"`
	Failed     int `json:"failed"`
}

// handleImportMbox загружает письма из файлов .mbox и .eml (поле files) в папку.
// Письма с Message-ID, который уже есть в папке, пропускаются — повторный
// импорт того же файла ничего не дублирует.
func (s *Server) handleImportMbox(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	folder := r.URL.Query().Get("folder")
	if folder == "" {
		writeErr(w, http.StatusBadRequest, "invalid", "")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxImportSize)
	mr, err := r.MultipartReader()
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid", "")
		return
	}
	var res importResult
	files := 0
	ok := s.withIMAP(w, r, func(c *imapclient.Client) error {
		if _, err := c.Select(folder, nil).Wait(); err != nil {
			return err
		}
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				return nil
			}
			if err != nil {
				return errImportBody
			}
			if part.FormName() != "files" {
				continue
			}
			files++
			var rd *mail.MboxReader
			if strings.EqualFold(path.Ext(part.FileName()), ".eml") {
				// .eml — одно письмо целиком, строки "From " в нём не разделители.
				data, err := io.ReadAll(io.LimitReader(part, maxImportMessage+1))
				if err != nil {
					return errImportBody
				}
				if len(data) > maxImportMessage {
					res.TooLarge++
					continue
				}
				rd = mail.NewMboxReader(bytes.NewReader(data), 0)
				m, err := rd.Next()
				if err == nil {
					// Единственное письмо: склеиваем обратно возможные «разрывы».
					m.Raw = bytes.ReplaceAll(bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n")), []byte("\n"), []byte("\r\n"))
					s.importOne(c, folder, m, &res)
				}
				continue
			}
			rd = mail.NewMboxReader(part, maxImportMessage)
			for {
				m, err := rd.Next()
				if err == io.EOF {
					break
				}
				if errors.Is(err, mail.ErrMessageTooLarge) {
					res.TooLarge++
					continue
				}
				if err != nil {
					return errImportBody
				}
				if err := s.importOne(c, folder, m, &res); err != nil {
					return err
				}
			}
		}
	})
	if !ok {
		return
	}
	if files == 0 {
		writeErr(w, http.StatusBadRequest, "invalid", "no files")
		return
	}
	s.audit.Info("mbox imported", "mailbox", sess.Mailbox, "folder", folder, "imported", res.Imported,
		"duplicates", res.Duplicates, "too_large", res.TooLarge, "failed", res.Failed)
	writeJSON(w, http.StatusOK, res)
}

var errImportBody = &mail.SendError{Code: 400, Message: "не удалось прочитать файл"}

// importOne кладёт письмо в выбранную папку. Отказ сервера по одному письму
// не прерывает импорт; разрыв соединения — прерывает.
func (s *Server) importOne(c *imapclient.Client, folder string, m *mail.MboxMessage, res *importResult) error {
	if msg, err := netmail.ReadMessage(bytes.NewReader(m.Raw)); err == nil {
		if id := strings.TrimSpace(msg.Header.Get("Message-Id")); id != "" {
			dup, err := mail.HasMessageID(c, id)
			if err != nil {
				return err
			}
			if dup {
				res.Duplicates++
				return nil
			}
		}
	}
	if err := mail.AppendAt(c, folder, m.Raw, m.Date, m.Flags); err != nil {
		var ie *imap.Error
		if !errors.As(err, &ie) { // не отказ по письму, а сбой соединения
			return err
		}
		s.log.Warn("mbox import: message rejected", "err", err)
		res.Failed++
		return nil
	}
	res.Imported++
	return nil
}
