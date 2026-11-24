package store

import (
	"context"
	"strings"
)

// Шаблоны ответов: именованные куски HTML, которые вставляются в письмо.

func init() {
	migrations = append(migrations,
		`CREATE TABLE templates (
			id         INTEGER PRIMARY KEY,
			mailbox    TEXT NOT NULL,
			name       TEXT NOT NULL,
			html       TEXT NOT NULL DEFAULT '',
			updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
		);
		CREATE INDEX templates_mailbox ON templates(mailbox);`,
	)
}

// MaxTemplates — шаблонов на ящик.
const MaxTemplates = 200

type Template struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	HTML string `json:"html"` // уже санитизированный
}

func (s *Store) Templates(ctx context.Context, mailbox string) ([]Template, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, html FROM templates WHERE mailbox = ? ORDER BY name COLLATE NOCASE, id`, mailbox)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Template{}
	for rows.Next() {
		var t Template
		if err := rows.Scan(&t.ID, &t.Name, &t.HTML); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// SaveTemplate создаёт (ID == 0) или обновляет шаблон ящика.
func (s *Store) SaveTemplate(ctx context.Context, mailbox string, t Template) (Template, error) {
	if mailbox == "" {
		return t, errNoMailbox
	}
	t.Name = strings.TrimSpace(t.Name)
	if t.ID == 0 {
		var n int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM templates WHERE mailbox = ?`, mailbox).Scan(&n); err != nil {
			return t, err
		}
		if n >= MaxTemplates {
			return t, ErrTooMany
		}
		res, err := s.db.ExecContext(ctx, `INSERT INTO templates(mailbox, name, html) VALUES (?, ?, ?)`, mailbox, t.Name, t.HTML)
		if err != nil {
			return t, err
		}
		t.ID, err = res.LastInsertId()
		return t, err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE templates SET name = ?, html = ?, updated_at = CURRENT_TIMESTAMP
		WHERE id = ? AND mailbox = ?`, t.Name, t.HTML, t.ID, mailbox)
	if err != nil {
		return t, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return t, ErrNotFound
	}
	return t, nil
}

func (s *Store) DeleteTemplate(ctx context.Context, mailbox string, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM templates WHERE id = ? AND mailbox = ?`, id, mailbox)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
