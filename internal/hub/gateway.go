package hub

import (
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/slime4ik/backmess/internal/auth"
	"github.com/slime4ik/backmess/internal/store"
)

// Gateway — постоянное соединение клиента, живущее всё время, пока приложение
// открыто (в отличие от голосового ws, который поднимается только на время
// звонка). Через него идут: presence (кто в сети), дерево групп/каналов,
// текстовый чат и состав голосовых каналов.
//
// Именно поэтому список каналов обновляется мгновенно и видно, кто онлайн:
// раньше клиент раз в 5 секунд дёргал REST и всё выглядело мёртвым.
type Gateway struct {
	st  *store.Store
	hub *Hub

	mu    sync.RWMutex
	conns map[string]map[*gwConn]bool // user id -> его соединения (может быть несколько устройств)
}

type gwConn struct {
	gw   *Gateway
	user auth.User
	ws   *websocket.Conn
	mu   sync.Mutex
	dead bool
}

// OutMsg — сообщение чата с уже вложенным автором: клиенту не нужно ходить
// за профилем отдельно, он рисует сразу.
type OutMsg struct {
	ID   int64     `json:"id"`
	Ch   string    `json:"ch"`
	From auth.User `json:"from"`
	Text string    `json:"text,omitempty"`
	Img  string    `json:"img,omitempty"`
	TS   int64     `json:"ts"`
}

// VoiceMember — кто сидит в голосовом канале и в каком состоянии.
type VoiceMember struct {
	User  auth.User   `json:"user"`
	State MemberState `json:"state"`
}

func NewGateway(st *store.Store, h *Hub) *Gateway {
	gw := &Gateway{st: st, hub: h, conns: map[string]map[*gwConn]bool{}}
	h.gw = gw
	return gw
}

/* ---------- соединение ---------- */

func (gw *Gateway) HandleWS(w http.ResponseWriter, r *http.Request, user auth.User) {
	ws, err := gw.hub.upgrader().Upgrade(w, r, nil)
	if err != nil {
		return
	}
	gw.st.SaveUser(user)

	c := &gwConn{gw: gw, user: user, ws: ws}
	first := gw.add(c)
	defer func() {
		last := gw.remove(c)
		ws.Close()
		if last {
			gw.broadcastPresence(user.ID, false)
		}
	}()
	if first {
		gw.broadcastPresence(user.ID, true)
	}

	c.sendReady()

	ws.SetReadLimit(64 << 10)
	ws.SetReadDeadline(time.Now().Add(90 * time.Second))
	ws.SetPongHandler(func(string) error {
		ws.SetReadDeadline(time.Now().Add(90 * time.Second))
		return nil
	})
	go c.pingLoop()

	for {
		var m gwIn
		if err := ws.ReadJSON(&m); err != nil {
			return
		}
		ws.SetReadDeadline(time.Now().Add(90 * time.Second))
		gw.handle(c, m)
	}
}

type gwIn struct {
	T    string `json:"t"`
	Ch   string `json:"ch,omitempty"`
	Text string `json:"text,omitempty"`
	Img  string `json:"img,omitempty"`
	TS   int64  `json:"ts,omitempty"`
}

func (gw *Gateway) handle(c *gwConn, m gwIn) {
	switch m.T {
	case "ping":
		// эхо клиентской метки времени: RTT считает сам клиент и рисует
		// его как индикатор связи (в Discord это те самые палочки)
		c.send(out{"t": "pong", "ts": m.TS})

	case "history":
		g, _, ok := gw.st.ChannelGroup(m.Ch)
		if !ok || !g.HasMember(c.user.ID) {
			return
		}
		c.send(out{"t": "history", "ch": m.Ch, "msgs": gw.hydrate(m.Ch, gw.st.History(m.Ch))})

	case "chat":
		gw.PostChat(c.user, m.Ch, m.Text, m.Img)
	}
}

func (c *gwConn) pingLoop() {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for range t.C {
		c.mu.Lock()
		if c.dead {
			c.mu.Unlock()
			return
		}
		err := c.ws.WriteControl(websocket.PingMessage, nil, time.Now().Add(10*time.Second))
		c.mu.Unlock()
		if err != nil {
			return
		}
	}
}

func (c *gwConn) send(v any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.dead {
		return
	}
	c.ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if err := c.ws.WriteJSON(v); err != nil {
		c.dead = true
		c.ws.Close()
	}
}

// sendReady — полный снимок мира для клиента: он сразу рисует всё дерево,
// не делая ни одного дополнительного запроса.
func (c *gwConn) sendReady() {
	gw := c.gw
	groups := gw.st.GroupsOf(c.user.ID)
	c.send(out{
		"t":      "ready",
		"you":    c.user,
		"groups": gw.groupViews(groups),
	})
}

/* ---------- вид группы для клиента ---------- */

type GroupView struct {
	*store.Group
	Users  []auth.User              `json:"users"`  // профили участников
	Online map[string]bool          `json:"online"` // кто сейчас в сети
	Voice  map[string][]VoiceMember `json:"voice"`  // channel id -> кто в голосе
}

func (gw *Gateway) groupViews(gs []*store.Group) []GroupView {
	out := make([]GroupView, 0, len(gs))
	for _, g := range gs {
		out = append(out, gw.groupView(g))
	}
	return out
}

func (gw *Gateway) groupView(g *store.Group) GroupView {
	profiles := gw.st.Users(g.Members)
	users := make([]auth.User, 0, len(g.Members))
	online := map[string]bool{}
	for _, id := range g.Members {
		if u, ok := profiles[id]; ok {
			users = append(users, u)
		}
		if gw.isOnline(id) {
			online[id] = true
		}
	}
	voice := map[string][]VoiceMember{}
	for _, ch := range g.Channels {
		if ch.Kind != "voice" {
			continue
		}
		if vm := gw.hub.VoiceRoster(ch.ID); len(vm) > 0 {
			voice[ch.ID] = vm
		}
	}
	return GroupView{Group: g, Users: users, Online: online, Voice: voice}
}

/* ---------- реестр соединений ---------- */

// add возвращает true, если это первое соединение юзера (значит, он «зашёл»).
func (gw *Gateway) add(c *gwConn) bool {
	gw.mu.Lock()
	defer gw.mu.Unlock()
	set := gw.conns[c.user.ID]
	if set == nil {
		set = map[*gwConn]bool{}
		gw.conns[c.user.ID] = set
	}
	set[c] = true
	return len(set) == 1
}

// remove возвращает true, если соединений у юзера не осталось (он «вышел»).
func (gw *Gateway) remove(c *gwConn) bool {
	gw.mu.Lock()
	defer gw.mu.Unlock()
	c.mu.Lock()
	c.dead = true
	c.mu.Unlock()
	set := gw.conns[c.user.ID]
	if set == nil {
		return false
	}
	delete(set, c)
	if len(set) == 0 {
		delete(gw.conns, c.user.ID)
		return true
	}
	return false
}

func (gw *Gateway) isOnline(uid string) bool {
	gw.mu.RLock()
	defer gw.mu.RUnlock()
	return len(gw.conns[uid]) > 0
}

func (gw *Gateway) connsOf(uid string) []*gwConn {
	gw.mu.RLock()
	defer gw.mu.RUnlock()
	out := make([]*gwConn, 0, len(gw.conns[uid]))
	for c := range gw.conns[uid] {
		out = append(out, c)
	}
	return out
}

/* ---------- рассылки ---------- */

// ToUser шлёт событие всем устройствам одного юзера.
func (gw *Gateway) ToUser(uid string, v any) {
	for _, c := range gw.connsOf(uid) {
		c.send(v)
	}
}

// ToGroup шлёт событие всем участникам группы (кто сейчас на связи).
func (gw *Gateway) ToGroup(g *store.Group, v any) {
	for _, uid := range g.Members {
		gw.ToUser(uid, v)
	}
}

// GroupChanged — «состав/каналы группы изменились»: клиенты перерисовывают
// дерево мгновенно, без опроса REST.
func (gw *Gateway) GroupChanged(gid string) {
	g, ok := gw.st.Group(gid)
	if !ok {
		return
	}
	view := gw.groupView(g)
	gw.ToGroup(g, out{"t": "group", "group": view})
}

// GroupGone — группу удалили: убираем её у всех, кто в ней состоял.
func (gw *Gateway) GroupGone(g *store.Group) {
	gw.ToGroup(g, out{"t": "group-gone", "id": g.ID})
}

// broadcastPresence сообщает о входе/выходе юзера всем, с кем он в общих группах.
func (gw *Gateway) broadcastPresence(uid string, online bool) {
	seen := map[string]bool{}
	for _, g := range gw.st.GroupsOf(uid) {
		for _, other := range g.Members {
			if other == uid || seen[other] {
				continue
			}
			seen[other] = true
			gw.ToUser(other, out{"t": "presence", "id": uid, "online": online})
		}
	}
}

// VoiceChanged — состав голосового канала поменялся (зашёл/вышел/мут).
// Летит всей группе, поэтому «кто сидит в голосовом» видно, ещё не заходя в него.
func (gw *Gateway) VoiceChanged(chID string) {
	g, _, ok := gw.st.ChannelGroup(chID)
	if !ok {
		return
	}
	gw.ToGroup(g, out{"t": "voice", "ch": chID, "members": gw.hub.VoiceRoster(chID)})
}

/* ---------- чат ---------- */

func (gw *Gateway) hydrate(chID string, msgs []store.Msg) []OutMsg {
	ids := make([]string, 0, len(msgs))
	for _, m := range msgs {
		ids = append(ids, m.From)
	}
	profiles := gw.st.Users(ids)
	out := make([]OutMsg, 0, len(msgs))
	for _, m := range msgs {
		u, ok := profiles[m.From]
		if !ok {
			u = auth.User{ID: m.From, Name: "кто-то", Color: auth.ColorFor(m.From)}
		}
		out = append(out, OutMsg{ID: m.ID, Ch: chID, From: u, Text: m.Text, Img: m.Img, TS: m.TS})
	}
	return out
}

// PostChat кладёт сообщение в историю и рассылает участникам группы.
func (gw *Gateway) PostChat(u auth.User, chID, text, img string) {
	text = trimChat(text)
	// картинки принимаем только свои загруженные — чужие ссылки в чат не пускаем
	if img != "" && !isUploadPath(img) {
		img = ""
	}
	if text == "" && img == "" {
		return
	}
	g, ch, ok := gw.st.ChannelGroup(chID)
	if !ok || ch.Kind != "text" || !g.HasMember(u.ID) {
		return
	}
	m := gw.st.AddMsg(chID, u, text, img)
	gw.ToGroup(g, out{"t": "chat", "msg": OutMsg{
		ID: m.ID, Ch: chID, From: u, Text: m.Text, Img: m.Img, TS: m.TS,
	}})
}
