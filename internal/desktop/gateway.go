package desktop

import (
	"encoding/json"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"github.com/slime4ik/backmess/internal/auth"
	"github.com/slime4ik/backmess/internal/hub"
)

// GatewayEvents — всё, что клиент узнаёт о мире, пока приложение открыто.
// Вызывается из горутины читателя: UI обязан оборачивать обновления в fyne.Do.
type GatewayEvents struct {
	OnReady     func(you auth.User, groups []hub.GroupView)
	OnGroup     func(hub.GroupView)
	OnGroupGone func(id string)
	OnPresence  func(userID string, online bool)
	OnVoice     func(chID string, members []hub.VoiceMember)
	OnChat      func(hub.OutMsg)
	OnHistory   func(chID string, msgs []hub.OutMsg)
	OnMsgDelete func(chID string, id int64)
	OnPing      func(ms int)
	OnLink      func(up bool) // связь с сервером есть/нет
}

// Gateway — постоянное соединение с сервером. Само переподключается: если
// пропал вайфай или сервер перезапустили, приложение молча оживает, а не
// висит мёртвым до перезапуска.
type Gateway struct {
	api *API
	ev  GatewayEvents

	mu     sync.Mutex
	ws     *websocket.Conn
	closed atomic.Bool
	pingMS atomic.Int64
}

func NewGateway(api *API, ev GatewayEvents) *Gateway {
	g := &Gateway{api: api, ev: ev}
	go g.run()
	return g
}

func (g *Gateway) PingMS() int { return int(g.pingMS.Load()) }

func (g *Gateway) run() {
	backoff := time.Second
	for !g.closed.Load() {
		if err := g.connect(); err != nil {
			if g.ev.OnLink != nil {
				g.ev.OnLink(false)
			}
			time.Sleep(backoff)
			if backoff < 15*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second
	}
}

// connect держит одно соединение до его смерти; возвращает ошибку, чтобы
// внешний цикл ушёл в паузу и попробовал снова.
func (g *Gateway) connect() error {
	hdr := map[string][]string{"Cookie": {sessionCookie + "=" + g.api.Token}}
	ws, _, err := websocket.DefaultDialer.Dial(g.api.GatewayURL(), hdr)
	if err != nil {
		return err
	}
	g.mu.Lock()
	g.ws = ws
	g.mu.Unlock()
	if g.ev.OnLink != nil {
		g.ev.OnLink(true)
	}

	stop := make(chan struct{})
	go g.pingLoop(stop)
	defer close(stop)
	defer ws.Close()

	ws.SetReadLimit(4 << 20)
	ws.SetReadDeadline(time.Now().Add(90 * time.Second))
	ws.SetPingHandler(func(string) error {
		ws.SetReadDeadline(time.Now().Add(90 * time.Second))
		g.mu.Lock()
		defer g.mu.Unlock()
		return ws.WriteControl(websocket.PongMessage, nil, time.Now().Add(10*time.Second))
	})

	for {
		var raw map[string]json.RawMessage
		if err := ws.ReadJSON(&raw); err != nil {
			if g.ev.OnLink != nil {
				g.ev.OnLink(false)
			}
			return err
		}
		ws.SetReadDeadline(time.Now().Add(90 * time.Second))
		g.dispatch(raw)
	}
}

func (g *Gateway) dispatch(raw map[string]json.RawMessage) {
	var t string
	json.Unmarshal(raw["t"], &t)
	get := func(k string, v any) bool {
		b, ok := raw[k]
		return ok && json.Unmarshal(b, v) == nil
	}

	switch t {
	case "ready":
		var you auth.User
		var groups []hub.GroupView
		get("you", &you)
		get("groups", &groups)
		if g.ev.OnReady != nil {
			g.ev.OnReady(you, groups)
		}
	case "group":
		var gv hub.GroupView
		if get("group", &gv) && g.ev.OnGroup != nil {
			g.ev.OnGroup(gv)
		}
	case "group-gone":
		var id string
		if get("id", &id) && g.ev.OnGroupGone != nil {
			g.ev.OnGroupGone(id)
		}
	case "presence":
		var id string
		var online bool
		get("id", &id)
		get("online", &online)
		if g.ev.OnPresence != nil {
			g.ev.OnPresence(id, online)
		}
	case "voice":
		var ch string
		var ms []hub.VoiceMember
		get("ch", &ch)
		get("members", &ms)
		if g.ev.OnVoice != nil {
			g.ev.OnVoice(ch, ms)
		}
	case "chat":
		var m hub.OutMsg
		if get("msg", &m) && g.ev.OnChat != nil {
			g.ev.OnChat(m)
		}
	case "history":
		var ch string
		var msgs []hub.OutMsg
		get("ch", &ch)
		get("msgs", &msgs)
		if g.ev.OnHistory != nil {
			g.ev.OnHistory(ch, msgs)
		}
	case "msg-deleted":
		var ch string
		var id int64
		get("ch", &ch)
		get("id", &id)
		if g.ev.OnMsgDelete != nil {
			g.ev.OnMsgDelete(ch, id)
		}
	case "pong":
		var ts int64
		if get("ts", &ts) && ts > 0 {
			ms := time.Now().UnixMilli() - ts
			g.pingMS.Store(ms)
			if g.ev.OnPing != nil {
				g.ev.OnPing(int(ms))
			}
		}
	}
}

// pingLoop меряет задержку до сервера на уровне приложения: отправляем свою
// метку времени, сервер возвращает её как есть — разница и есть RTT.
func (g *Gateway) pingLoop(stop <-chan struct{}) {
	send := func() { g.send(map[string]any{"t": "ping", "ts": time.Now().UnixMilli()}) }
	send()
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			send()
		}
	}
}

func (g *Gateway) send(v any) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.ws == nil {
		return
	}
	g.ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
	g.ws.WriteJSON(v)
}

func (g *Gateway) RequestHistory(chID string) {
	g.send(map[string]any{"t": "history", "ch": chID})
}

func (g *Gateway) SendChat(chID, text, img string, replyTo int64) {
	g.send(map[string]any{"t": "chat", "ch": chID, "text": text, "img": img, "replyTo": replyTo})
}

func (g *Gateway) DeleteMsg(chID string, id int64) {
	g.send(map[string]any{"t": "delete", "ch": chID, "id": id})
}

func (g *Gateway) Close() {
	g.closed.Store(true)
	g.mu.Lock()
	if g.ws != nil {
		g.ws.Close()
	}
	g.mu.Unlock()
}
