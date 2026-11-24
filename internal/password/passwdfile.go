package password

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// PasswdFile меняет хеш в файле Dovecot passwd-file (user:{SCHEME}hash:...).
// Это помощник для драйвера exec: `webmail passwd-file -file PATH`.
//
// Новый хеш — {BLF-CRYPT} (bcrypt). Остальные поля строки сохраняются.
// Файл заменяется атомарно (временный файл + rename) под flock, права и
// группа сохраняются. Каталогу файла нужна запись для пользователя webmail.
type PasswdFile struct {
	Path          string
	VerifyCurrent bool // сверить текущий пароль с хешем в файле (только BLF-CRYPT)
	Cost          int
}

// Exit-коды совпадают с протоколом драйвера exec.
type exitError struct {
	code int
	msg  string
}

func (e *exitError) Error() string { return e.msg }

// ExitCode возвращает код выхода для ошибки PasswdFile.Run.
func ExitCode(err error) int {
	var e *exitError
	if errors.As(err, &e) {
		return e.code
	}
	return 1
}

// Run читает запрос из r и меняет пароль.
func (p *PasswdFile) Run(r io.Reader) error {
	var req Request
	dec := json.NewDecoder(io.LimitReader(r, 64<<10))
	if err := dec.Decode(&req); err != nil {
		return fmt.Errorf("bad request: %w", err)
	}
	user := strings.ToLower(strings.TrimSpace(req.User))
	if user == "" || strings.ContainsAny(user, ":\r\n\x00") {
		return errors.New("bad user")
	}
	if req.Password == "" || strings.ContainsAny(req.Password, "\r\n\x00") {
		return &exitError{ExitWeak, "bad new password"}
	}
	// bcrypt учитывает только первые 72 байта: длиннее — отказ, а не тихое обрезание.
	if len(req.Password) > 72 {
		return &exitError{ExitWeak, "password longer than 72 bytes is not supported by BLF-CRYPT"}
	}
	cost := p.Cost
	if cost == 0 {
		cost = 10
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), cost)
	if err != nil {
		return err
	}

	lock, err := os.OpenFile(p.Path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}

	data, err := os.ReadFile(p.Path)
	if err != nil {
		return err
	}
	st, err := os.Stat(p.Path)
	if err != nil {
		return err
	}
	var out bytes.Buffer
	found := false
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		name, rest, ok := strings.Cut(line, ":")
		if ok && !found && strings.ToLower(name) == user && !strings.HasPrefix(line, "#") {
			found = true
			old, tail, hasTail := strings.Cut(rest, ":")
			if p.VerifyCurrent {
				if err := verify(old, req.Current); err != nil {
					return err
				}
			}
			line = name + ":{BLF-CRYPT}" + string(hash)
			if hasTail {
				line += ":" + tail
			}
		}
		out.WriteString(line)
		out.WriteByte('\n')
	}
	if err := sc.Err(); err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("user %s not found in %s", user, p.Path)
	}
	return replaceFile(p.Path, out.Bytes(), st)
}

func verify(stored, current string) error {
	h, ok := strings.CutPrefix(stored, "{BLF-CRYPT}")
	if !ok {
		return fmt.Errorf("cannot verify scheme of stored hash; use verify_current only with BLF-CRYPT")
	}
	if bcrypt.CompareHashAndPassword([]byte(h), []byte(current)) != nil {
		return &exitError{ExitWrongCurrent, "current password mismatch"}
	}
	return nil
}

func replaceFile(path string, data []byte, st os.FileInfo) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(st.Mode().Perm()); err != nil {
		tmp.Close()
		return err
	}
	if sys, ok := st.Sys().(*syscall.Stat_t); ok {
		// Группа нужна Dovecot для чтения; владельца сменить может только root —
		// ошибку игнорируем, если группа и так совпала (setgid-каталог).
		if err := tmp.Chown(-1, int(sys.Gid)); err != nil {
			if fi, e := tmp.Stat(); e != nil || fi.Sys().(*syscall.Stat_t).Gid != sys.Gid {
				tmp.Close()
				return fmt.Errorf("keep group of %s: %w", path, err)
			}
		}
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// Dovecot перечитывает passwd-file, только если изменились mtime (в секундах)
	// или размер. Хеши bcrypt одной длины, поэтому две смены в одну секунду
	// оставили бы в памяти Dovecot старый пароль. Новая версия всегда получает
	// mtime хотя бы на секунду позже прежнего.
	mt := time.Now()
	if old := st.ModTime().Truncate(time.Second); !mt.Truncate(time.Second).After(old) {
		mt = old.Add(time.Second)
	}
	if err := os.Chtimes(tmp.Name(), mt, mt); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	// Dovecot проверяет файл не чаще раза в секунду: если в эту секунду уже был
	// вход (ядро только что проверило текущий пароль по IMAP), до её конца
	// действует старый пароль. Возвращаемся, когда началась следующая секунда
	// и наступило mtime файла.
	until := time.Now().Truncate(time.Second).Add(time.Second)
	if m := mt.Truncate(time.Second); m.After(until) {
		until = m
	}
	time.Sleep(time.Until(until.Add(10 * time.Millisecond)))
	return nil
}
