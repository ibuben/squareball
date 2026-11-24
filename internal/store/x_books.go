package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// Внешние адресные книги (CardDAV и т. п.): учётки, книги и кэш карточек.
// Карточки внешних книг лежат в contacts с book_id > 0, href, etag и исходной
// vCard — чтобы при правке не терять поля, которых нет в форме (фото, адреса).
//
// Имя файла начинается с x_, чтобы init() шёл после остальных: миграции
// добавляются в порядке инициализации файлов пакета (по имени файла).

func init() {
	migrations = append(migrations,
		`CREATE TABLE dav_accounts (
			id         INTEGER PRIMARY KEY,
			mailbox    TEXT NOT NULL,
			preset     TEXT NOT NULL DEFAULT '', -- id пресета из конфига; '' — добавлена пользователем
			name       TEXT NOT NULL,
			url        TEXT NOT NULL,
			username   TEXT NOT NULL DEFAULT '',
			secret     BLOB,                     -- пароль, зашифрованный ключом сервера
			error      TEXT NOT NULL DEFAULT '',
			synced_at  TEXT NOT NULL DEFAULT ''
		);
		CREATE INDEX dav_accounts_mailbox ON dav_accounts(mailbox);
		CREATE TABLE address_books (
			id         INTEGER PRIMARY KEY,
			mailbox    TEXT NOT NULL,
			account_id INTEGER NOT NULL REFERENCES dav_accounts(id) ON DELETE CASCADE,
			url        TEXT NOT NULL,
			name       TEXT NOT NULL,
			readonly   INTEGER NOT NULL DEFAULT 0,
			error      TEXT NOT NULL DEFAULT '',
			synced_at  TEXT NOT NULL DEFAULT '',
			UNIQUE (account_id, url)
		);
		ALTER TABLE contacts ADD COLUMN book_id INTEGER NOT NULL DEFAULT 0;
		ALTER TABLE contacts ADD COLUMN href TEXT NOT NULL DEFAULT '';
		ALTER TABLE contacts ADD COLUMN etag TEXT NOT NULL DEFAULT '';
		ALTER TABLE contacts ADD COLUMN vcard TEXT NOT NULL DEFAULT '';
		CREATE INDEX contacts_book ON contacts(book_id, href);`,
	)
}

// MaxDavAccounts — учёток, которые пользователь может добавить сам.
const MaxDavAccounts = 10

type DavAccount struct {
	ID       int64     `json:"id"`
	Preset   string    `json:"preset,omitempty"`
	Name     string    `json:"name"`
	URL      string    `json:"url"`
	Username string    `json:"username"`
	Error    string    `json:"error,omitempty"`
	Synced   time.Time `json:"synced_at"`
	Secret   []byte    `json:"-"`
}

type Book struct {
	ID       int64     `json:"id"`
	Account  int64     `json:"account"`
	URL      string    `json:"-"`
	Name     string    `json:"name"`
	ReadOnly bool      `json:"readonly"`
	Error    string    `json:"error,omitempty"`
	Synced   time.Time `json:"synced_at"`
	Count    int       `json:"count"`
}

const tsLayout = "2006-01-02 15:04:05"

func parseTS(s string) time.Time { t, _ := time.Parse(tsLayout, s); return t }

func (s *Store) DavAccounts(ctx context.Context, mailbox string) ([]DavAccount, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, preset, name, url, username, secret, error, synced_at
		FROM dav_accounts WHERE mailbox = ? ORDER BY preset = '', id`, mailbox)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DavAccount{}
	for rows.Next() {
		var a DavAccount
		var synced string
		if err := rows.Scan(&a.ID, &a.Preset, &a.Name, &a.URL, &a.Username, &a.Secret, &a.Error, &synced); err != nil {
			return nil, err
		}
		a.Synced = parseTS(synced)
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) DavAccount(ctx context.Context, mailbox string, id int64) (DavAccount, error) {
	list, err := s.DavAccounts(ctx, mailbox)
	if err != nil {
		return DavAccount{}, err
	}
	for _, a := range list {
		if a.ID == id {
			return a, nil
		}
	}
	return DavAccount{}, ErrNotFound
}

// AddDavAccount сохраняет учётку. Для пользовательских (Preset == "") действует MaxDavAccounts.
func (s *Store) AddDavAccount(ctx context.Context, mailbox string, a DavAccount) (DavAccount, error) {
	if a.Preset == "" {
		var n int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM dav_accounts WHERE mailbox = ? AND preset = ''`, mailbox).Scan(&n); err != nil {
			return a, err
		}
		if n >= MaxDavAccounts {
			return a, ErrTooMany
		}
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO dav_accounts(mailbox, preset, name, url, username, secret) VALUES (?,?,?,?,?,?)`,
		mailbox, a.Preset, a.Name, a.URL, a.Username, a.Secret)
	if err != nil {
		return a, err
	}
	a.ID, _ = res.LastInsertId()
	return a, nil
}

// UpdateDavPreset обновляет имя и адрес учётки-пресета после правки конфига.
func (s *Store) UpdateDavPreset(ctx context.Context, id int64, name, url, username string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE dav_accounts SET name = ?, url = ?, username = ? WHERE id = ?`, name, url, username, id)
	return err
}

// DeleteDavAccount удаляет учётку с её книгами и карточками.
func (s *Store) DeleteDavAccount(ctx context.Context, mailbox string, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM contacts WHERE mailbox = ? AND book_id IN (SELECT id FROM address_books WHERE account_id = ?)`, mailbox, id); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM dav_accounts WHERE mailbox = ? AND id = ?`, mailbox, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}

func (s *Store) SetDavAccountState(ctx context.Context, id int64, errText string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE dav_accounts SET error = ?, synced_at = ? WHERE id = ?`,
		errText, time.Now().UTC().Format(tsLayout), id)
	return err
}

// Books — внешние книги ящика с числом карточек.
func (s *Store) Books(ctx context.Context, mailbox string) ([]Book, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT b.id, b.account_id, b.url, b.name, b.readonly, b.error, b.synced_at,
			(SELECT COUNT(*) FROM contacts c WHERE c.book_id = b.id)
		FROM address_books b WHERE b.mailbox = ? ORDER BY b.account_id, lower(b.name), b.id`, mailbox)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Book{}
	for rows.Next() {
		var b Book
		var synced string
		if err := rows.Scan(&b.ID, &b.Account, &b.URL, &b.Name, &b.ReadOnly, &b.Error, &synced, &b.Count); err != nil {
			return nil, err
		}
		b.Synced = parseTS(synced)
		out = append(out, b)
	}
	return out, rows.Err()
}

func (s *Store) Book(ctx context.Context, mailbox string, id int64) (Book, error) {
	list, err := s.Books(ctx, mailbox)
	if err != nil {
		return Book{}, err
	}
	for _, b := range list {
		if b.ID == id {
			return b, nil
		}
	}
	return Book{}, ErrNotFound
}

// RemoteBookInfo — книга, найденная на сервере.
type RemoteBookInfo struct {
	URL      string
	Name     string
	ReadOnly bool
}

// SetAccountBooks приводит список книг учётки к найденному на сервере:
// новые добавляет, у старых обновляет имя, пропавшие удаляет вместе с карточками.
func (s *Store) SetAccountBooks(ctx context.Context, mailbox string, account int64, books []RemoteBookInfo) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	keep := map[string]bool{}
	for _, b := range books {
		keep[b.URL] = true
		if _, err := tx.ExecContext(ctx, `INSERT INTO address_books(mailbox, account_id, url, name, readonly) VALUES (?,?,?,?,?)
			ON CONFLICT(account_id, url) DO UPDATE SET name = excluded.name, readonly = excluded.readonly`,
			mailbox, account, b.URL, b.Name, b.ReadOnly); err != nil {
			return err
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, url FROM address_books WHERE account_id = ?`, account)
	if err != nil {
		return err
	}
	var gone []int64
	for rows.Next() {
		var id int64
		var u string
		if err := rows.Scan(&id, &u); err != nil {
			rows.Close()
			return err
		}
		if !keep[u] {
			gone = append(gone, id)
		}
	}
	rows.Close()
	for _, id := range gone {
		if _, err := tx.ExecContext(ctx, `DELETE FROM contacts WHERE book_id = ?`, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM address_books WHERE id = ?`, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) SetBookState(ctx context.Context, id int64, errText string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE address_books SET error = ?, synced_at = ? WHERE id = ?`,
		errText, time.Now().UTC().Format(tsLayout), id)
	return err
}

// CardRef — карточка внешней книги в кэше.
type CardRef struct {
	ID   int64
	ETag string
}

// BookCards — href → карточка для сравнения с сервером.
func (s *Store) BookCards(ctx context.Context, book int64) (map[string]CardRef, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT href, id, etag FROM contacts WHERE book_id = ?`, book)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]CardRef{}
	for rows.Next() {
		var h string
		var r CardRef
		if err := rows.Scan(&h, &r.ID, &r.ETag); err != nil {
			return nil, err
		}
		out[h] = r
	}
	return out, rows.Err()
}

// RemoteMeta — привязка контакта к карточке на сервере.
type RemoteMeta struct {
	Book  int64
	Href  string
	ETag  string
	VCard []byte
}

func (s *Store) ContactRemote(ctx context.Context, mailbox string, id int64) (RemoteMeta, error) {
	var m RemoteMeta
	var vc string
	err := s.db.QueryRowContext(ctx, `SELECT book_id, href, etag, vcard FROM contacts WHERE mailbox = ? AND id = ?`, mailbox, id).
		Scan(&m.Book, &m.Href, &m.ETag, &vc)
	if errors.Is(err, sql.ErrNoRows) {
		return m, ErrNotFound
	}
	m.VCard = []byte(vc)
	return m, err
}

// SetContactRemote запоминает href, ETag и vCard после записи на сервер.
func (s *Store) SetContactRemote(ctx context.Context, id int64, href, etag string, vcard []byte) error {
	_, err := s.db.ExecContext(ctx, `UPDATE contacts SET href = ?, etag = ?, vcard = ? WHERE id = ?`, href, etag, string(vcard), id)
	return err
}

// UpsertCard кладёт карточку с сервера в кэш: по href обновляет (id и группы
// сохраняются), иначе добавляет. Сверх MaxContacts новые карточки пропускаются.
func (s *Store) UpsertCard(ctx context.Context, mailbox string, book int64, href, etag string, vcard []byte, c Contact) error {
	normalize(&c)
	emails, _ := json.Marshal(c.Emails)
	phones, _ := json.Marshal(c.Phones)
	res, err := s.db.ExecContext(ctx, `UPDATE contacts SET name=?, first_name=?, last_name=?, org=?, title=?, notes=?, emails=?, phones=?,
		search=?, etag=?, vcard=?, updated_at=CURRENT_TIMESTAMP WHERE book_id = ? AND href = ?`,
		c.Name, c.First, c.Last, c.Org, c.Title, c.Notes, string(emails), string(phones), c.searchText(), etag, string(vcard), book, href)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return nil
	}
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM contacts WHERE mailbox = ?`, mailbox).Scan(&n); err != nil {
		return err
	}
	if n >= MaxContacts {
		return ErrTooMany
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO contacts(mailbox, book_id, href, etag, vcard, name, first_name, last_name, org, title, notes, emails, phones, search)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, mailbox, book, href, etag, string(vcard),
		c.Name, c.First, c.Last, c.Org, c.Title, c.Notes, string(emails), string(phones), c.searchText())
	return err
}

// DeleteCards убирает из кэша карточки, которых больше нет на сервере.
func (s *Store) DeleteCards(ctx context.Context, book int64, hrefs []string) error {
	for _, h := range hrefs {
		if _, err := s.db.ExecContext(ctx, `DELETE FROM contacts WHERE book_id = ? AND href = ?`, book, h); err != nil {
			return err
		}
	}
	return nil
}

// davAccountsCleanup — для DeleteMailbox.
const davCleanup = `DELETE FROM dav_accounts WHERE mailbox = ?` // книги — каскадом
