package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"
)

// Адресная книга: контакты, группы и «собранные адреса» (получатели
// отправленных писем — как в Roundcube), из которых строятся подсказки.

func init() {
	migrations = append(migrations,
		`CREATE TABLE contacts (
			id         INTEGER PRIMARY KEY,
			mailbox    TEXT NOT NULL,
			name       TEXT NOT NULL DEFAULT '',
			first_name TEXT NOT NULL DEFAULT '',
			last_name  TEXT NOT NULL DEFAULT '',
			org        TEXT NOT NULL DEFAULT '',
			title      TEXT NOT NULL DEFAULT '',
			notes      TEXT NOT NULL DEFAULT '',
			emails     TEXT NOT NULL DEFAULT '[]',
			phones     TEXT NOT NULL DEFAULT '[]',
			search     TEXT NOT NULL DEFAULT '',
			updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
		);
		CREATE INDEX contacts_mailbox ON contacts(mailbox);
		CREATE TABLE contact_groups (
			id      INTEGER PRIMARY KEY,
			mailbox TEXT NOT NULL,
			name    TEXT NOT NULL
		);
		CREATE INDEX contact_groups_mailbox ON contact_groups(mailbox);
		CREATE TABLE group_members (
			group_id   INTEGER NOT NULL REFERENCES contact_groups(id) ON DELETE CASCADE,
			contact_id INTEGER NOT NULL REFERENCES contacts(id) ON DELETE CASCADE,
			PRIMARY KEY (group_id, contact_id)
		);
		CREATE TABLE collected (
			mailbox   TEXT NOT NULL,
			email     TEXT NOT NULL,
			name      TEXT NOT NULL DEFAULT '',
			uses      INTEGER NOT NULL DEFAULT 1,
			search    TEXT NOT NULL DEFAULT '', -- lower(email + name) в Go: lower() SQLite не знает кириллицу
			last_used TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (mailbox, email)
		);`,
	)
}

// MaxContacts — защита от раздувания БД одним ящиком.
const MaxContacts = 20000

var ErrTooMany = errors.New("too many contacts")

type Labeled struct {
	Value string `json:"value"`
	Label string `json:"label,omitempty"` // work | home | mobile | other
}

type Contact struct {
	ID      int64     `json:"id"`
	Name    string    `json:"name"` // отображаемое имя
	First   string    `json:"first_name"`
	Last    string    `json:"last_name"`
	Org     string    `json:"org"`
	Title   string    `json:"title"`
	Notes   string    `json:"notes"`
	Emails  []Labeled `json:"emails"`
	Phones  []Labeled `json:"phones"`
	Groups  []int64   `json:"groups"`
	Updated time.Time `json:"updated_at"`
}

// DisplayName — имя для списка: явное, иначе «Имя Фамилия», иначе адрес.
func (c *Contact) DisplayName() string {
	if n := strings.TrimSpace(c.Name); n != "" {
		return n
	}
	if n := strings.TrimSpace(c.First + " " + c.Last); n != "" {
		return n
	}
	if len(c.Emails) > 0 {
		return c.Emails[0].Value
	}
	return ""
}

func (c *Contact) searchText() string {
	parts := []string{c.Name, c.First, c.Last, c.Org}
	for _, e := range c.Emails {
		parts = append(parts, e.Value)
	}
	for _, p := range c.Phones {
		parts = append(parts, p.Value)
	}
	return strings.ToLower(strings.Join(parts, " "))
}

const contactCols = `id, name, first_name, last_name, org, title, notes, emails, phones, updated_at`

func scanContact(sc interface{ Scan(...any) error }) (Contact, error) {
	var c Contact
	var emails, phones, updated string
	if err := sc.Scan(&c.ID, &c.Name, &c.First, &c.Last, &c.Org, &c.Title, &c.Notes, &emails, &phones, &updated); err != nil {
		return c, err
	}
	_ = json.Unmarshal([]byte(emails), &c.Emails)
	_ = json.Unmarshal([]byte(phones), &c.Phones)
	c.Updated, _ = time.Parse("2006-01-02 15:04:05", updated)
	if c.Emails == nil {
		c.Emails = []Labeled{}
	}
	if c.Phones == nil {
		c.Phones = []Labeled{}
	}
	c.Groups = []int64{}
	return c, nil
}

func likePattern(q string) string {
	q = strings.ToLower(strings.TrimSpace(q))
	q = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(q)
	return "%" + q + "%"
}

// Contacts — страница контактов с поиском и фильтром по группе (0 — все).
func (s *Store) Contacts(ctx context.Context, mailbox, q string, group int64, limit, offset int) ([]Contact, int, error) {
	where := `c.mailbox = ?`
	args := []any{mailbox}
	if strings.TrimSpace(q) != "" {
		where += ` AND c.search LIKE ? ESCAPE '\'`
		args = append(args, likePattern(q))
	}
	if group > 0 {
		where += ` AND c.id IN (SELECT contact_id FROM group_members WHERE group_id = ?)`
		args = append(args, group)
	}
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM contacts c WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+contactCols+` FROM contacts c WHERE `+where+
		` ORDER BY lower(CASE WHEN c.name <> '' THEN c.name ELSE c.first_name || ' ' || c.last_name END), c.id LIMIT ? OFFSET ?`,
		append(args, limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []Contact{}
	for rows.Next() {
		c, err := scanContact(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return out, total, s.fillGroups(ctx, out)
}

func (s *Store) fillGroups(ctx context.Context, list []Contact) error {
	if len(list) == 0 {
		return nil
	}
	idx := map[int64]int{}
	ph := make([]string, len(list))
	args := make([]any, len(list))
	for i, c := range list {
		idx[c.ID] = i
		ph[i], args[i] = "?", c.ID
	}
	rows, err := s.db.QueryContext(ctx, `SELECT contact_id, group_id FROM group_members WHERE contact_id IN (`+strings.Join(ph, ",")+`) ORDER BY group_id`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid, gid int64
		if err := rows.Scan(&cid, &gid); err != nil {
			return err
		}
		list[idx[cid]].Groups = append(list[idx[cid]].Groups, gid)
	}
	return rows.Err()
}

func (s *Store) Contact(ctx context.Context, mailbox string, id int64) (Contact, error) {
	c, err := scanContact(s.db.QueryRowContext(ctx, `SELECT `+contactCols+` FROM contacts WHERE mailbox = ? AND id = ?`, mailbox, id))
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	if err != nil {
		return c, err
	}
	list := []Contact{c}
	err = s.fillGroups(ctx, list)
	return list[0], err
}

func normalize(c *Contact) {
	clean := func(in []Labeled, lower bool) []Labeled {
		out := []Labeled{}
		seen := map[string]bool{}
		for _, l := range in {
			v := strings.TrimSpace(l.Value)
			if lower {
				v = strings.ToLower(v)
			}
			if v == "" || seen[v] {
				continue
			}
			seen[v] = true
			out = append(out, Labeled{Value: v, Label: strings.TrimSpace(l.Label)})
		}
		return out
	}
	c.Emails = clean(c.Emails, true)
	c.Phones = clean(c.Phones, false)
	c.Name, c.First, c.Last = strings.TrimSpace(c.Name), strings.TrimSpace(c.First), strings.TrimSpace(c.Last)
	c.Org, c.Title = strings.TrimSpace(c.Org), strings.TrimSpace(c.Title)
}

// SaveContact создаёт (ID == 0) или обновляет контакт вместе с его группами.
// Группы чужого ящика молча отбрасываются.
func (s *Store) SaveContact(ctx context.Context, mailbox string, c Contact) (Contact, error) {
	normalize(&c)
	emails, _ := json.Marshal(c.Emails)
	phones, _ := json.Marshal(c.Phones)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return c, err
	}
	defer tx.Rollback()
	if c.ID == 0 {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM contacts WHERE mailbox = ?`, mailbox).Scan(&n); err != nil {
			return c, err
		}
		if n >= MaxContacts {
			return c, ErrTooMany
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO contacts(mailbox, name, first_name, last_name, org, title, notes, emails, phones, search)
			VALUES (?,?,?,?,?,?,?,?,?,?)`, mailbox, c.Name, c.First, c.Last, c.Org, c.Title, c.Notes, string(emails), string(phones), c.searchText())
		if err != nil {
			return c, err
		}
		c.ID, _ = res.LastInsertId()
	} else {
		res, err := tx.ExecContext(ctx, `UPDATE contacts SET name=?, first_name=?, last_name=?, org=?, title=?, notes=?, emails=?, phones=?,
			search=?, updated_at=CURRENT_TIMESTAMP WHERE mailbox = ? AND id = ?`,
			c.Name, c.First, c.Last, c.Org, c.Title, c.Notes, string(emails), string(phones), c.searchText(), mailbox, c.ID)
		if err != nil {
			return c, err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return c, ErrNotFound
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM group_members WHERE contact_id = ?`, c.ID); err != nil {
			return c, err
		}
	}
	for _, g := range c.Groups {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO group_members(group_id, contact_id)
			SELECT id, ? FROM contact_groups WHERE id = ? AND mailbox = ?`, c.ID, g, mailbox); err != nil {
			return c, err
		}
	}
	if err := tx.Commit(); err != nil {
		return c, err
	}
	return s.Contact(ctx, mailbox, c.ID)
}

func (s *Store) DeleteContacts(ctx context.Context, mailbox string, ids []int64) (int, error) {
	n := 0
	for _, id := range ids {
		res, err := s.db.ExecContext(ctx, `DELETE FROM contacts WHERE mailbox = ? AND id = ?`, mailbox, id)
		if err != nil {
			return n, err
		}
		k, _ := res.RowsAffected()
		n += int(k)
	}
	return n, nil
}

// ImportContacts добавляет контакты, пропуская те, чей адрес уже есть в книге.
func (s *Store) ImportContacts(ctx context.Context, mailbox string, list []Contact) (added, skipped int, err error) {
	known := map[string]bool{}
	rows, err := s.db.QueryContext(ctx, `SELECT emails FROM contacts WHERE mailbox = ?`, mailbox)
	if err != nil {
		return 0, 0, err
	}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			rows.Close()
			return 0, 0, err
		}
		var es []Labeled
		_ = json.Unmarshal([]byte(raw), &es)
		for _, e := range es {
			known[e.Value] = true
		}
	}
	rows.Close()
	for _, c := range list {
		normalize(&c)
		dup := false
		for _, e := range c.Emails {
			dup = dup || known[e.Value]
		}
		if dup || (c.DisplayName() == "" && len(c.Phones) == 0) {
			skipped++
			continue
		}
		c.ID, c.Groups = 0, nil
		if _, err := s.SaveContact(ctx, mailbox, c); err != nil {
			return added, skipped, err
		}
		for _, e := range c.Emails {
			known[e.Value] = true
		}
		added++
	}
	return added, skipped, nil
}

// --- группы ----------------------------------------------------------------------

type Group struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Count int    `json:"count"`
}

func (s *Store) Groups(ctx context.Context, mailbox string) ([]Group, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT g.id, g.name, COUNT(m.contact_id) FROM contact_groups g
		LEFT JOIN group_members m ON m.group_id = g.id WHERE g.mailbox = ? GROUP BY g.id ORDER BY lower(g.name)`, mailbox)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Group{}
	for rows.Next() {
		var g Group
		if err := rows.Scan(&g.ID, &g.Name, &g.Count); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func (s *Store) SaveGroup(ctx context.Context, mailbox string, g Group) (Group, error) {
	g.Name = strings.TrimSpace(g.Name)
	if g.ID == 0 {
		res, err := s.db.ExecContext(ctx, `INSERT INTO contact_groups(mailbox, name) VALUES (?, ?)`, mailbox, g.Name)
		if err != nil {
			return g, err
		}
		g.ID, _ = res.LastInsertId()
		return g, nil
	}
	res, err := s.db.ExecContext(ctx, `UPDATE contact_groups SET name = ? WHERE mailbox = ? AND id = ?`, g.Name, mailbox, g.ID)
	if err != nil {
		return g, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return g, ErrNotFound
	}
	return g, nil
}

func (s *Store) DeleteGroup(ctx context.Context, mailbox string, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM contact_groups WHERE mailbox = ? AND id = ?`, mailbox, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// --- собранные адреса и подсказки ---------------------------------------------------

type Collected struct {
	Email    string    `json:"email"`
	Name     string    `json:"name"`
	Uses     int       `json:"uses"`
	LastUsed time.Time `json:"last_used"`
}

// Collect запоминает получателей отправленного письма.
func (s *Store) Collect(ctx context.Context, mailbox string, addrs []Collected) error {
	for _, a := range addrs {
		e := strings.ToLower(strings.TrimSpace(a.Email))
		if e == "" || e == strings.ToLower(mailbox) {
			continue
		}
		name := strings.TrimSpace(a.Name)
		if _, err := s.db.ExecContext(ctx, `INSERT INTO collected(mailbox, email, name, search) VALUES (?, ?, ?, ?)
			ON CONFLICT(mailbox, email) DO UPDATE SET uses = uses + 1, last_used = CURRENT_TIMESTAMP,
			name = CASE WHEN excluded.name <> '' THEN excluded.name ELSE collected.name END,
			search = CASE WHEN excluded.name <> '' THEN excluded.search ELSE collected.search END`,
			mailbox, e, name, strings.ToLower(e+" "+name)); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) CollectedList(ctx context.Context, mailbox, q string, limit int) ([]Collected, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT email, name, uses, last_used FROM collected WHERE mailbox = ?
		AND search LIKE ? ESCAPE '\' ORDER BY last_used DESC LIMIT ?`, mailbox, likePattern(q), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Collected{}
	for rows.Next() {
		var c Collected
		var lu string
		if err := rows.Scan(&c.Email, &c.Name, &c.Uses, &lu); err != nil {
			return nil, err
		}
		c.LastUsed, _ = time.Parse("2006-01-02 15:04:05", lu)
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) DeleteCollected(ctx context.Context, mailbox string, emails []string) error {
	for _, e := range emails {
		if _, err := s.db.ExecContext(ctx, `DELETE FROM collected WHERE mailbox = ? AND email = ?`, mailbox, strings.ToLower(e)); err != nil {
			return err
		}
	}
	return nil
}

type Suggestion struct {
	Kind    string      `json:"kind"` // contact | collected | group
	Name    string      `json:"name"`
	Email   string      `json:"email,omitempty"`
	GroupID int64       `json:"group_id,omitempty"`
	Members []Collected `json:"members,omitempty"` // адреса участников группы
	score   int
}

// Suggest — подсказки для полей адреса: группы, контакты, собранные адреса.
// Совпадение с начала слова выше совпадения в середине; дубли адресов убираются.
func (s *Store) Suggest(ctx context.Context, mailbox, q string, limit int) ([]Suggestion, error) {
	q = strings.ToLower(strings.TrimSpace(q))
	if q == "" {
		return []Suggestion{}, nil
	}
	rank := func(fields ...string) int {
		best := 0
		for _, f := range fields {
			f = strings.ToLower(f)
			switch {
			case strings.HasPrefix(f, q):
				best = max(best, 3)
			case strings.Contains(f, " "+q) || strings.Contains(f, "."+q) || strings.Contains(f, "@"+q):
				best = max(best, 2)
			case strings.Contains(f, q):
				best = max(best, 1)
			}
		}
		return best
	}
	var out []Suggestion
	seen := map[string]bool{}

	groups, err := s.Groups(ctx, mailbox)
	if err != nil {
		return nil, err
	}
	for _, g := range groups {
		if r := rank(g.Name); r > 0 && g.Count > 0 {
			members, _, err := s.Contacts(ctx, mailbox, "", g.ID, 500, 0)
			if err != nil {
				return nil, err
			}
			sg := Suggestion{Kind: "group", Name: g.Name, GroupID: g.ID, score: r*10 + 5}
			for _, m := range members {
				if len(m.Emails) > 0 {
					sg.Members = append(sg.Members, Collected{Email: m.Emails[0].Value, Name: m.DisplayName()})
				}
			}
			if len(sg.Members) > 0 {
				out = append(out, sg)
			}
		}
	}
	contacts, _, err := s.Contacts(ctx, mailbox, q, 0, 50, 0)
	if err != nil {
		return nil, err
	}
	for _, c := range contacts {
		for _, e := range c.Emails {
			if seen[e.Value] {
				continue
			}
			seen[e.Value] = true
			out = append(out, Suggestion{Kind: "contact", Name: c.DisplayName(), Email: e.Value,
				score: rank(c.Name, c.First, c.Last, e.Value)*10 + 4})
		}
	}
	collected, err := s.CollectedList(ctx, mailbox, q, 50)
	if err != nil {
		return nil, err
	}
	for _, c := range collected {
		if seen[c.Email] {
			continue
		}
		seen[c.Email] = true
		out = append(out, Suggestion{Kind: "collected", Name: c.Name, Email: c.Email, score: rank(c.Name, c.Email)*10 + min(c.Uses, 3)})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].score > out[j].score })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
