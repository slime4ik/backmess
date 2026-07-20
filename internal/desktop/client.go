package desktop

import (
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"

	"github.com/slime4ik/backmess/internal/hub"
)

// VoiceEvents — события голосового канала. Вызываются из горутин клиента,
// UI обязан заворачивать обновления в fyne.Do.
type VoiceEvents struct {
	OnHello       func(you hub.MemberInfo, chID string)
	OnMembers     func([]hub.MemberInfo)
	OnMemberJoin  func(hub.MemberInfo)
	OnMemberLeave func(id string)
	OnMemberState func(id string, st hub.MemberState)
	OnRTC         func(state string)
	OnClosed      func(reason string)
}

type wsMsg struct {
	T         string                   `json:"t"`
	Ch        string                   `json:"ch,omitempty"`
	SDP       string                   `json:"sdp,omitempty"`
	Candidate *webrtc.ICECandidateInit `json:"candidate,omitempty"`
	State     *hub.MemberState         `json:"state,omitempty"`
}

// VoiceClient — одно подключение к голосовому каналу: ws-сигналинг +
// PeerConnection + микрофон.
type VoiceClient struct {
	api   *API
	audio *AudioEngine
	ev    VoiceEvents

	ChannelID string
	MyID      string // = user id
	JoinedAt  time.Time

	ws     *websocket.Conn
	wsMu   sync.Mutex
	pc     *webrtc.PeerConnection
	mic    *webrtc.TrackLocalStaticSample
	state  hub.MemberState
	stMu   sync.Mutex
	closed atomic.Bool
	rttMS  atomic.Int64
}

func JoinVoice(api *API, audio *AudioEngine, chID string, ev VoiceEvents) (*VoiceClient, error) {
	hdr := map[string][]string{"Cookie": {sessionCookie + "=" + api.Token}}
	ws, _, err := websocket.DefaultDialer.Dial(api.WSURL(), hdr)
	if err != nil {
		return nil, err
	}
	c := &VoiceClient{
		api: api, audio: audio, ev: ev,
		ChannelID: chID, ws: ws, JoinedAt: time.Now(),
	}
	if err := c.buildPC(); err != nil {
		ws.Close()
		return nil, err
	}
	c.send(wsMsg{T: "join", Ch: chID})
	go c.readLoop()
	go c.statsLoop()
	return c, nil
}

// RTTMS — задержка до сервера по медиа-каналу (то, что в Discord показывают
// палочками качества связи). 0 — ещё не измерено.
func (c *VoiceClient) RTTMS() int { return int(c.rttMS.Load()) }

// statsLoop тянет RTT из статистики ICE-пары: это реальная задержка голоса,
// а не задержка HTTP, и именно она объясняет «меня плохо слышно».
func (c *VoiceClient) statsLoop() {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for range t.C {
		if c.closed.Load() || c.pc == nil {
			return
		}
		for _, s := range c.pc.GetStats() {
			pair, ok := s.(webrtc.ICECandidatePairStats)
			if !ok || pair.State != webrtc.StatsICECandidatePairStateSucceeded {
				continue
			}
			if pair.CurrentRoundTripTime > 0 {
				c.rttMS.Store(int64(pair.CurrentRoundTripTime * 1000))
				break
			}
		}
	}
}

func (c *VoiceClient) send(m wsMsg) {
	c.wsMu.Lock()
	defer c.wsMu.Unlock()
	if c.ws != nil {
		c.ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
		c.ws.WriteJSON(m)
	}
}

func (c *VoiceClient) buildPC() error {
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{{URLs: []string{"stun:stun.l.google.com:19302"}}},
	})
	if err != nil {
		return err
	}
	c.pc = pc

	mic, err := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2},
		"mic", "mic")
	if err != nil {
		return err
	}
	c.mic = mic

	// порядок m-line обязан совпадать с сервером: mic, cam, screen
	if _, err := pc.AddTransceiverFromTrack(mic, webrtc.RTPTransceiverInit{
		Direction: webrtc.RTPTransceiverDirectionSendrecv,
	}); err != nil {
		return err
	}
	for i := 0; i < 2; i++ {
		if _, err := pc.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo, webrtc.RTPTransceiverInit{
			Direction: webrtc.RTPTransceiverDirectionSendrecv,
		}); err != nil {
			return err
		}
	}

	pc.OnICECandidate(func(cand *webrtc.ICECandidate) {
		if cand == nil {
			return
		}
		init := cand.ToJSON()
		c.send(wsMsg{T: "candidate", Candidate: &init})
	})
	pc.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		if c.ev.OnRTC != nil {
			c.ev.OnRTC(s.String())
		}
		if s == webrtc.PeerConnectionStateFailed {
			c.Close("связь оборвалась")
		}
	})
	pc.OnTrack(func(t *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		// streamID = "<user id>:<mic|cam|screen>" — по нему сразу понятно,
		// чей это звук, и индикатор «говорит» вешается на нужного участника
		id, kind, ok := strings.Cut(t.StreamID(), ":")
		if !ok {
			return
		}
		if t.Kind() == webrtc.RTPCodecTypeAudio && kind == "mic" {
			go func() {
				defer c.audio.RemoveSource(id)
				for {
					pkt, _, err := t.ReadRTP()
					if err != nil {
						return
					}
					c.audio.Ingest(id, pkt.Payload)
				}
			}()
			return
		}
		// видео (чей-то экран/камера) в нативном клиенте пока не рендерим —
		// просто вычитываем, чтобы не копился буфер
		go func() {
			buf := make([]byte, 1600)
			for {
				if _, _, err := t.Read(buf); err != nil {
					return
				}
			}
		}()
	})

	c.audio.OnMicFrame = func(data []byte) {
		c.mic.WriteSample(media.Sample{Data: data, Duration: 20 * time.Millisecond})
	}
	return nil
}

func (c *VoiceClient) readLoop() {
	defer c.Close("соединение закрыто")
	for {
		var raw map[string]json.RawMessage
		if err := c.ws.ReadJSON(&raw); err != nil {
			return
		}
		var t string
		json.Unmarshal(raw["t"], &t)
		get := func(key string, v any) bool {
			b, ok := raw[key]
			return ok && json.Unmarshal(b, v) == nil
		}
		switch t {
		case "hello":
			var you hub.MemberInfo
			var ch string
			get("you", &you)
			get("ch", &ch)
			c.MyID = you.ID
			if c.ev.OnHello != nil {
				c.ev.OnHello(you, ch)
			}
		case "members":
			var ms []hub.MemberInfo
			get("members", &ms)
			if c.ev.OnMembers != nil {
				c.ev.OnMembers(ms)
			}
		case "member-join":
			var m hub.MemberInfo
			get("member", &m)
			if c.ev.OnMemberJoin != nil {
				c.ev.OnMemberJoin(m)
			}
		case "member-leave":
			var id string
			get("id", &id)
			c.audio.RemoveSource(id)
			if c.ev.OnMemberLeave != nil {
				c.ev.OnMemberLeave(id)
			}
		case "member-state":
			var id string
			var st hub.MemberState
			get("id", &id)
			get("state", &st)
			if c.ev.OnMemberState != nil {
				c.ev.OnMemberState(id, st)
			}
		case "offer":
			var sdp string
			get("sdp", &sdp)
			if err := c.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: sdp}); err != nil {
				continue
			}
			ans, err := c.pc.CreateAnswer(nil)
			if err != nil {
				continue
			}
			if err := c.pc.SetLocalDescription(ans); err != nil {
				continue
			}
			c.send(wsMsg{T: "answer", SDP: c.pc.LocalDescription().SDP})
		case "candidate":
			var cand webrtc.ICECandidateInit
			if get("candidate", &cand) {
				c.pc.AddICECandidate(cand)
			}
		case "error":
			var msg string
			get("msg", &msg)
			c.Close(msg)
			return
		}
	}
}

func (c *VoiceClient) SetState(muted, deafened bool) {
	c.stMu.Lock()
	c.state.Muted = muted
	c.state.Deafened = deafened
	st := c.state
	c.stMu.Unlock()
	c.audio.SetMuted(muted)
	c.audio.SetDeafened(deafened)
	c.send(wsMsg{T: "state", State: &st})
}

func (c *VoiceClient) Close(reason string) {
	if !c.closed.CompareAndSwap(false, true) {
		return
	}
	c.audio.OnMicFrame = nil
	c.audio.ClearSources()
	if c.pc != nil {
		c.pc.Close()
	}
	c.wsMu.Lock()
	if c.ws != nil {
		c.ws.Close()
	}
	c.wsMu.Unlock()
	if c.ev.OnClosed != nil {
		c.ev.OnClosed(reason)
	}
}
