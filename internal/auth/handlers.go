package auth

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/slime4ik/backmess/internal/config"
)

const (
	stateCookie = "bm_oauth_state"
	portCookie  = "bm_oauth_port"
)

// Handlers — вход через Discord OAuth2 (scope identify) и гостевой вход по нику.
type Handlers struct {
	cfg      *config.Config
	sessions *Sessions
	http     *http.Client
}

func NewHandlers(cfg *config.Config, s *Sessions) *Handlers {
	return &Handlers{cfg: cfg, sessions: s, http: &http.Client{Timeout: 10 * time.Second}}
}

func (h *Handlers) redirectURI() string {
	// Путь /oauth/callback — он уже зарегистрирован в Discord Developer Portal.
	return h.cfg.BaseURL + "/oauth/callback"
}

// GET /auth/discord — редирект на страницу авторизации Discord.
func (h *Handlers) DiscordLogin(w http.ResponseWriter, r *http.Request) {
	if !h.cfg.DiscordEnabled() {
		http.Error(w, "discord oauth is not configured", http.StatusNotImplemented)
		return
	}
	state := RandID(24)
	http.SetCookie(w, &http.Cookie{
		Name: stateCookie, Value: state, Path: "/", MaxAge: 600,
		HttpOnly: true, Secure: h.cfg.Secure(), SameSite: http.SameSiteLaxMode,
	})
	// десктоп-приложение передаёт ?port=N — после логина вернём ему сессию
	// редиректом на его локальный слушатель http://127.0.0.1:N/callback
	if p, err := strconv.Atoi(r.URL.Query().Get("port")); err == nil && p >= 1024 && p <= 65535 {
		http.SetCookie(w, &http.Cookie{
			Name: portCookie, Value: strconv.Itoa(p), Path: "/", MaxAge: 600,
			HttpOnly: true, Secure: h.cfg.Secure(), SameSite: http.SameSiteLaxMode,
		})
	} else {
		http.SetCookie(w, &http.Cookie{Name: portCookie, Value: "", Path: "/", MaxAge: -1})
	}
	q := url.Values{
		"client_id":     {h.cfg.DiscordClientID},
		"response_type": {"code"},
		"redirect_uri":  {h.redirectURI()},
		"scope":         {"identify"},
		"state":         {state},
	}
	http.Redirect(w, r, "https://discord.com/oauth2/authorize?"+q.Encode(), http.StatusFound)
}

type discordUser struct {
	ID         string `json:"id"`
	Username   string `json:"username"`
	GlobalName string `json:"global_name"`
	Avatar     string `json:"avatar"`
}

// GET /oauth/callback — обмен code на токен, запрос профиля, выдача сессии.
func (h *Handlers) DiscordCallback(w http.ResponseWriter, r *http.Request) {
	if !h.cfg.DiscordEnabled() {
		http.Error(w, "discord oauth is not configured", http.StatusNotImplemented)
		return
	}
	st, err := r.Cookie(stateCookie)
	if err != nil || st.Value == "" || r.URL.Query().Get("state") != st.Value {
		http.Error(w, "bad oauth state", http.StatusBadRequest)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: stateCookie, Value: "", Path: "/", MaxAge: -1})
	code := r.URL.Query().Get("code")
	if code == "" {
		http.Redirect(w, r, "/", http.StatusFound) // юзер нажал «отмена»
		return
	}

	form := url.Values{
		"client_id":     {h.cfg.DiscordClientID},
		"client_secret": {h.cfg.DiscordClientSecret},
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {h.redirectURI()},
	}
	resp, err := h.http.PostForm("https://discord.com/api/oauth2/token", form)
	if err != nil {
		http.Error(w, "discord token exchange failed", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != 200 || json.Unmarshal(body, &tok) != nil || tok.AccessToken == "" {
		http.Error(w, "discord token exchange failed: "+string(body), http.StatusBadGateway)
		return
	}

	req, _ := http.NewRequest("GET", "https://discord.com/api/users/@me", nil)
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	uresp, err := h.http.Do(req)
	if err != nil {
		http.Error(w, "discord profile request failed", http.StatusBadGateway)
		return
	}
	defer uresp.Body.Close()
	var du discordUser
	if err := json.NewDecoder(io.LimitReader(uresp.Body, 1<<20)).Decode(&du); err != nil || du.ID == "" {
		http.Error(w, "discord profile request failed", http.StatusBadGateway)
		return
	}

	name := du.GlobalName
	if name == "" {
		name = du.Username
	}
	u := User{
		ID:     du.ID,
		Name:   name,
		Avatar: discordAvatarURL(du),
		Color:  ColorFor(du.ID),
	}
	h.sessions.Issue(w, u)

	// логин из десктоп-приложения: отдаём сессию его локальному слушателю
	if pc, err := r.Cookie(portCookie); err == nil && pc.Value != "" {
		http.SetCookie(w, &http.Cookie{Name: portCookie, Value: "", Path: "/", MaxAge: -1})
		if port, err := strconv.Atoi(pc.Value); err == nil && port >= 1024 && port <= 65535 {
			http.Redirect(w, r, fmt.Sprintf("http://127.0.0.1:%d/callback?session=%s",
				port, url.QueryEscape(h.sessions.Token(u))), http.StatusFound)
			return
		}
	}
	http.Redirect(w, r, "/", http.StatusFound)
}

func discordAvatarURL(du discordUser) string {
	if du.Avatar == "" {
		// дефолтная аватарка Discord
		n := uint64(0)
		if id, err := strconv.ParseUint(du.ID, 10, 64); err == nil {
			n = (id >> 22) % 6
		}
		return fmt.Sprintf("https://cdn.discordapp.com/embed/avatars/%d.png", n)
	}
	ext := "png"
	if strings.HasPrefix(du.Avatar, "a_") {
		ext = "gif"
	}
	return fmt.Sprintf("https://cdn.discordapp.com/avatars/%s/%s.%s?size=128", du.ID, du.Avatar, ext)
}

// POST /auth/guest {"name": "..."} — вход без Discord.
func (h *Handlers) GuestLogin(w http.ResponseWriter, r *http.Request) {
	if !h.cfg.AllowGuests {
		http.Error(w, "guest login is disabled", http.StatusForbidden)
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" || len([]rune(name)) > 32 {
		http.Error(w, "имя: от 1 до 32 символов", http.StatusBadRequest)
		return
	}
	id := "g" + RandID(10)
	u := User{ID: id, Name: name, Color: ColorFor(id), Guest: true}
	h.sessions.Issue(w, u)
	// session — для десктоп-приложения (браузеру хватает cookie)
	writeJSON(w, map[string]any{"user": u, "session": h.sessions.Token(u)})
}

// POST /auth/logout
func (h *Handlers) Logout(w http.ResponseWriter, r *http.Request) {
	h.sessions.Clear(w)
	writeJSON(w, map[string]any{"ok": true})
}

// GET /api/refresh — продлить сессию: свежая cookie + токен для десктопа.
func (h *Handlers) Refresh(w http.ResponseWriter, r *http.Request) {
	u, ok := h.sessions.Get(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	h.sessions.Issue(w, *u)
	writeJSON(w, map[string]any{"user": u, "session": h.sessions.Token(*u)})
}

// GET /api/me — текущий юзер (или null) + доступные способы входа.
func (h *Handlers) Me(w http.ResponseWriter, r *http.Request) {
	var user *User
	if u, ok := h.sessions.Get(r); ok {
		user = u
	}
	writeJSON(w, map[string]any{
		"user":    user,
		"discord": h.cfg.DiscordEnabled(),
		"guests":  h.cfg.AllowGuests,
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
