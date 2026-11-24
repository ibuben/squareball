package mail

import (
	"errors"
	"sort"
	"strings"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// Ошибки операций с папками; API отдаёт их как 400/409 с понятным кодом.
var (
	ErrFolderName      = errors.New("invalid folder name")
	ErrFolderExists    = errors.New("folder already exists")
	ErrFolderProtected = errors.New("special folder cannot be changed")
)

// maxFolderName — предел длины одного сегмента (в символах), как у Roundcube.
const maxFolderName = 128

// CheckFolderName проверяет имя одного уровня: без разделителя иерархии,
// управляющих символов, %, * и без пробелов по краям.
func CheckFolderName(name, delim string) error {
	if name == "" || strings.TrimSpace(name) != name || len([]rune(name)) > maxFolderName {
		return ErrFolderName
	}
	if delim != "" && strings.Contains(name, delim) {
		return ErrFolderName
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f || r == '%' || r == '*' {
			return ErrFolderName
		}
	}
	if strings.EqualFold(name, "INBOX") {
		return ErrFolderName
	}
	return nil
}

func findFolder(folders []Folder, name string) *Folder {
	for i := range folders {
		if folders[i].Name == name {
			return &folders[i]
		}
	}
	return nil
}

// delimiter — разделитель иерархии сервера (по INBOX или первой папке).
func delimiter(folders []Folder) string {
	for _, f := range folders {
		if f.Delimiter != "" {
			return f.Delimiter
		}
	}
	return ""
}

// children — вложенные папки (все уровни), самые глубокие первыми.
func children(folders []Folder, name, delim string) []Folder {
	if delim == "" {
		return nil
	}
	var out []Folder
	for _, f := range folders {
		if strings.HasPrefix(f.Name, name+delim) {
			out = append(out, f)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Depth > out[j].Depth })
	return out
}

// protected: INBOX и спецпапки (и родители спецпапок) не переименовываются и не удаляются —
// иначе ломаются роли, на которые рассчитывают Roundcube, Sieve и почтовые клиенты.
func protected(folders []Folder, f *Folder) bool {
	if f.Role != "" || strings.EqualFold(f.Name, "INBOX") {
		return true
	}
	for _, c := range children(folders, f.Name, f.Delimiter) {
		if c.Role != "" {
			return true
		}
	}
	return false
}

func joinFolder(parent, name, delim string) string {
	if parent == "" {
		return name
	}
	return parent + delim + name
}

// CreateFolder создаёт папку name внутри parent ("" — верхний уровень) и подписывается на неё.
func CreateFolder(c *imapclient.Client, folders []Folder, parent, name string) (string, error) {
	delim := delimiter(folders)
	if err := CheckFolderName(name, delim); err != nil {
		return "", err
	}
	if parent != "" {
		if delim == "" || findFolder(folders, parent) == nil {
			return "", ErrNotFound
		}
	}
	full := joinFolder(parent, name, delim)
	for _, f := range folders {
		if strings.EqualFold(f.Name, full) {
			return "", ErrFolderExists
		}
	}
	if err := c.Create(full, nil).Wait(); err != nil {
		return "", err
	}
	_ = c.Subscribe(full).Wait()
	return full, nil
}

// RenameFolder переименовывает и/или переносит папку под newParent.
// Подписки вложенных папок переносятся вместе с ней.
func RenameFolder(c *imapclient.Client, folders []Folder, name, newParent, newName string) (string, error) {
	f := findFolder(folders, name)
	if f == nil {
		return "", ErrNotFound
	}
	if protected(folders, f) {
		return "", ErrFolderProtected
	}
	delim := delimiter(folders)
	if err := CheckFolderName(newName, delim); err != nil {
		return "", err
	}
	if newParent != "" {
		if delim == "" || findFolder(folders, newParent) == nil {
			return "", ErrNotFound
		}
		// Нельзя перенести папку в саму себя или в свою подпапку.
		if newParent == name || strings.HasPrefix(newParent, name+delim) {
			return "", ErrFolderName
		}
	}
	full := joinFolder(newParent, newName, delim)
	if full == name {
		return full, nil
	}
	for _, o := range folders {
		if strings.EqualFold(o.Name, full) && o.Name != name {
			return "", ErrFolderExists
		}
	}
	subs := subscribedUnder(folders, name, delim)
	// Выбранная папка мешает RENAME на некоторых серверах.
	unselect(c)
	if err := c.Rename(name, full).Wait(); err != nil {
		return "", err
	}
	for _, old := range subs {
		_ = c.Unsubscribe(old).Wait()
		_ = c.Subscribe(full + strings.TrimPrefix(old, name)).Wait()
	}
	return full, nil
}

func subscribedUnder(folders []Folder, name, delim string) []string {
	var out []string
	for _, f := range folders {
		if f.Subscribed && (f.Name == name || (delim != "" && strings.HasPrefix(f.Name, name+delim))) {
			out = append(out, f.Name)
		}
	}
	return out
}

// DeleteFolder удаляет папку вместе с подпапками и письмами в них.
func DeleteFolder(c *imapclient.Client, folders []Folder, name string) error {
	f := findFolder(folders, name)
	if f == nil {
		return ErrNotFound
	}
	if protected(folders, f) {
		return ErrFolderProtected
	}
	unselect(c)
	for _, sub := range append(children(folders, name, f.Delimiter), *f) {
		_ = c.Unsubscribe(sub.Name).Wait()
		if err := c.Delete(sub.Name).Wait(); err != nil {
			var ie *imap.Error
			// Папка-контейнер (\Noselect) могла исчезнуть вместе с последней подпапкой.
			if errors.As(err, &ie) && ie.Code == imap.ResponseCodeNonExistent {
				continue
			}
			return err
		}
	}
	return nil
}

// SetSubscribed подписывает на папку или отписывает от неё.
func SetSubscribed(c *imapclient.Client, folders []Folder, name string, on bool) error {
	f := findFolder(folders, name)
	if f == nil {
		return ErrNotFound
	}
	if !on && (f.Role != "") {
		return ErrFolderProtected // спецпапки всегда видны
	}
	if on {
		return c.Subscribe(name).Wait()
	}
	return c.Unsubscribe(name).Wait()
}

// EmptyFolder удаляет навсегда все письма папки.
func EmptyFolder(c *imapclient.Client, name string) error {
	data, err := c.Select(name, nil).Wait()
	if err != nil {
		return err
	}
	if data.NumMessages == 0 {
		return nil
	}
	all := imap.SeqSet{}
	all.AddRange(1, 0) // 1:*
	if err := c.Store(all, &imap.StoreFlags{Op: imap.StoreFlagsAdd, Silent: true, Flags: []imap.Flag{imap.FlagDeleted}}, nil).Close(); err != nil {
		return err
	}
	return c.Expunge().Close()
}

// MarkAllRead ставит \Seen всем непрочитанным письмам папки.
func MarkAllRead(c *imapclient.Client, name string) error {
	if _, err := c.Select(name, nil).Wait(); err != nil {
		return err
	}
	res, err := c.UIDSearch(&imap.SearchCriteria{NotFlag: []imap.Flag{imap.FlagSeen}}, nil).Wait()
	if err != nil {
		return err
	}
	uids := res.AllUIDs()
	if len(uids) == 0 {
		return nil
	}
	var set imap.UIDSet
	set.AddNum(uids...)
	return c.Store(set, &imap.StoreFlags{Op: imap.StoreFlagsAdd, Silent: true, Flags: []imap.Flag{imap.FlagSeen}}, nil).Close()
}

// unselect закрывает выбранную папку: RENAME и DELETE выбранной папки
// некоторые серверы не выполняют.
func unselect(c *imapclient.Client) {
	if c.Mailbox() != nil && (c.Caps().Has(imap.CapUnselect) || c.Caps().Has(imap.CapIMAP4rev2)) {
		_ = c.Unselect().Wait()
	}
}

// ErrNoArchive — у ящика нет папки с ролью «Архив».
var ErrNoArchive = errors.New("archive folder not found")

func asIMAPError(err error, target **imap.Error) bool { return errors.As(err, target) }
