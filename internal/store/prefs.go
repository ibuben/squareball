package store

import (
	"context"
	"database/sql"
	"errors"
)

// Настройки ящика: ключ → строка. Допустимые ключи и значения проверяет API.

func init() {
	migrations = append(migrations,
		`CREATE TABLE prefs (
			mailbox TEXT NOT NULL,
			key     TEXT NOT NULL,
			value   TEXT NOT NULL,
			PRIMARY KEY (mailbox, key)
		);`,
	)
}

// Prefs возвращает все настройки ящика.
func (s *Store) Prefs(ctx context.Context, mailbox string) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key, value FROM prefs WHERE mailbox = ?`, mailbox)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// Pref возвращает одну настройку или "".
func (s *Store) Pref(ctx context.Context, mailbox, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM prefs WHERE mailbox = ? AND key = ?`, mailbox, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

// SetPrefs сохраняет настройки; пустое значение удаляет ключ (значение по умолчанию).
func (s *Store) SetPrefs(ctx context.Context, mailbox string, kv map[string]string) error {
	if mailbox == "" {
		return errNoMailbox
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for k, v := range kv {
		if v == "" {
			_, err = tx.ExecContext(ctx, `DELETE FROM prefs WHERE mailbox = ? AND key = ?`, mailbox, k)
		} else {
			_, err = tx.ExecContext(ctx, `INSERT INTO prefs(mailbox, key, value) VALUES (?, ?, ?)
				ON CONFLICT(mailbox, key) DO UPDATE SET value = excluded.value`, mailbox, k, v)
		}
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}
