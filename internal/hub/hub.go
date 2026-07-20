// Package hub — голосовые комнаты (SFU на Pion) и gateway-соединение клиента.
//
// Разделение такое:
//   - Gateway (gateway.go) — постоянный ws: presence, дерево групп, текстовый чат.
//   - Room (этот файл + sfu.go) — временный ws на время звонка: одна комната
//     соответствует одному голосовому каналу, участники публикуют до трёх
//     треков (mic/cam/screen), сервер пересылает RTP остальным.
//
// Комнаты живут только пока в них кто-то есть — вся долгоживущая структура
// (группы, каналы, история) лежит в store и переживает рестарт.
package hub

import (
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pion/webrtc/v4"

	"github.com/slime4ik/backmess/internal/auth"
	"github.com/slime4ik/backmess/internal/config"
	"github.com/slime4ik/backmess/internal/store"
)

const (
	roomCap    = 32
	maxChatLen = 2000
)

type Hub struct {
	cfg *config.Config
	st  *store.Store
	api *webrtc.API
	gw  *Gateway

	mu       sync.Mutex
	rooms    map[string]*Room // ключ — id голосового канала
	userRoom map[string]*Room // user id -> где он сейчас в голосе
}

func New(cfg *config.Config, st *store.Store) (*Hub, error) {
	api, err := newWebRTCAPI(cfg)
	if err != nil {
		return nil, err
	}
	h := &Hub{
		cfg: cfg, st: st, api: api,
		rooms:    map[string]*Room{},
		userRoom: map[string]*Room{},
	}
	go h.keyFrameLoop()
	return h, nil
}

// keyFrameLoop раз в 3 секунды просит у всех видео-паблишеров ключевой кадр,
// чтобы новые зрители не смотрели на чёрный экран (как в pion sfu-ws).
func (h *Hub) keyFrameLoop() {
	for range time.NewTicker(3 * time.Second).C {
		for _, r := range h.roomList() {
			r.mu.Lock()
			r.dispatchKeyFrames()
			r.mu.Unlock()
		}
	}
}

func (h *Hub) roomList() []*Room {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]*Room, 0, len(h.rooms))
	for _, r := range h.rooms {
		out = append(out, r)
	}
	return out
}

func (h *Hub) getOrCreateRoom(chID string) *Room {
	h.mu.Lock()
	defer h.mu.Unlock()
	if r, ok := h.rooms[chID]; ok {
		return r
	}
	r := &Room{
		hub:       h,
		ChannelID: chID,
		members:   map[string]*Member{},
		tracks:    map[string]*roomTrack{},
	}
	h.rooms[chID] = r
	return r
}

// maybeDropRoom удаляет комнату, когда из неё вышел последний участник:
// ничего долгоживущего в ней нет, а состав голосового канала всё равно
// пересобирается при следующем входе.
func (h *Hub) maybeDropRoom(r *Room) {
	h.mu.Lock()
	defer h.mu.Unlock()
	r.mu.Lock()
	empty := len(r.members) == 0
	r.mu.Unlock()
	if empty {
		delete(h.rooms, r.ChannelID)
	}
}

// VoiceRoster — кто сейчас сидит в голосовом канале и в каком состоянии.
// Читается gateway'ем, чтобы состав было видно всей группе, не заходя внутрь.
func (h *Hub) VoiceRoster(chID string) []VoiceMember {
	h.mu.Lock()
	r, ok := h.rooms[chID]
	h.mu.Unlock()
	if !ok {
		return []VoiceMember{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]VoiceMember, 0, len(r.members))
	for _, m := range r.members {
		out = append(out, VoiceMember{User: m.User, State: m.State})
	}
	sortVoice(out)
	return out
}

func sortVoice(v []VoiceMember) {
	for i := 1; i < len(v); i++ {
		for j := i; j > 0 && v[j].User.Name < v[j-1].User.Name; j-- {
			v[j], v[j-1] = v[j-1], v[j]
		}
	}
}

// ---- Room ----

type Room struct {
	hub       *Hub
	ChannelID string

	mu      sync.Mutex
	members map[string]*Member    // ключ — user id: один юзер = одно место в голосе
	tracks  map[string]*roomTrack // ключ — streamID вида "<userID>:<mic|cam|screen>"
}

type roomTrack struct {
	owner string // user id паблишера
	kind  string // mic | cam | screen
	local *webrtc.TrackLocalStaticRTP
}

type MemberState struct {
	Muted    bool `json:"muted"`
	Deafened bool `json:"deafened"`
	Cam      bool `json:"cam"`
	Screen   bool `json:"screen"`
}

type MemberInfo struct {
	ID    string      `json:"id"` // = user id
	User  auth.User   `json:"user"`
	State MemberState `json:"state"`
}

type Member struct {
	ID    string // = user id
	User  auth.User
	State MemberState

	room *Room
	ws   *websocket.Conn
	wsMu sync.Mutex
	pc   *webrtc.PeerConnection

	// приёмные трансиверы в фиксированном порядке m-line: mic, cam, screen
	recvMic, recvCam, recvScreen *webrtc.RTPTransceiver

	closed atomic.Bool
}

func (m *Member) info() MemberInfo {
	return MemberInfo{ID: m.ID, User: m.User, State: m.State}
}

// send пишет JSON в WebSocket; при ошибке участник асинхронно выкидывается.
func (m *Member) send(v any) {
	if m.closed.Load() {
		return
	}
	m.wsMu.Lock()
	m.ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
	err := m.ws.WriteJSON(v)
	m.wsMu.Unlock()
	if err != nil {
		go m.room.remove(m)
	}
}

// add добавляет участника: hello/members уходят под локом, чтобы никакой
// offer не мог проскочить раньше hello.
func (r *Room) add(m *Member) bool {
	r.mu.Lock()
	if len(r.members) >= roomCap {
		r.mu.Unlock()
		return false
	}
	r.members[m.ID] = m
	infos := []MemberInfo{}
	for _, o := range r.members {
		infos = append(infos, o.info())
	}
	m.send(out{"t": "hello", "you": m.info(), "ch": r.ChannelID})
	m.send(out{"t": "members", "members": infos})
	targets := r.othersOf(m)
	r.mu.Unlock()

	for _, o := range targets {
		o.send(out{"t": "member-join", "member": m.info()})
	}
	r.signal()
	r.notifyVoice()
	return true
}

func (r *Room) othersOf(m *Member) []*Member {
	list := make([]*Member, 0, len(r.members))
	for _, o := range r.members {
		if o.ID != m.ID {
			list = append(list, o)
		}
	}
	return list
}

// remove — единственная точка выхода участника (закрытие ws, падение pc и т.д.).
func (r *Room) remove(m *Member) {
	if !m.closed.CompareAndSwap(false, true) {
		return
	}
	r.mu.Lock()
	// не выкидываем чужую сессию, если юзер уже перезашёл другим соединением
	if cur, ok := r.members[m.ID]; ok && cur == m {
		delete(r.members, m.ID)
	}
	for sid, t := range r.tracks {
		if t.owner == m.ID {
			delete(r.tracks, sid)
		}
	}
	targets := r.othersOf(m)
	empty := len(r.members) == 0
	r.mu.Unlock()

	r.hub.forgetUserRoom(m.ID, r)

	if m.pc != nil {
		m.pc.Close()
	}
	m.ws.Close()

	for _, o := range targets {
		o.send(out{"t": "member-leave", "id": m.ID})
	}
	if empty {
		r.hub.maybeDropRoom(r)
	} else {
		r.signal()
	}
	r.notifyVoice()
}

func (r *Room) setState(m *Member, st MemberState) {
	r.mu.Lock()
	m.State = st
	targets := r.othersOf(m)
	r.mu.Unlock()
	for _, o := range targets {
		o.send(out{"t": "member-state", "id": m.ID, "state": st})
	}
	r.notifyVoice()
}

// notifyVoice — сообщить всей группе, что состав/состояние голосового канала
// поменялись. Благодаря этому в сайдбаре под каналом сразу видно, кто зашёл
// и кто в муте, даже если сам ты в этот канал не заходил.
func (r *Room) notifyVoice() {
	if r.hub.gw != nil {
		go r.hub.gw.VoiceChanged(r.ChannelID)
	}
}

func (h *Hub) forgetUserRoom(uid string, r *Room) {
	h.mu.Lock()
	if cur, ok := h.userRoom[uid]; ok && cur == r {
		delete(h.userRoom, uid)
	}
	h.mu.Unlock()
}

// trackUserRoom запоминает, где юзер сидит в голосе, и возвращает предыдущую
// комнату — её надо покинуть (в двух голосовых каналах сразу быть нельзя).
func (h *Hub) trackUserRoom(uid string, r *Room) *Room {
	h.mu.Lock()
	prev := h.userRoom[uid]
	h.userRoom[uid] = r
	h.mu.Unlock()
	if prev == r {
		return nil
	}
	return prev
}

func trimChat(text string) string {
	text = strings.TrimSpace(text)
	if n := []rune(text); len(n) > maxChatLen {
		text = string(n[:maxChatLen])
	}
	return text
}

func isUploadPath(p string) bool {
	return strings.HasPrefix(p, "/u/")
}

// out — исходящее ws-сообщение.
type out map[string]any
