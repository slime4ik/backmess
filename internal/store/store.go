// Package store — состояние приложения: группы, каналы, участники, история чата.
//
// Базы нет и не будет: всё живёт в памяти и переливается в один JSON-файл
// (DATA_DIR/state.json). Запись отложенная — пачка изменений схлопывается в
// одну запись на диск, поэтому чат не упирается в диск на каждом сообщении.
// Для компании друзей этого хватает с запасом, а бэкап — это `cp state.json`.
package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/slime4ik/backmess/internal/auth"
)

const (
	HistoryCap  = 300 // сообщений на канал в истории
	MaxChannels = 50
	MaxGroups   = 30 // на одного юзера
	MaxNameLen  = 32
)

var (
	ErrNotFound  = errors.New("не найдено")
	ErrForbidden = errors.New("нельзя")
	ErrBadName   = errors.New("имя: 1–32 символа")
	ErrTooMany   = errors.New("слишком много")
)

// Channel — текстовый или голосовой канал внутри группы.
type Channel struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"` // "text" | "voice"
}

// Group — «сервер» в терминах Discord: набор каналов + список участников.
type Group struct {
	ID       string     `json:"id"`
	Name     string     `json:"name"`
	Owner    string     `json:"owner"`  // user id
	Invite   string     `json:"invite"` // код приглашения, уникальный
	Channels []*Channel `json:"channels"`
	Members  []string   `json:"members"` // user id, порядок = порядок вступления
	Created  int64      `json:"created"`
}

func (g *Group) HasMember(uid string) bool {
	return slices.Contains(g.Members, uid)
}

func (g *Group) channel(id string) *Channel {
	for _, c := range g.Channels {
		if c.ID == id {
			return c
		}
	}
	return nil
}

// clone — копия для отдачи наружу, чтобы никто не менял состояние мимо лока.
func (g *Group) clone() *Group {
	cp := *g
	cp.Channels = make([]*Channel, len(g.Channels))
	for i, c := range g.Channels {
		cc := *c
		cp.Channels[i] = &cc
	}
	cp.Members = append([]string(nil), g.Members...)
	return &cp
}

// Msg — сообщение чата. Автор хранится по id, профиль подтягивается из Users.
type Msg struct {
	ID   int64  `json:"id"`
	From string `json:"from"`
	Text string `json:"text,omitempty"`
	Img  string `json:"img,omitempty"`
	TS   int64  `json:"ts"` // unix millis
}

type state struct {
	Groups map[string]*Group    `json:"groups"`
	Users  map[string]auth.User `json:"users"`
	Msgs   map[string][]Msg     `json:"msgs"` // channel id -> история
	Seq    int64                `json:"seq"`
}

type Store struct {
	path string

	mu sync.RWMutex
	s  state

	saveMu   sync.Mutex
	dirty    chan struct{}
	closed   chan struct{}
	closeOne sync.Once
}

func Open(dataDir string) (*Store, error) {
	st := &Store{
		path:   filepath.Join(dataDir, "state.json"),
		dirty:  make(chan struct{}, 1),
		closed: make(chan struct{}),
	}
	st.s = state{
		Groups: map[string]*Group{},
		Users:  map[string]auth.User{},
		Msgs:   map[string][]Msg{},
	}
	if b, err := os.ReadFile(st.path); err == nil {
		if err := json.Unmarshal(b, &st.s); err != nil {
			return nil, err
		}
		// файл мог быть записан старой версией — добиваем нули
		if st.s.Groups == nil {
			st.s.Groups = map[string]*Group{}
		}
		if st.s.Users == nil {
			st.s.Users = map[string]auth.User{}
		}
		if st.s.Msgs == nil {
			st.s.Msgs = map[string][]Msg{}
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	go st.saveLoop()
	return st, nil
}

// touch помечает состояние изменённым; реальная запись — в saveLoop.
func (s *Store) touch() {
	select {
	case s.dirty <- struct{}{}:
	default:
	}
}

func (s *Store) saveLoop() {
	for {
		select {
		case <-s.closed:
			return
		case <-s.dirty:
			// схлопываем шторм изменений в одну запись
			select {
			case <-time.After(500 * time.Millisecond):
			case <-s.closed:
			}
			s.Flush()
		}
	}
}

// Flush пишет состояние на диск атомарно (temp + rename).
func (s *Store) Flush() error {
	s.saveMu.Lock()
	defer s.saveMu.Unlock()

	s.mu.RLock()
	b, err := json.Marshal(&s.s)
	s.mu.RUnlock()
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *Store) Close() error {
	s.closeOne.Do(func() { close(s.closed) })
	return s.Flush()
}

/* ---------- юзеры ---------- */

// SaveUser кэширует профиль: нужен, чтобы показывать оффлайн-участников и
// авторов старых сообщений, когда самого юзера сейчас нет на связи.
func (s *Store) SaveUser(u auth.User) {
	s.mu.Lock()
	old, ok := s.s.Users[u.ID]
	if !ok || old != u {
		s.s.Users[u.ID] = u
		s.mu.Unlock()
		s.touch()
		return
	}
	s.mu.Unlock()
}

func (s *Store) User(id string) (auth.User, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u, ok := s.s.Users[id]
	return u, ok
}

// Users — профили пачкой (для отрисовки списка участников группы).
func (s *Store) Users(ids []string) map[string]auth.User {
	out := make(map[string]auth.User, len(ids))
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, id := range ids {
		if u, ok := s.s.Users[id]; ok {
			out[id] = u
		}
	}
	return out
}

/* ---------- группы ---------- */

func validName(s string) (string, bool) {
	s = strings.TrimSpace(s)
	n := len([]rune(s))
	return s, n >= 1 && n <= MaxNameLen
}

func (s *Store) CreateGroup(owner auth.User, name string) (*Group, error) {
	name, ok := validName(name)
	if !ok {
		return nil, ErrBadName
	}
	s.SaveUser(owner)

	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, g := range s.s.Groups {
		if g.Owner == owner.ID {
			n++
		}
	}
	if n >= MaxGroups {
		return nil, ErrTooMany
	}
	g := &Group{
		ID:      auth.RandID(12),
		Name:    name,
		Owner:   owner.ID,
		Invite:  auth.RandID(10),
		Members: []string{owner.ID},
		Created: time.Now().UnixMilli(),
		Channels: []*Channel{
			{ID: auth.RandID(12), Name: "общий", Kind: "text"},
			{ID: auth.RandID(12), Name: "голосовой", Kind: "voice"},
		},
	}
	s.s.Groups[g.ID] = g
	s.touch()
	return g.clone(), nil
}

func (s *Store) Group(id string) (*Group, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	g, ok := s.s.Groups[id]
	if !ok {
		return nil, false
	}
	return g.clone(), true
}

// GroupsOf — группы юзера, в стабильном порядке (по времени создания).
func (s *Store) GroupsOf(uid string) []*Group {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []*Group{}
	for _, g := range s.s.Groups {
		if g.HasMember(uid) {
			out = append(out, g.clone())
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created < out[j].Created })
	return out
}

// IsMember — проверка доступа; всё, что меняет группу, обязано её звать.
func (s *Store) IsMember(gid, uid string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	g, ok := s.s.Groups[gid]
	return ok && g.HasMember(uid)
}

// JoinByInvite — вход по коду приглашения. Возвращает группу и признак
// «реально вступил» (false, если уже состоял — тогда просто открываем её).
func (s *Store) JoinByInvite(code string, u auth.User) (*Group, bool, error) {
	code = strings.TrimSpace(code)
	if code == "" {
		return nil, false, ErrNotFound
	}
	s.SaveUser(u)

	s.mu.Lock()
	defer s.mu.Unlock()
	for _, g := range s.s.Groups {
		if g.Invite != code {
			continue
		}
		if g.HasMember(u.ID) {
			return g.clone(), false, nil
		}
		g.Members = append(g.Members, u.ID)
		s.touch()
		return g.clone(), true, nil
	}
	return nil, false, ErrNotFound
}

// LeaveGroup: владелец выйти не может — только удалить группу целиком,
// иначе получится группа-сирота, которой никто не управляет.
func (s *Store) LeaveGroup(gid, uid string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.s.Groups[gid]
	if !ok {
		return ErrNotFound
	}
	if g.Owner == uid {
		return ErrForbidden
	}
	for i, m := range g.Members {
		if m == uid {
			g.Members = append(g.Members[:i], g.Members[i+1:]...)
			s.touch()
			return nil
		}
	}
	return ErrNotFound
}

func (s *Store) DeleteGroup(gid, uid string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.s.Groups[gid]
	if !ok {
		return ErrNotFound
	}
	if g.Owner != uid {
		return ErrForbidden
	}
	for _, c := range g.Channels {
		delete(s.s.Msgs, c.ID)
	}
	delete(s.s.Groups, gid)
	s.touch()
	return nil
}

// ResetInvite — перевыпуск ссылки, если старая утекла.
func (s *Store) ResetInvite(gid, uid string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.s.Groups[gid]
	if !ok {
		return "", ErrNotFound
	}
	if g.Owner != uid {
		return "", ErrForbidden
	}
	g.Invite = auth.RandID(10)
	s.touch()
	return g.Invite, nil
}

/* ---------- каналы ---------- */

func (s *Store) AddChannel(gid, uid, name, kind string) (*Channel, error) {
	name, ok := validName(name)
	if !ok {
		return nil, ErrBadName
	}
	if kind != "text" && kind != "voice" {
		return nil, ErrBadName
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.s.Groups[gid]
	if !ok {
		return nil, ErrNotFound
	}
	if !g.HasMember(uid) {
		return nil, ErrForbidden
	}
	if len(g.Channels) >= MaxChannels {
		return nil, ErrTooMany
	}
	c := &Channel{ID: auth.RandID(12), Name: name, Kind: kind}
	g.Channels = append(g.Channels, c)
	s.touch()
	cc := *c
	return &cc, nil
}

// DeleteChannel: удалять может владелец группы. Последний канал не сносим,
// иначе группа превращается в пустое место без единого способа что-то сказать.
func (s *Store) DeleteChannel(gid, uid, chid string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.s.Groups[gid]
	if !ok {
		return ErrNotFound
	}
	if g.Owner != uid {
		return ErrForbidden
	}
	if len(g.Channels) <= 1 {
		return ErrForbidden
	}
	for i, c := range g.Channels {
		if c.ID == chid {
			g.Channels = append(g.Channels[:i], g.Channels[i+1:]...)
			delete(s.s.Msgs, chid)
			s.touch()
			return nil
		}
	}
	return ErrNotFound
}

func (s *Store) RenameChannel(gid, uid, chid, name string) error {
	name, ok := validName(name)
	if !ok {
		return ErrBadName
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.s.Groups[gid]
	if !ok {
		return ErrNotFound
	}
	if g.Owner != uid {
		return ErrForbidden
	}
	c := g.channel(chid)
	if c == nil {
		return ErrNotFound
	}
	c.Name = name
	s.touch()
	return nil
}

// ChannelGroup — к какой группе относится канал (нужно для проверки доступа
// по одному лишь id канала: и для чата, и для входа в голосовой).
func (s *Store) ChannelGroup(chid string) (*Group, *Channel, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, g := range s.s.Groups {
		if c := g.channel(chid); c != nil {
			cc := *c
			return g.clone(), &cc, true
		}
	}
	return nil, nil, false
}

/* ---------- сообщения ---------- */

func (s *Store) AddMsg(chid string, from auth.User, text, img string) Msg {
	s.SaveUser(from)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.s.Seq++
	m := Msg{ID: s.s.Seq, From: from.ID, Text: text, Img: img, TS: time.Now().UnixMilli()}
	h := append(s.s.Msgs[chid], m)
	if len(h) > HistoryCap {
		h = h[len(h)-HistoryCap:]
	}
	s.s.Msgs[chid] = h
	s.touch()
	return m
}

func (s *Store) History(chid string) []Msg {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]Msg(nil), s.s.Msgs[chid]...)
}
