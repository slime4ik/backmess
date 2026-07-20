package config

import (
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

// Config задаётся через переменные окружения (или .env в рабочей директории).
// Всё имеет дефолты для локального запуска: `go run ./cmd/backmess` и порядок.
type Config struct {
	Addr      string // ADDR — адрес HTTP-сервера, дефолт :8080 (игнорируется, если задан DOMAIN)
	Domain    string // DOMAIN — если задан, включается HTTPS c Let's Encrypt на :80/:443
	BaseURL   string // BASE_URL — внешний URL (для OAuth redirect). Выводится из DOMAIN/ADDR, если не задан
	PublicIP  string // PUBLIC_IP — публичный IP сервера, нужен для WebRTC при деплое
	MediaPort int    // MEDIA_PORT — один порт (UDP+TCP) для всего WebRTC-трафика, дефолт 8443
	DataDir   string // DATA_DIR — куда класть загрузки/ключи/сертификаты, дефолт ./data

	DiscordClientID     string // DISCORD_CLIENT_ID
	DiscordClientSecret string // DISCORD_CLIENT_SECRET
	SessionSecret       string // SESSION_SECRET — если пуст, генерируется и сохраняется в DATA_DIR
	AllowGuests         bool   // ALLOW_GUESTS — вход без Discord по нику, дефолт false (клиент — только Discord)
	MaxUploadMB         int64  // MAX_UPLOAD_MB — лимит размера картинки, дефолт 15
}

func Load() *Config {
	_ = godotenv.Load() // .env опционален

	c := &Config{
		Addr:                env("ADDR", ":8080"),
		Domain:              env("DOMAIN", ""),
		BaseURL:             env("BASE_URL", ""),
		PublicIP:            env("PUBLIC_IP", ""),
		MediaPort:           envInt("MEDIA_PORT", 8443),
		DataDir:             env("DATA_DIR", "data"),
		DiscordClientID:     env("DISCORD_CLIENT_ID", ""),
		DiscordClientSecret: env("DISCORD_CLIENT_SECRET", ""),
		SessionSecret:       env("SESSION_SECRET", ""),
		AllowGuests:         envBool("ALLOW_GUESTS", false),
		MaxUploadMB:         int64(envInt("MAX_UPLOAD_MB", 15)),
	}

	if c.BaseURL == "" {
		if c.Domain != "" {
			c.BaseURL = "https://" + c.Domain
		} else {
			port := c.Addr
			if i := strings.LastIndex(port, ":"); i >= 0 {
				port = port[i+1:]
			}
			c.BaseURL = "http://localhost:" + port
		}
	}
	c.BaseURL = strings.TrimRight(c.BaseURL, "/")
	return c
}

func (c *Config) DiscordEnabled() bool {
	return c.DiscordClientID != "" && c.DiscordClientSecret != ""
}

func (c *Config) Secure() bool {
	return strings.HasPrefix(c.BaseURL, "https://")
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envBool(key string, def bool) bool {
	if v := os.Getenv(key); v != "" {
		b, err := strconv.ParseBool(v)
		if err == nil {
			return b
		}
	}
	return def
}
