package api

import (
	"net/http"

	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/ibuben/squareball/internal/mail"
)

func (s *Server) folderRoutes() {
	m := s.mux
	m.HandleFunc("POST /api/v1/folders", s.authed(s.handleCreateFolder))
	m.HandleFunc("POST /api/v1/folders/rename", s.authed(s.handleRenameFolder))
	m.HandleFunc("POST /api/v1/folders/delete", s.authed(s.handleDeleteFolder))
	m.HandleFunc("POST /api/v1/folders/subscribe", s.authed(s.handleSubscribeFolder))
	m.HandleFunc("POST /api/v1/folders/empty", s.authed(s.handleEmptyFolder))
	m.HandleFunc("POST /api/v1/folders/read", s.authed(s.handleReadFolder))
}

type folderReq struct {
	Name       string `json:"name"`
	Parent     string `json:"parent"`
	NewName    string `json:"new_name"`
	Subscribed bool   `json:"subscribed"`
}

func (s *Server) readFolderReq(w http.ResponseWriter, r *http.Request) (*folderReq, bool) {
	var in folderReq
	if err := readJSON(r, &in); err != nil || in.Name == "" {
		writeErr(w, http.StatusBadRequest, "invalid", "")
		return nil, false
	}
	return &in, true
}

// folderOp выполняет операцию над актуальным списком папок и отдаёт его заново.
func (s *Server) folderOp(w http.ResponseWriter, r *http.Request, extra map[string]any,
	fn func(c *imapclient.Client, fs []mail.Folder) error) {
	var out []mail.Folder
	if s.withIMAP(w, r, func(c *imapclient.Client) error {
		fs, err := s.folders(c)
		if err != nil {
			return err
		}
		if err := fn(c, fs); err != nil {
			return err
		}
		out, err = s.folders(c)
		return err
	}) {
		body := map[string]any{"folders": out}
		for k, v := range extra {
			body[k] = v
		}
		writeJSON(w, http.StatusOK, body)
	}
}

func (s *Server) handleCreateFolder(w http.ResponseWriter, r *http.Request) {
	in, ok := s.readFolderReq(w, r)
	if !ok {
		return
	}
	extra := map[string]any{}
	s.folderOp(w, r, extra, func(c *imapclient.Client, fs []mail.Folder) error {
		full, err := mail.CreateFolder(c, fs, in.Parent, in.Name)
		extra["name"] = full
		return err
	})
}

func (s *Server) handleRenameFolder(w http.ResponseWriter, r *http.Request) {
	in, ok := s.readFolderReq(w, r)
	if !ok {
		return
	}
	extra := map[string]any{}
	s.folderOp(w, r, extra, func(c *imapclient.Client, fs []mail.Folder) error {
		full, err := mail.RenameFolder(c, fs, in.Name, in.Parent, in.NewName)
		extra["name"] = full
		if err == nil {
			s.audit.Info("folder renamed", "mailbox", sessionFrom(r.Context()).Mailbox, "from", in.Name, "to", full)
		}
		return err
	})
}

func (s *Server) handleDeleteFolder(w http.ResponseWriter, r *http.Request) {
	in, ok := s.readFolderReq(w, r)
	if !ok {
		return
	}
	s.folderOp(w, r, nil, func(c *imapclient.Client, fs []mail.Folder) error {
		err := mail.DeleteFolder(c, fs, in.Name)
		if err == nil {
			s.audit.Info("folder deleted", "mailbox", sessionFrom(r.Context()).Mailbox, "folder", in.Name)
		}
		return err
	})
}

func (s *Server) handleSubscribeFolder(w http.ResponseWriter, r *http.Request) {
	in, ok := s.readFolderReq(w, r)
	if !ok {
		return
	}
	s.folderOp(w, r, nil, func(c *imapclient.Client, fs []mail.Folder) error {
		return mail.SetSubscribed(c, fs, in.Name, in.Subscribed)
	})
}

// handleEmptyFolder очищает только Корзину и Спам: из прочих папок письма
// удаляются обычным путём, через Корзину.
func (s *Server) handleEmptyFolder(w http.ResponseWriter, r *http.Request) {
	in, ok := s.readFolderReq(w, r)
	if !ok {
		return
	}
	s.folderOp(w, r, nil, func(c *imapclient.Client, fs []mail.Folder) error {
		if in.Name != mail.FolderByRole(fs, mail.RoleTrash) && in.Name != mail.FolderByRole(fs, mail.RoleJunk) {
			return mail.ErrFolderProtected
		}
		err := mail.EmptyFolder(c, in.Name)
		if err == nil {
			s.audit.Info("folder emptied", "mailbox", sessionFrom(r.Context()).Mailbox, "folder", in.Name)
		}
		return err
	})
}

func (s *Server) handleReadFolder(w http.ResponseWriter, r *http.Request) {
	in, ok := s.readFolderReq(w, r)
	if !ok {
		return
	}
	s.folderOp(w, r, nil, func(c *imapclient.Client, _ []mail.Folder) error {
		return mail.MarkAllRead(c, in.Name)
	})
}
