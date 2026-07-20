// mess — лёгкий голосовой чат для своих: группы с каналами, WebRTC (SFU на
// Pion), текстовый чат с картинками, вход через Discord. Один бинарь; всё
// состояние — в JSON-файле рядом, базы данных нет.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/crypto/acme/autocert"

	"github.com/slime4ik/backmess/internal/api"
	"github.com/slime4ik/backmess/internal/auth"
	"github.com/slime4ik/backmess/internal/config"
	"github.com/slime4ik/backmess/internal/hub"
	"github.com/slime4ik/backmess/internal/img"
	"github.com/slime4ik/backmess/internal/store"
	"github.com/slime4ik/backmess/web"
)

func main() {
	cfg := config.Load()

	uploadsDir := filepath.Join(cfg.DataDir, "uploads")
	if err := os.MkdirAll(uploadsDir, 0o755); err != nil {
		log.Fatalf("data dir: %v", err)
	}

	st, err := store.Open(cfg.DataDir)
	if err != nil {
		log.Fatalf("store: %v", err)
	}
	defer st.Close()

	secret, err := auth.LoadOrCreateSecret(cfg.SessionSecret, cfg.DataDir)
	if err != nil {
		log.Fatalf("session secret: %v", err)
	}
	sessions := auth.NewSessions(secret, cfg.Secure())
	authH := auth.NewHandlers(cfg, sessions)

	h, err := hub.New(cfg, st)
	if err != nil {
		log.Fatalf("webrtc: %v (занят порт %d?)", err, cfg.MediaPort)
	}
	gw := hub.NewGateway(st, h)
	apiH := api.New(cfg, st, gw)

	requireUser := func(next func(http.ResponseWriter, *http.Request, auth.User)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			u, ok := sessions.Get(r)
			if !ok {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			next(w, r, *u)
		}
	}

	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.FS(web.Static())))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "ok")
	})

	mux.HandleFunc("GET /auth/discord", authH.DiscordLogin)
	mux.HandleFunc("GET /oauth/callback", authH.DiscordCallback)
	mux.HandleFunc("POST /auth/guest", authH.GuestLogin)
	mux.HandleFunc("POST /auth/logout", authH.Logout)
	mux.HandleFunc("GET /api/me", authH.Me)
	mux.HandleFunc("GET /api/refresh", authH.Refresh)

	apiH.Routes(mux, func(f api.Handler) http.HandlerFunc {
		return requireUser(func(w http.ResponseWriter, r *http.Request, u auth.User) { f(w, r, u) })
	})

	// gateway — постоянное соединение: presence, дерево групп, текстовый чат
	mux.HandleFunc("GET /gw", requireUser(func(w http.ResponseWriter, r *http.Request, u auth.User) {
		gw.HandleWS(w, r, u)
	}))
	// голосовой ws — поднимается только на время звонка
	mux.HandleFunc("GET /ws", requireUser(func(w http.ResponseWriter, r *http.Request, u auth.User) {
		h.HandleVoiceWS(w, r, u)
	}))

	// страница-приглашение: открывается в браузере, объясняет, что делать
	mux.HandleFunc("GET /i/{code}", func(w http.ResponseWriter, r *http.Request) {
		invitePage(w, r.PathValue("code"))
	})

	mux.HandleFunc("POST /api/upload", requireUser(func(w http.ResponseWriter, r *http.Request, _ auth.User) {
		r.Body = http.MaxBytesReader(w, r.Body, cfg.MaxUploadMB<<20)
		file, _, err := r.FormFile("file")
		if err != nil {
			http.Error(w, "нет файла или файл слишком большой", http.StatusBadRequest)
			return
		}
		defer file.Close()
		data, err := io.ReadAll(file)
		if err != nil {
			http.Error(w, "read error", http.StatusBadRequest)
			return
		}
		compressed, ext, err := img.Compress(data)
		if err != nil {
			http.Error(w, "это не картинка (jpeg/png/gif/webp)", http.StatusUnsupportedMediaType)
			return
		}
		sum := sha256.Sum256(compressed)
		name := hex.EncodeToString(sum[:])[:24] + ext
		if err := os.WriteFile(filepath.Join(uploadsDir, name), compressed, 0o644); err != nil {
			http.Error(w, "write error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"url": "/u/" + name})
	}))

	uploadsFS := http.StripPrefix("/u/", http.FileServer(http.Dir(uploadsDir)))
	mux.Handle("GET /u/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		uploadsFS.ServeHTTP(w, r)
	}))

	// состояние на диск при Ctrl+C / docker stop, иначе потеряется последняя пачка
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-stop
		log.Println("выключаюсь, сохраняю состояние…")
		st.Close()
		os.Exit(0)
	}()

	log.Printf("mess: %s | discord oauth: %v | гости: %v | медиа-порт: %d (udp+tcp)",
		cfg.BaseURL, cfg.DiscordEnabled(), cfg.AllowGuests, cfg.MediaPort)
	if cfg.PublicIP == "" && cfg.Domain != "" {
		log.Printf("ВНИМАНИЕ: DOMAIN задан, а PUBLIC_IP нет — WebRTC через интернет не заведётся")
	}

	if cfg.Domain != "" {
		mgr := &autocert.Manager{
			Prompt:     autocert.AcceptTOS,
			HostPolicy: autocert.HostWhitelist(cfg.Domain),
			Cache:      autocert.DirCache(filepath.Join(cfg.DataDir, "autocert")),
		}
		go func() {
			log.Fatal(http.ListenAndServe(":80", mgr.HTTPHandler(nil)))
		}()
		srv := &http.Server{
			Addr:              ":443",
			Handler:           mux,
			TLSConfig:         mgr.TLSConfig(),
			ReadHeaderTimeout: 10 * time.Second,
		}
		fmt.Printf("https://%s (Let's Encrypt)\n", cfg.Domain)
		log.Fatal(srv.ListenAndServeTLS("", ""))
	}

	srv := &http.Server{Addr: cfg.Addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(srv.ListenAndServe())
}

// invitePage — то, что видит человек, открыв ссылку-приглашение в браузере:
// код крупно, чтобы его можно было вставить в приложение.
func invitePage(w http.ResponseWriter, code string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!doctype html><meta charset=utf-8>
<meta name=viewport content="width=device-width,initial-scale=1">
<title>приглашение в mess</title>
<style>
body{background:#0E1116;color:#E9EDF3;font:16px/1.6 -apple-system,Segoe UI,Roboto,sans-serif;
display:flex;min-height:100vh;margin:0;align-items:center;justify-content:center;text-align:center}
.card{max-width:420px;padding:32px}
h1{color:#FFB454;font-size:28px;margin:0 0 8px}
code{display:block;background:#171B22;border:1px solid #262C36;border-radius:10px;
padding:14px;margin:18px 0;font-size:20px;letter-spacing:2px;color:#FFB454;user-select:all}
p{color:#93A0B4;margin:6px 0}
</style>
<div class=card>
<h1>тебя зовут в mess</h1>
<p>открой приложение, нажми «+» → «войти по коду» и вставь:</p>
<code>%s</code>
<p>нет приложения? качай на <a style="color:#FFB454" href="https://github.com/slime4ik/backmess/releases">GitHub Releases</a></p>
</div>`, html.EscapeString(code))
}
