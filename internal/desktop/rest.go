package desktop

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/slime4ik/backmess/internal/auth"
)

const sessionCookie = "bm_session"

// API — HTTP-клиент к серверу backmess; сессия передаётся заголовком Cookie.
type API struct {
	BaseURL string
	Token   string
	hc      *http.Client
}

func NewAPI(baseURL, token string) *API {
	return &API{
		BaseURL: normalizeBaseURL(baseURL),
		Token:   token,
		hc:      &http.Client{Timeout: 15 * time.Second},
	}
}

// normalizeBaseURL: адрес без схемы ломает и запросы, и открытие браузера —
// достраиваем https:// (для localhost — http://).
func normalizeBaseURL(s string) string {
	s = strings.TrimRight(strings.TrimSpace(s), "/")
	if s == "" {
		return s
	}
	if !strings.Contains(s, "://") {
		if strings.HasPrefix(s, "localhost") || strings.HasPrefix(s, "127.") || strings.HasPrefix(s, "192.168.") {
			s = "http://" + s
		} else {
			s = "https://" + s
		}
	}
	return s
}

func (a *API) req(method, path string, body io.Reader, contentType string) (*http.Response, error) {
	r, err := http.NewRequest(method, a.BaseURL+path, body)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}
	if a.Token != "" {
		r.Header.Set("Cookie", sessionCookie+"="+a.Token)
	}
	return a.hc.Do(r)
}

func decodeOrErr[T any](resp *http.Response, err error) (T, error) {
	var zero T
	if err != nil {
		return zero, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return zero, fmt.Errorf("%d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var v T
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return zero, err
	}
	return v, nil
}

type MeResponse struct {
	User    *auth.User `json:"user"`
	Discord bool       `json:"discord"`
	Guests  bool       `json:"guests"`
}

func (a *API) Me() (MeResponse, error) {
	return decodeOrErr[MeResponse](a.req("GET", "/api/me", nil, ""))
}

/* ---------- группы и каналы ---------- */

func (a *API) postJSON(path string, body any) (map[string]json.RawMessage, error) {
	b, _ := json.Marshal(body)
	return decodeOrErr[map[string]json.RawMessage](a.req("POST", path, bytes.NewReader(b), "application/json"))
}

func (a *API) CreateGroup(name string) error {
	_, err := a.postJSON("/api/groups", map[string]string{"name": name})
	return err
}

// JoinGroup принимает и код, и полную ссылку-приглашение.
func (a *API) JoinGroup(code string) error {
	_, err := a.postJSON("/api/groups/join", map[string]string{"code": code})
	return err
}

func (a *API) LeaveGroup(gid string) error {
	_, err := a.postJSON("/api/groups/"+gid+"/leave", nil)
	return err
}

func (a *API) DeleteGroup(gid string) error {
	_, err := decodeOrErr[map[string]json.RawMessage](a.req("DELETE", "/api/groups/"+gid, nil, ""))
	return err
}

func (a *API) AddChannel(gid, name, kind string) error {
	_, err := a.postJSON("/api/groups/"+gid+"/channels", map[string]string{"name": name, "kind": kind})
	return err
}

func (a *API) DeleteChannel(gid, chid string) error {
	_, err := decodeOrErr[map[string]json.RawMessage](a.req("DELETE", "/api/groups/"+gid+"/channels/"+chid, nil, ""))
	return err
}

func (a *API) RenameChannel(gid, chid, name string) error {
	b, _ := json.Marshal(map[string]string{"name": name})
	_, err := decodeOrErr[map[string]json.RawMessage](a.req("PATCH", "/api/groups/"+gid+"/channels/"+chid, bytes.NewReader(b), "application/json"))
	return err
}

// InviteURL — ссылка-приглашение в группу; её кидают друзьям.
func (a *API) InviteURL(invite string) string {
	return a.BaseURL + "/i/" + invite
}

// DiscordLoginFlow: локальный слушатель ждёт, пока сервер после OAuth
// отредиректит сессию на 127.0.0.1:<port>/callback.
// Использование: f := BeginDiscordLogin(...) → открыть f.URL в браузере
// (с UI-потока!) → f.Wait() в горутине.
type DiscordLoginFlow struct {
	URL     string
	baseURL string
	ch      chan string
	srv     *http.Server
}

func BeginDiscordLogin(baseURL string) (*DiscordLoginFlow, error) {
	baseURL = normalizeBaseURL(baseURL)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	f := &DiscordLoginFlow{baseURL: baseURL, ch: make(chan string, 1)}
	f.URL = fmt.Sprintf("%s/auth/discord?port=%d", baseURL, ln.Addr().(*net.TCPAddr).Port)

	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		s := r.URL.Query().Get("session")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if s == "" {
			io.WriteString(w, "<body style='background:#0E1116;color:#F0566A;font-family:monospace;padding:40px'>не получилось, попробуй ещё раз</body>")
			return
		}
		io.WriteString(w, "<body style='background:#0E1116;color:#E9EDF3;font-family:monospace;padding:40px'>готово — возвращайся в mess, вкладку можно закрыть</body>")
		select {
		case f.ch <- s:
		default:
		}
	})
	f.srv = &http.Server{Handler: mux}
	go f.srv.Serve(ln)
	return f, nil
}

func (f *DiscordLoginFlow) Wait(timeout time.Duration) (*API, error) {
	defer f.srv.Close()
	select {
	case token := <-f.ch:
		return NewAPI(f.baseURL, token), nil
	case <-time.After(timeout):
		return nil, errors.New("не дождался входа через Discord")
	}
}

// Refresh продлевает сессию, возвращает свежий токен (и обновляет его в API).
func (a *API) Refresh() (string, error) {
	res, err := decodeOrErr[map[string]json.RawMessage](a.req("GET", "/api/refresh", nil, ""))
	if err != nil {
		return "", err
	}
	var token string
	if err := json.Unmarshal(res["session"], &token); err != nil || token == "" {
		return "", errors.New("пустой токен")
	}
	a.Token = token
	return token, nil
}

// Upload шлёт картинку, возвращает абсолютный URL для чата ("/u/...").
func (a *API) Upload(filename string, data []byte) (string, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", filename)
	if err != nil {
		return "", err
	}
	fw.Write(data)
	mw.Close()
	res, err := decodeOrErr[map[string]string](a.req("POST", "/api/upload", &buf, mw.FormDataContentType()))
	if err != nil {
		return "", err
	}
	return res["url"], nil
}

// AbsURL превращает относительный путь сервера в абсолютный.
func (a *API) AbsURL(p string) string {
	if strings.HasPrefix(p, "http") {
		return p
	}
	return a.BaseURL + p
}

func (a *API) wsBase() string {
	u := strings.Replace(a.BaseURL, "https://", "wss://", 1)
	return strings.Replace(u, "http://", "ws://", 1)
}

// WSURL — голосовой вебсокет (поднимается только на время звонка).
func (a *API) WSURL() string { return a.wsBase() + "/ws" }

// GatewayURL — постоянный вебсокет: presence, дерево групп, текстовый чат.
func (a *API) GatewayURL() string { return a.wsBase() + "/gw" }
