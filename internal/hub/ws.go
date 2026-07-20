package hub

import (
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pion/webrtc/v4"

	"github.com/slime4ik/backmess/internal/auth"
)

// inMsg — входящее сообщение клиента по голосовому ws.
type inMsg struct {
	T         string                   `json:"t"`
	Ch        string                   `json:"ch,omitempty"`
	SDP       string                   `json:"sdp,omitempty"`
	Candidate *webrtc.ICECandidateInit `json:"candidate,omitempty"`
	State     *MemberState             `json:"state,omitempty"`
}

func (h *Hub) upgrader() *websocket.Upgrader {
	return &websocket.Upgrader{
		ReadBufferSize:  4096,
		WriteBufferSize: 4096,
		CheckOrigin: func(r *http.Request) bool {
			o := r.Header.Get("Origin")
			if o == "" {
				return true
			}
			u, err := url.Parse(o)
			if err != nil {
				return false
			}
			if strings.EqualFold(u.Host, r.Host) {
				return true
			}
			base, err := url.Parse(h.cfg.BaseURL)
			return err == nil && strings.EqualFold(u.Host, base.Host)
		},
	}
}

// HandleVoiceWS: апгрейд, первое сообщение обязано быть join{ch}, дальше —
// сигналинг (answer/candidate) и статусы мут/глух.
func (h *Hub) HandleVoiceWS(w http.ResponseWriter, r *http.Request, user auth.User) {
	ws, err := h.upgrader().Upgrade(w, r, nil)
	if err != nil {
		return
	}

	ws.SetReadLimit(128 << 10)
	ws.SetReadDeadline(time.Now().Add(15 * time.Second))
	var join inMsg
	if err := ws.ReadJSON(&join); err != nil || join.T != "join" {
		ws.Close()
		return
	}

	// канал должен существовать, быть голосовым и принадлежать группе,
	// в которой юзер реально состоит
	g, ch, ok := h.st.ChannelGroup(strings.TrimSpace(join.Ch))
	if !ok || ch.Kind != "voice" {
		writeCloseErr(ws, "канала нет")
		return
	}
	if !g.HasMember(user.ID) {
		writeCloseErr(ws, "ты не в этой группе")
		return
	}

	room := h.getOrCreateRoom(ch.ID)
	m := &Member{
		ID:   user.ID,
		User: user,
		room: room,
		ws:   ws,
	}
	if err := h.newPeer(m); err != nil {
		log.Printf("hub: new peer: %v", err)
		writeCloseErr(ws, "webrtc недоступен")
		return
	}

	// в двух голосовых сразу быть нельзя: выкидываем прошлую сессию юзера
	if prev := h.trackUserRoom(user.ID, room); prev != nil {
		prev.evict(user.ID)
	}
	room.evict(user.ID) // и старое соединение в этой же комнате (перезаход)

	if !room.add(m) {
		m.pc.Close()
		writeCloseErr(ws, "в канале уже максимум народу")
		return
	}

	// keepalive: пингуем сами, браузер отвечает pong автоматически
	ws.SetReadDeadline(time.Now().Add(90 * time.Second))
	ws.SetPongHandler(func(string) error {
		ws.SetReadDeadline(time.Now().Add(90 * time.Second))
		return nil
	})
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for range t.C {
			if m.closed.Load() {
				return
			}
			m.wsMu.Lock()
			err := ws.WriteControl(websocket.PingMessage, nil, time.Now().Add(10*time.Second))
			m.wsMu.Unlock()
			if err != nil {
				go room.remove(m)
				return
			}
		}
	}()

	defer room.remove(m)
	for {
		var msg inMsg
		if err := ws.ReadJSON(&msg); err != nil {
			return
		}
		ws.SetReadDeadline(time.Now().Add(90 * time.Second))
		switch msg.T {
		case "answer":
			if msg.SDP == "" {
				continue
			}
			if err := m.pc.SetRemoteDescription(webrtc.SessionDescription{
				Type: webrtc.SDPTypeAnswer, SDP: msg.SDP,
			}); err != nil {
				log.Printf("hub: set answer: %v", err)
			}
		case "candidate":
			if msg.Candidate == nil {
				continue
			}
			if err := m.pc.AddICECandidate(*msg.Candidate); err != nil {
				log.Printf("hub: add candidate: %v", err)
			}
		case "state":
			if msg.State != nil {
				room.setState(m, *msg.State)
			}
		}
	}
}

// evict выкидывает прежнее соединение юзера (перезаход или переход в другой
// канал) — иначе он остался бы висеть призраком в списке участников.
func (r *Room) evict(uid string) {
	r.mu.Lock()
	m := r.members[uid]
	r.mu.Unlock()
	if m != nil {
		r.remove(m)
	}
}

func writeCloseErr(ws *websocket.Conn, reason string) {
	ws.WriteJSON(out{"t": "error", "msg": reason})
	ws.Close()
}
