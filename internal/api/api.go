// Package api — REST поверх store: группы, каналы, приглашения.
//
// Изменяющие ручки после успеха дёргают gateway, чтобы все участники группы
// мгновенно увидели новый/удалённый канал — без опроса и без перезахода.
package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/slime4ik/backmess/internal/auth"
	"github.com/slime4ik/backmess/internal/config"
	"github.com/slime4ik/backmess/internal/hub"
	"github.com/slime4ik/backmess/internal/store"
)

type API struct {
	cfg *config.Config
	st  *store.Store
	gw  *hub.Gateway
}

func New(cfg *config.Config, st *store.Store, gw *hub.Gateway) *API {
	return &API{cfg: cfg, st: st, gw: gw}
}

// Handler — user-aware обёртка: все ручки требуют залогиненного юзера.
type Handler func(w http.ResponseWriter, r *http.Request, u auth.User)

func (a *API) Routes(mux *http.ServeMux, requireUser func(Handler) http.HandlerFunc) {
	mux.HandleFunc("GET /api/groups", requireUser(a.listGroups))
	mux.HandleFunc("POST /api/groups", requireUser(a.createGroup))
	mux.HandleFunc("POST /api/groups/join", requireUser(a.joinGroup))
	mux.HandleFunc("DELETE /api/groups/{gid}", requireUser(a.deleteGroup))
	mux.HandleFunc("POST /api/groups/{gid}/leave", requireUser(a.leaveGroup))
	mux.HandleFunc("POST /api/groups/{gid}/invite", requireUser(a.resetInvite))
	mux.HandleFunc("POST /api/groups/{gid}/channels", requireUser(a.addChannel))
	mux.HandleFunc("DELETE /api/groups/{gid}/channels/{chid}", requireUser(a.delChannel))
	mux.HandleFunc("PATCH /api/groups/{gid}/channels/{chid}", requireUser(a.renameChannel))
}

/* ---------- ручки ---------- */

func (a *API) listGroups(w http.ResponseWriter, r *http.Request, u auth.User) {
	writeJSON(w, map[string]any{"groups": a.st.GroupsOf(u.ID)})
}

func (a *API) createGroup(w http.ResponseWriter, r *http.Request, u auth.User) {
	var req struct{ Name string }
	if !decode(w, r, &req) {
		return
	}
	g, err := a.st.CreateGroup(u, req.Name)
	if err != nil {
		fail(w, err)
		return
	}
	a.gw.GroupChanged(g.ID)
	writeJSON(w, map[string]any{"group": g, "invite": a.InviteURL(g.Invite)})
}

// joinGroup принимает и голый код, и полную ссылку-приглашение —
// юзеру не надо думать, что именно копировать.
func (a *API) joinGroup(w http.ResponseWriter, r *http.Request, u auth.User) {
	var req struct{ Code string }
	if !decode(w, r, &req) {
		return
	}
	g, joined, err := a.st.JoinByInvite(InviteCode(req.Code), u)
	if err != nil {
		fail(w, err)
		return
	}
	if joined {
		a.gw.GroupChanged(g.ID)
	} else {
		// уже состоял — просто отдадим ему группу, чтобы клиент её открыл
		a.gw.ToUser(u.ID, map[string]any{"t": "nudge", "group": g.ID})
	}
	writeJSON(w, map[string]any{"group": g, "joined": joined})
}

func (a *API) deleteGroup(w http.ResponseWriter, r *http.Request, u auth.User) {
	gid := r.PathValue("gid")
	g, ok := a.st.Group(gid)
	if !ok {
		fail(w, store.ErrNotFound)
		return
	}
	if err := a.st.DeleteGroup(gid, u.ID); err != nil {
		fail(w, err)
		return
	}
	a.gw.GroupGone(g)
	writeJSON(w, map[string]any{"ok": true})
}

func (a *API) leaveGroup(w http.ResponseWriter, r *http.Request, u auth.User) {
	gid := r.PathValue("gid")
	if err := a.st.LeaveGroup(gid, u.ID); err != nil {
		fail(w, err)
		return
	}
	a.gw.ToUser(u.ID, map[string]any{"t": "group-gone", "id": gid})
	a.gw.GroupChanged(gid)
	writeJSON(w, map[string]any{"ok": true})
}

func (a *API) resetInvite(w http.ResponseWriter, r *http.Request, u auth.User) {
	code, err := a.st.ResetInvite(r.PathValue("gid"), u.ID)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, map[string]any{"invite": code, "url": a.InviteURL(code)})
}

func (a *API) addChannel(w http.ResponseWriter, r *http.Request, u auth.User) {
	var req struct{ Name, Kind string }
	if !decode(w, r, &req) {
		return
	}
	gid := r.PathValue("gid")
	c, err := a.st.AddChannel(gid, u.ID, req.Name, req.Kind)
	if err != nil {
		fail(w, err)
		return
	}
	a.gw.GroupChanged(gid)
	writeJSON(w, map[string]any{"channel": c})
}

func (a *API) delChannel(w http.ResponseWriter, r *http.Request, u auth.User) {
	gid := r.PathValue("gid")
	if err := a.st.DeleteChannel(gid, u.ID, r.PathValue("chid")); err != nil {
		fail(w, err)
		return
	}
	a.gw.GroupChanged(gid)
	writeJSON(w, map[string]any{"ok": true})
}

func (a *API) renameChannel(w http.ResponseWriter, r *http.Request, u auth.User) {
	var req struct{ Name string }
	if !decode(w, r, &req) {
		return
	}
	gid := r.PathValue("gid")
	if err := a.st.RenameChannel(gid, u.ID, r.PathValue("chid"), req.Name); err != nil {
		fail(w, err)
		return
	}
	a.gw.GroupChanged(gid)
	writeJSON(w, map[string]any{"ok": true})
}

/* ---------- приглашения ---------- */

func (a *API) InviteURL(code string) string {
	return a.cfg.BaseURL + "/i/" + code
}

// InviteCode вытаскивает код из чего угодно: и из полной ссылки, и из кода.
func InviteCode(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndex(s, "/"); i >= 0 {
		s = s[i+1:]
	}
	if i := strings.IndexAny(s, "?#"); i >= 0 {
		s = s[:i]
	}
	return s
}

/* ---------- вспомогательное ---------- */

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(io.LimitReader(r.Body, 8192)).Decode(v); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return false
	}
	return true
}

func fail(w http.ResponseWriter, err error) {
	code := http.StatusBadRequest
	switch {
	case errors.Is(err, store.ErrNotFound):
		code = http.StatusNotFound
	case errors.Is(err, store.ErrForbidden):
		code = http.StatusForbidden
	}
	http.Error(w, err.Error(), code)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
