package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const sessionCookie = "bm_session"

// Полгода; клиент при каждом старте дёргает /api/refresh и получает свежий
// токен, так что перелогин нужен только после полугода полного молчания.
const sessionTTL = 180 * 24 * time.Hour

// Sessions — stateless-сессии: подписанный HMAC-SHA256 JSON в cookie, база не нужна.
type Sessions struct {
	secret []byte
	secure bool
}

func NewSessions(secret []byte, secure bool) *Sessions {
	return &Sessions{secret: secret, secure: secure}
}

// LoadOrCreateSecret: SESSION_SECRET из env, иначе генерируется один раз
// и сохраняется в data/session.key — чтобы сессии переживали рестарт.
func LoadOrCreateSecret(envSecret, dataDir string) ([]byte, error) {
	if envSecret != "" {
		return []byte(envSecret), nil
	}
	path := filepath.Join(dataDir, "session.key")
	if b, err := os.ReadFile(path); err == nil && len(b) >= 32 {
		return b, nil
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return nil, err
	}
	return b, nil
}

type sessionPayload struct {
	User User  `json:"u"`
	Exp  int64 `json:"e"`
}

func (s *Sessions) sign(data []byte) string {
	mac := hmac.New(sha256.New, s.secret)
	mac.Write(data)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// Token — подписанное значение сессии; его же кладём в cookie, его же
// отдаём десктоп-приложению (оно шлёт его в заголовке Cookie самостоятельно).
func (s *Sessions) Token(u User) string {
	raw, _ := json.Marshal(sessionPayload{User: u, Exp: time.Now().Add(sessionTTL).Unix()})
	return base64.RawURLEncoding.EncodeToString(raw) + "." + s.sign(raw)
}

func (s *Sessions) Issue(w http.ResponseWriter, u User) {
	val := s.Token(u)
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    val,
		Path:     "/",
		MaxAge:   int(sessionTTL.Seconds()),
		HttpOnly: true,
		Secure:   s.secure,
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *Sessions) Clear(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: s.secure, SameSite: http.SameSiteLaxMode,
	})
}

func (s *Sessions) Get(r *http.Request) (*User, bool) {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return nil, false
	}
	parts := strings.SplitN(c.Value, ".", 2)
	if len(parts) != 2 {
		return nil, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, false
	}
	if !hmac.Equal([]byte(s.sign(raw)), []byte(parts[1])) {
		return nil, false
	}
	var p sessionPayload
	if err := json.Unmarshal(raw, &p); err != nil || time.Now().Unix() > p.Exp {
		return nil, false
	}
	return &p.User, true
}

// RandID — короткий URL-safe id без двоеточий (используется и для connID в hub).
func RandID(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)[:n]
}
