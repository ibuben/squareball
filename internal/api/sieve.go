package api

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"

	"github.com/ibuben/squareball/ext"
	"github.com/ibuben/squareball/internal/session"
	"github.com/ibuben/squareball/internal/sieve"
)

// Скрипт по умолчанию, если активного нет. Если активный скрипт уже есть
// (например, "roundcube"), правим именно его — так Roundcube и этот клиент
// видят одни и те же правила.
const defaultScriptName = "webmail"

func scriptTag(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:8])
}

func (s *Server) sieveCreds(sess *session.Session) (sieve.Creds, bool) {
	if sess.AuthMode == ext.AuthImpersonation {
		if !contains(s.cfg.IMAP.Impersonation.ApplyTo, "sieve") {
			return sieve.Creds{}, false
		}
		return sieve.Creds{Username: sess.Mailbox, MasterUser: s.cfg.IMAP.Impersonation.MasterUser, MasterSecret: s.masterPw}, true
	}
	pw, _ := sess.Password(s.sessions)
	return sieve.Creds{Username: sess.Mailbox, Password: pw}, true
}

// withSieve открывает соединение ManageSieve на время одного запроса.
func (s *Server) withSieve(w http.ResponseWriter, r *http.Request, fn func(c *sieve.Client) error) bool {
	if !s.cfg.Sieve.Enabled {
		writeErr(w, http.StatusNotFound, "sieve.disabled", "")
		return false
	}
	cr, ok := s.sieveCreds(sessionFrom(r.Context()))
	if !ok {
		writeErr(w, http.StatusForbidden, "sieve.not_allowed", "")
		return false
	}
	c, err := sieve.Dial(&s.cfg.Sieve.Endpoint, cr)
	if err == nil {
		defer c.Close()
		err = fn(c)
	}
	if err == nil {
		return true
	}
	var se *sieve.ServerError
	switch {
	case errors.Is(err, sieve.ErrAuth):
		writeErr(w, http.StatusUnauthorized, "auth.expired", "")
	case errors.As(err, &se):
		writeErr(w, http.StatusBadRequest, "sieve.rejected", se.Message)
	case errors.Is(err, errSieveConflict):
		writeErr(w, http.StatusConflict, "sieve.conflict", "")
	default:
		s.log.Error("managesieve", "err", err)
		writeErr(w, http.StatusBadGateway, "sieve.unavailable", "")
	}
	return false
}

var errSieveConflict = errors.New("script changed since it was loaded")

// activeScript возвращает имя и текст активного скрипта ("" — если его нет).
func activeScript(c *sieve.Client) (name, script string, err error) {
	list, err := c.ListScripts()
	if err != nil {
		return "", "", err
	}
	for _, sc := range list {
		if sc.Active {
			script, err = c.GetScript(sc.Name)
			return sc.Name, script, err
		}
	}
	return defaultScriptName, "", nil
}

type sieveState struct {
	Mode       string          `json:"mode"` // rules | raw
	ScriptName string          `json:"script_name"`
	Tag        string          `json:"tag"` // версия скрипта для защиты от перезаписи
	Ruleset    *sieve.Ruleset  `json:"ruleset,omitempty"`
	Raw        string          `json:"raw"`
	Reason     string          `json:"reason,omitempty"` // почему форма недоступна
	Extensions map[string]bool `json:"extensions"`
}

func (s *Server) handleGetSieve(w http.ResponseWriter, r *http.Request) {
	var st sieveState
	if s.withSieve(w, r, func(c *sieve.Client) error {
		name, script, err := activeScript(c)
		if err != nil {
			return err
		}
		st = sieveState{Mode: "rules", ScriptName: name, Tag: scriptTag(script), Raw: script, Extensions: c.Extensions()}
		rs, err := sieve.FromScript(script)
		if err != nil {
			st.Mode, st.Reason = "raw", err.Error()
		} else {
			st.Ruleset = rs
		}
		return nil
	}) {
		writeJSON(w, http.StatusOK, st)
	}
}

func (s *Server) handlePutSieve(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Mode    string         `json:"mode"`
		Tag     string         `json:"tag"`
		Ruleset *sieve.Ruleset `json:"ruleset"`
		Raw     string         `json:"raw"`
	}
	if err := readJSON(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid", "")
		return
	}
	var script string
	switch {
	case in.Mode == "rules" && in.Ruleset != nil:
		if err := in.Ruleset.Validate(); err != nil {
			writeErr(w, http.StatusBadRequest, "sieve.invalid", err.Error())
			return
		}
		script = in.Ruleset.Script()
	case in.Mode == "raw":
		if len(in.Raw) > 256<<10 {
			writeErr(w, http.StatusBadRequest, "sieve.too_large", "")
			return
		}
		script = in.Raw
	default:
		writeErr(w, http.StatusBadRequest, "invalid", "")
		return
	}
	var out sieveState
	if s.withSieve(w, r, func(c *sieve.Client) error {
		name, current, err := activeScript(c)
		if err != nil {
			return err
		}
		if in.Tag != scriptTag(current) {
			return errSieveConflict
		}
		if err := c.CheckScript(script); err != nil {
			return err
		}
		if err := c.PutScript(name, script); err != nil {
			return err
		}
		if err := c.SetActive(name); err != nil {
			return err
		}
		out = sieveState{Mode: in.Mode, ScriptName: name, Tag: scriptTag(script), Raw: script, Ruleset: in.Ruleset, Extensions: c.Extensions()}
		return nil
	}) {
		s.audit.Info("sieve updated", "mailbox", sessionFrom(r.Context()).Mailbox, "script", out.ScriptName, "mode", in.Mode)
		writeJSON(w, http.StatusOK, out)
	}
}
