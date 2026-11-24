package store

import (
	"context"
	"errors"
)

// Личный словарь проверки орфографии.
//
// Файл назван так, чтобы его init() шёл после templates.go: миграции
// добавляются в порядке инициализации файлов пакета (по имени файла).

func init() {
	migrations = append(migrations,
		`CREATE TABLE spell_words (
			mailbox TEXT NOT NULL,
			word    TEXT NOT NULL,
			PRIMARY KEY (mailbox, word)
		);`,
	)
}

// MaxSpellWords — слов в личном словаре ящика.
const MaxSpellWords = 5000

var ErrTooManyWords = errors.New("personal dictionary is full")

func (s *Store) SpellWords(ctx context.Context, mailbox string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT word FROM spell_words WHERE mailbox = ? ORDER BY word`, mailbox)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var w string
		if err := rows.Scan(&w); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

func (s *Store) AddSpellWord(ctx context.Context, mailbox, word string) error {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM spell_words WHERE mailbox = ?`, mailbox).Scan(&n); err != nil {
		return err
	}
	if n >= MaxSpellWords {
		return ErrTooManyWords
	}
	_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO spell_words (mailbox, word) VALUES (?, ?)`, mailbox, word)
	return err
}

func (s *Store) DeleteSpellWord(ctx context.Context, mailbox, word string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM spell_words WHERE mailbox = ? AND word = ?`, mailbox, word)
	return err
}
