// Package store — локальные данные пользователя: отправители (идентичности)
// и подписи; позже — контакты и настройки. Почта здесь не хранится.
//
// Драйвер — SQLite на чистом Go (modernc.org/sqlite): бинарь остаётся
// статическим и не требует CGO.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

type Store struct{ db *sql.DB }

// Path выбирает файл БД: явный DSN из файла, иначе каталог состояния systemd
// ($STATE_DIRECTORY), иначе текущий каталог.
func Path(dsnFile string) (string, error) {
	if dsnFile != "" {
		b, err := os.ReadFile(dsnFile)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(b)), nil
	}
	dir := os.Getenv("STATE_DIRECTORY")
	if dir == "" {
		dir = "."
	}
	if i := strings.IndexByte(dir, ':'); i >= 0 { // systemd может передать несколько каталогов
		dir = dir[:i]
	}
	return filepath.Join(dir, "webmail.db"), nil
}

func Open(path string) (*Store, error) {
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // SQLite: один писатель; WAL даёт параллельное чтение внутри соединения
	s := &Store{db: db}
	if err := s.migrate(context.Background()); err != nil {
		db.Close()
		return nil, fmt.Errorf("store %s: %w", path, err)
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

// Миграции только добавляются в конец; применённые не меняются.
var migrations = []string{
	`CREATE TABLE identities (
		id         INTEGER PRIMARY KEY,
		mailbox    TEXT NOT NULL,
		name       TEXT NOT NULL DEFAULT '',
		email      TEXT NOT NULL,
		reply_to   TEXT NOT NULL DEFAULT '',
		bcc        TEXT NOT NULL DEFAULT '',
		signature  TEXT NOT NULL DEFAULT '',
		is_default INTEGER NOT NULL DEFAULT 0,
		created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
	);
	CREATE INDEX identities_mailbox ON identities(mailbox);`,
}

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL)`); err != nil {
		return err
	}
	var v int
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_version`).Scan(&v); err != nil {
		return err
	}
	for i := v; i < len(migrations); i++ {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, migrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_version(version) VALUES (?)`, i+1); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// --- отправители --------------------------------------------------------------

type Identity struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Email     string `json:"email"`
	ReplyTo   string `json:"reply_to"`
	Bcc       string `json:"bcc"`
	Signature string `json:"signature"` // HTML, уже санитизированный
	IsDefault bool   `json:"is_default"`
}

var (
	ErrNotFound  = errors.New("not found")
	ErrLastOne   = errors.New("cannot delete the only identity")
	errNoMailbox = errors.New("mailbox is required")
)

const identityCols = `id, name, email, reply_to, bcc, signature, is_default`

func scanIdentity(sc interface{ Scan(...any) error }) (Identity, error) {
	var i Identity
	var def int
	err := sc.Scan(&i.ID, &i.Name, &i.Email, &i.ReplyTo, &i.Bcc, &i.Signature, &def)
	i.IsDefault = def == 1
	return i, err
}

// Identities возвращает отправителей ящика; при первом обращении создаёт
// отправителя по умолчанию с адресом самого ящика.
func (s *Store) Identities(ctx context.Context, mailbox string) ([]Identity, error) {
	if mailbox == "" {
		return nil, errNoMailbox
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+identityCols+` FROM identities WHERE mailbox = ? ORDER BY is_default DESC, id`, mailbox)
	if err != nil {
		return nil, err
	}
	var out []Identity
	for rows.Next() {
		i, err := scanIdentity(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, i)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		res, err := s.db.ExecContext(ctx, `INSERT INTO identities(mailbox, email, is_default) VALUES (?, ?, 1)`, mailbox, mailbox)
		if err != nil {
			return nil, err
		}
		id, _ := res.LastInsertId()
		out = []Identity{{ID: id, Email: mailbox, IsDefault: true}}
	}
	return out, nil
}

func (s *Store) Identity(ctx context.Context, mailbox string, id int64) (Identity, error) {
	i, err := scanIdentity(s.db.QueryRowContext(ctx, `SELECT `+identityCols+` FROM identities WHERE mailbox = ? AND id = ?`, mailbox, id))
	if errors.Is(err, sql.ErrNoRows) {
		return i, ErrNotFound
	}
	return i, err
}

// SaveIdentity создаёт (ID == 0) или обновляет отправителя. Если он отмечен
// по умолчанию, флаг снимается с остальных.
func (s *Store) SaveIdentity(ctx context.Context, mailbox string, i Identity) (Identity, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return i, err
	}
	defer tx.Rollback()
	if i.IsDefault {
		if _, err := tx.ExecContext(ctx, `UPDATE identities SET is_default = 0 WHERE mailbox = ?`, mailbox); err != nil {
			return i, err
		}
	}
	def := 0
	if i.IsDefault {
		def = 1
	}
	if i.ID == 0 {
		res, err := tx.ExecContext(ctx, `INSERT INTO identities(mailbox, name, email, reply_to, bcc, signature, is_default) VALUES (?,?,?,?,?,?,?)`,
			mailbox, i.Name, i.Email, i.ReplyTo, i.Bcc, i.Signature, def)
		if err != nil {
			return i, err
		}
		i.ID, _ = res.LastInsertId()
	} else {
		res, err := tx.ExecContext(ctx, `UPDATE identities SET name=?, email=?, reply_to=?, bcc=?, signature=?, is_default=? WHERE mailbox = ? AND id = ?`,
			i.Name, i.Email, i.ReplyTo, i.Bcc, i.Signature, def, mailbox, i.ID)
		if err != nil {
			return i, err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return i, ErrNotFound
		}
	}
	if err := ensureDefault(ctx, tx, mailbox); err != nil {
		return i, err
	}
	return i, tx.Commit()
}

func (s *Store) DeleteIdentity(ctx context.Context, mailbox string, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM identities WHERE mailbox = ?`, mailbox).Scan(&n); err != nil {
		return err
	}
	if n <= 1 {
		return ErrLastOne
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM identities WHERE mailbox = ? AND id = ?`, mailbox, id)
	if err != nil {
		return err
	}
	if k, _ := res.RowsAffected(); k == 0 {
		return ErrNotFound
	}
	if err := ensureDefault(ctx, tx, mailbox); err != nil {
		return err
	}
	return tx.Commit()
}

// ensureDefault следит, чтобы ровно один отправитель был по умолчанию.
func ensureDefault(ctx context.Context, tx *sql.Tx, mailbox string) error {
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM identities WHERE mailbox = ? AND is_default = 1`, mailbox).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		_, err := tx.ExecContext(ctx, `UPDATE identities SET is_default = 1 WHERE id = (SELECT MIN(id) FROM identities WHERE mailbox = ?)`, mailbox)
		return err
	}
	return nil
}

// DeleteMailbox удаляет все данные ящика (вызывается из админ-API/адаптера).
func (s *Store) DeleteMailbox(ctx context.Context, mailbox string) error {
	for _, q := range []string{
		`DELETE FROM identities WHERE mailbox = ?`,
		`DELETE FROM contacts WHERE mailbox = ?`, // group_members — каскадом
		`DELETE FROM contact_groups WHERE mailbox = ?`,
		`DELETE FROM collected WHERE mailbox = ?`,
		`DELETE FROM prefs WHERE mailbox = ?`,
		`DELETE FROM templates WHERE mailbox = ?`,
		`DELETE FROM spell_words WHERE mailbox = ?`,
	} {
		if _, err := s.db.ExecContext(ctx, q, mailbox); err != nil {
			return err
		}
	}
	return nil
}
