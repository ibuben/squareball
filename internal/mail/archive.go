package mail

import (
	"archive/zip"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// Структура архива — как archive_type в Roundcube.
const (
	ArchiveFlat  = ""      // всё в «Архив»
	ArchiveYear  = "year"  // Архив/2024
	ArchiveMonth = "month" // Архив/2024/05
)

// Archive переносит письма в папку архива; при структуре year/month — в подпапки
// по дате письма, создавая их при необходимости. Возвращает число перенесённых писем.
func Archive(c *imapclient.Client, folders []Folder, folder string, uids []uint32, structure string) (int, error) {
	root := FolderByRole(folders, RoleArchive)
	if root == "" {
		return 0, ErrNoArchive
	}
	delim := delimiter(folders)
	if structure == ArchiveFlat || delim == "" {
		if folder == root {
			return 0, nil
		}
		return len(uids), Move(c, folder, uids, root)
	}
	if _, err := c.Select(folder, nil).Wait(); err != nil {
		return 0, err
	}
	msgs, err := c.Fetch(uidSet(uids), &imap.FetchOptions{UID: true, Envelope: true, InternalDate: true}).Collect()
	if err != nil {
		return 0, err
	}
	groups := map[string][]uint32{}
	for _, m := range msgs {
		d := m.InternalDate
		if m.Envelope != nil && !m.Envelope.Date.IsZero() {
			d = m.Envelope.Date
		}
		dest := archivePath(root, delim, structure, d)
		if strings.HasPrefix(folder, root+delim) && dest == folder {
			continue // уже на месте
		}
		groups[dest] = append(groups[dest], uint32(m.UID))
	}
	existing := map[string]bool{}
	for _, f := range folders {
		existing[f.Name] = true
	}
	dests := make([]string, 0, len(groups))
	for d := range groups {
		dests = append(dests, d)
	}
	sort.Strings(dests)
	moved := 0
	for _, dest := range dests {
		// Создаём недостающие уровни: Архив/2024, затем Архив/2024/05.
		parts := strings.Split(strings.TrimPrefix(dest, root+delim), delim)
		cur := root
		for _, p := range parts {
			cur += delim + p
			if existing[cur] {
				continue
			}
			if err := c.Create(cur, nil).Wait(); err != nil {
				var ie *imap.Error
				if !(asIMAPError(err, &ie) && ie.Code == imap.ResponseCodeAlreadyExists) {
					return moved, err
				}
			}
			_ = c.Subscribe(cur).Wait()
			existing[cur] = true
		}
		if err := Move(c, folder, groups[dest], dest); err != nil {
			return moved, err
		}
		moved += len(groups[dest])
	}
	return moved, nil
}

func archivePath(root, delim, structure string, d time.Time) string {
	if d.IsZero() {
		d = time.Now()
	}
	p := root + delim + fmt.Sprintf("%04d", d.Year())
	if structure == ArchiveMonth {
		p += delim + fmt.Sprintf("%02d", int(d.Month()))
	}
	return p
}

// --- все вложения одним ZIP ---------------------------------------------------------

// WriteAttachmentsZip пишет в w ZIP со всеми вложениями письма (без картинок,
// встроенных в текст). Возвращает число файлов; 0 — вложений нет (w не тронут).
func WriteAttachmentsZip(c *imapclient.Client, folder string, uid uint32, w io.Writer, before func()) (int, error) {
	raw, _, err := fetchRaw(c, folder, imap.UID(uid))
	if err != nil {
		return 0, err
	}
	type file struct {
		name string
		data []byte
	}
	var files []file
	used := map[string]bool{}
	if _, err := walkParts(raw, func(_ int, att Attachment, isBody bool, _ string, body io.Reader) (bool, error) {
		if isBody || (att.Inline && att.ContentID != "") {
			return false, nil
		}
		b, err := io.ReadAll(body)
		if err != nil {
			return true, err
		}
		name := att.Filename
		if name == "" {
			name = defaultName(att)
		}
		files = append(files, file{uniqueName(safeZipName(name), used), b})
		return false, nil
	}); err != nil {
		return 0, err
	}
	if len(files) == 0 {
		return 0, nil
	}
	if before != nil {
		before()
	}
	zw := zip.NewWriter(w)
	for _, f := range files {
		// Уже сжатое (картинки, архивы, PDF) только сохраняем, остальное — Deflate.
		method := zip.Deflate
		if ext := strings.ToLower(path.Ext(f.name)); storedExt[ext] {
			method = zip.Store
		}
		fw, err := zw.CreateHeader(&zip.FileHeader{Name: f.name, Method: method, Modified: time.Now()})
		if err != nil {
			return 0, err
		}
		if _, err := fw.Write(f.data); err != nil {
			return 0, err
		}
	}
	return len(files), zw.Close()
}

var storedExt = map[string]bool{
	".zip": true, ".gz": true, ".7z": true, ".rar": true, ".jpg": true, ".jpeg": true, ".png": true,
	".gif": true, ".webp": true, ".mp3": true, ".mp4": true, ".pdf": true, ".docx": true, ".xlsx": true, ".pptx": true,
}

// SafeFileName — то же для имени скачиваемого файла.
func SafeFileName(name string) string { return safeZipName(name) }

// safeZipName убирает из имени пути, управляющие символы и «..», чтобы
// распаковка не могла выйти за пределы каталога.
func safeZipName(name string) string {
	name = strings.Map(func(r rune) rune {
		switch {
		case r < 0x20 || r == 0x7f:
			return -1
		case r == '/' || r == '\\' || r == ':':
			return '_'
		}
		return r
	}, name)
	name = strings.TrimSpace(strings.Trim(name, ". "))
	if name == "" {
		name = "attachment"
	}
	if r := []rune(name); len(r) > 200 {
		ext := path.Ext(name)
		name = string(r[:200-len([]rune(ext))]) + ext
	}
	return name
}

func uniqueName(name string, used map[string]bool) string {
	key := strings.ToLower(name)
	if !used[key] {
		used[key] = true
		return name
	}
	ext := path.Ext(name)
	base := strings.TrimSuffix(name, ext)
	for i := 2; ; i++ {
		n := fmt.Sprintf("%s (%d)%s", base, i, ext)
		if !used[strings.ToLower(n)] {
			used[strings.ToLower(n)] = true
			return n
		}
	}
}
