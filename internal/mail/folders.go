package mail

import (
	"sort"
	"strings"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// Роли спецпапок.
const (
	RoleInbox   = "inbox"
	RoleDrafts  = "drafts"
	RoleSent    = "sent"
	RoleJunk    = "junk"
	RoleTrash   = "trash"
	RoleArchive = "archive"
)

var roleOrder = map[string]int{RoleInbox: 0, RoleDrafts: 1, RoleSent: 2, RoleArchive: 3, RoleJunk: 4, RoleTrash: 5}

type Folder struct {
	Name       string `json:"name"`      // полное имя на сервере
	Display    string `json:"display"`   // последний сегмент
	Delimiter  string `json:"delimiter"` // разделитель из LIST
	Depth      int    `json:"depth"`
	Role       string `json:"role,omitempty"`
	Selectable bool   `json:"selectable"`
	Total      uint32 `json:"total"`
	Unread     uint32 `json:"unread"`
}

// ListFolders возвращает подписанные и существующие папки с ролями и счётчиками.
// detect = "special_use" — роли по атрибутам SPECIAL-USE, затем по именам из fallback.
func ListFolders(c *imapclient.Client, detect string, fallback map[string]string) ([]Folder, error) {
	opts := &imap.ListOptions{}
	useStatus := c.Caps().Has(imap.CapListStatus) || c.Caps().Has(imap.CapIMAP4rev2)
	if useStatus {
		opts.ReturnStatus = &imap.StatusOptions{NumMessages: true, NumUnseen: true}
	}
	if c.Caps().Has(imap.CapSpecialUse) {
		opts.ReturnSpecialUse = true
	}
	list, err := c.List("", "*", opts).Collect()
	if err != nil {
		return nil, err
	}

	byName := map[string]string{} // lower(name) → role по fallback
	for role, name := range fallback {
		byName[strings.ToLower(name)] = role
	}

	taken := map[string]bool{}
	folders := make([]Folder, 0, len(list))
	for _, l := range list {
		f := Folder{Name: l.Mailbox, Selectable: true}
		if l.Delim != 0 {
			f.Delimiter = string(l.Delim)
			parts := strings.Split(l.Mailbox, f.Delimiter)
			f.Display = parts[len(parts)-1]
			f.Depth = len(parts) - 1
		} else {
			f.Display = l.Mailbox
		}
		for _, a := range l.Attrs {
			switch a {
			case imap.MailboxAttrNoSelect, imap.MailboxAttrNonExistent:
				f.Selectable = false
			}
			if detect == "special_use" && f.Role == "" {
				f.Role = roleFromAttr(a)
			}
		}
		if strings.EqualFold(l.Mailbox, "INBOX") {
			f.Role = RoleInbox
		}
		if l.Status != nil {
			if l.Status.NumMessages != nil {
				f.Total = *l.Status.NumMessages
			}
			if l.Status.NumUnseen != nil {
				f.Unread = *l.Status.NumUnseen
			}
		}
		if f.Role != "" {
			if taken[f.Role] {
				f.Role = "" // вторая папка с той же ролью ("Sent Messages") — обычная
			} else {
				taken[f.Role] = true
			}
		}
		folders = append(folders, f)
	}
	// Роли, не найденные по SPECIAL-USE, — по именам (как в Roundcube).
	for i := range folders {
		if folders[i].Role != "" || folders[i].Depth != 0 {
			continue
		}
		if role, ok := byName[strings.ToLower(folders[i].Name)]; ok && !taken[role] {
			folders[i].Role = role
			taken[role] = true
		}
	}
	if !useStatus {
		for i := range folders {
			if !folders[i].Selectable {
				continue
			}
			st, err := c.Status(folders[i].Name, &imap.StatusOptions{NumMessages: true, NumUnseen: true}).Wait()
			if err != nil {
				continue
			}
			if st.NumMessages != nil {
				folders[i].Total = *st.NumMessages
			}
			if st.NumUnseen != nil {
				folders[i].Unread = *st.NumUnseen
			}
		}
	}
	sort.SliceStable(folders, func(i, j int) bool {
		ri, iok := roleOrder[folders[i].Role]
		rj, jok := roleOrder[folders[j].Role]
		switch {
		case iok && jok:
			return ri < rj
		case iok != jok:
			return iok
		}
		return strings.ToLower(folders[i].Name) < strings.ToLower(folders[j].Name)
	})
	return folders, nil
}

func roleFromAttr(a imap.MailboxAttr) string {
	switch a {
	case imap.MailboxAttrDrafts:
		return RoleDrafts
	case imap.MailboxAttrSent:
		return RoleSent
	case imap.MailboxAttrJunk:
		return RoleJunk
	case imap.MailboxAttrTrash:
		return RoleTrash
	case imap.MailboxAttrArchive:
		return RoleArchive
	}
	return ""
}

// FolderByRole находит имя папки с ролью.
func FolderByRole(folders []Folder, role string) string {
	for _, f := range folders {
		if f.Role == role {
			return f.Name
		}
	}
	return ""
}
