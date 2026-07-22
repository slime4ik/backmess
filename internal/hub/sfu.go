package hub

import (
	"errors"
	"io"
	"log"
	"net"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"

	"github.com/slime4ik/backmess/internal/config"
)

// newWebRTCAPI поднимает весь медиа-стек на ОДНОМ порту (UDP + TCP fallback):
// так на сервере достаточно открыть один порт в файрволе, а TURN не нужен вовсе —
// клиенты всегда ходят напрямую к публичному IP сервера (мы же SFU).
func newWebRTCAPI(cfg *config.Config) (*webrtc.API, error) {
	se := webrtc.SettingEngine{}

	udpLn, err := net.ListenUDP("udp", &net.UDPAddr{Port: cfg.MediaPort})
	if err != nil {
		return nil, err
	}
	se.SetICEUDPMux(webrtc.NewICEUDPMux(nil, udpLn))

	tcpLn, err := net.ListenTCP("tcp", &net.TCPAddr{Port: cfg.MediaPort})
	if err != nil {
		return nil, err
	}
	se.SetICETCPMux(webrtc.NewICETCPMux(nil, tcpLn, 8))

	se.SetNetworkTypes([]webrtc.NetworkType{
		webrtc.NetworkTypeUDP4, webrtc.NetworkTypeUDP6,
		webrtc.NetworkTypeTCP4, webrtc.NetworkTypeTCP6,
	})
	se.SetIncludeLoopbackCandidate(true) // для локального теста на 127.0.0.1

	// Дефолты Pion слишком строгие для живого интернета: 5 секунд тишины —
	// уже «disconnected», 25 — «failed» и участник вылетает из канала.
	// У человека с вайфаем или мобильным такое бывает регулярно, а он этого
	// даже не замечает. Даём минуту на восстановление и чаще шлём keepalive,
	// чтобы NAT не закрывал сопоставление, пока никто не говорит.
	se.SetICETimeouts(
		15*time.Second, // disconnected: короткие провалы связи не считаем обрывом
		60*time.Second, // failed: только после минуты реально считаем связь мёртвой
		2*time.Second,  // keepalive: держим NAT открытым
	)
	if cfg.PublicIP != "" {
		// подменяем локальный адрес в кандидатах на публичный IP сервера
		if err := se.SetICEAddressRewriteRules(webrtc.ICEAddressRewriteRule{
			External:        []string{cfg.PublicIP},
			AsCandidateType: webrtc.ICECandidateTypeHost,
		}); err != nil {
			return nil, err
		}
	}

	me := &webrtc.MediaEngine{}
	if err := me.RegisterDefaultCodecs(); err != nil {
		return nil, err
	}
	ir := &interceptor.Registry{}
	if err := webrtc.RegisterDefaultInterceptors(me, ir); err != nil {
		return nil, err
	}

	return webrtc.NewAPI(
		webrtc.WithMediaEngine(me),
		webrtc.WithInterceptorRegistry(ir),
		webrtc.WithSettingEngine(se),
	), nil
}

// newPeer создаёт PeerConnection участника с тремя приёмными слотами
// в фиксированном порядке m-line: mic (audio), cam (video), screen (video).
// Клиент прикрепляет свои треки к этим же слотам через replaceTrack —
// поэтому после первого рукопожатия ре-негосиация нужна только серверу
// (когда меняется состав чужих треков), glare невозможен по построению.
func (h *Hub) newPeer(m *Member) error {
	pc, err := h.api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		return err
	}
	m.pc = pc

	kinds := []webrtc.RTPCodecType{
		webrtc.RTPCodecTypeAudio, // mic
		webrtc.RTPCodecTypeVideo, // cam
		webrtc.RTPCodecTypeVideo, // screen
	}
	txs := make([]*webrtc.RTPTransceiver, 0, len(kinds))
	for _, k := range kinds {
		tx, err := pc.AddTransceiverFromKind(k, webrtc.RTPTransceiverInit{
			Direction: webrtc.RTPTransceiverDirectionRecvonly,
		})
		if err != nil {
			pc.Close()
			return err
		}
		txs = append(txs, tx)
	}
	m.recvMic, m.recvCam, m.recvScreen = txs[0], txs[1], txs[2]

	pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil {
			return
		}
		init := c.ToJSON()
		m.send(out{"t": "candidate", "candidate": init})
	})

	pc.OnTrack(func(t *webrtc.TrackRemote, recv *webrtc.RTPReceiver) {
		h.onTrack(m, t, recv)
	})

	pc.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		switch s {
		case webrtc.PeerConnectionStateFailed, webrtc.PeerConnectionStateClosed:
			go m.room.remove(m)
		}
	})

	return nil
}

// onTrack: пришло медиа от участника — заводим локальный трек-ретранслятор
// и гоняем RTP как есть (без транскодинга, поэтому серверу почти не нужен CPU).
func (h *Hub) onTrack(m *Member, t *webrtc.TrackRemote, recv *webrtc.RTPReceiver) {
	kind := ""
	switch recv {
	case m.recvMic.Receiver():
		kind = "mic"
	case m.recvCam.Receiver():
		kind = "cam"
	case m.recvScreen.Receiver():
		kind = "screen"
	}
	if kind == "" {
		return
	}

	streamID := m.ID + ":" + kind
	local, err := webrtc.NewTrackLocalStaticRTP(t.Codec().RTPCodecCapability, kind, streamID)
	if err != nil {
		log.Printf("hub: create local track: %v", err)
		return
	}

	r := m.room
	r.addLocalTrack(streamID, &roomTrack{owner: m.ID, kind: kind, local: local})
	defer r.removeLocalTrack(streamID)

	buf := make([]byte, 1500)
	for {
		n, _, err := t.Read(buf)
		if err != nil {
			return // трек умер вместе с peer connection
		}
		if _, err = local.Write(buf[:n]); err != nil && !errors.Is(err, io.ErrClosedPipe) {
			return
		}
	}
}

func (r *Room) addLocalTrack(id string, t *roomTrack) {
	r.mu.Lock()
	r.tracks[id] = t
	r.mu.Unlock()
	r.signal()
}

func (r *Room) removeLocalTrack(id string) {
	r.mu.Lock()
	_, ok := r.tracks[id]
	delete(r.tracks, id)
	r.mu.Unlock()
	if ok {
		r.signal()
	}
}

// signal приводит подписки всех участников комнаты в соответствие текущему
// набору треков и рассылает всем свежие offer'ы. Порт паттерна pion sfu-ws.
func (r *Room) signal() {
	r.mu.Lock()
	defer r.mu.Unlock()

	attemptSync := func() (again bool) {
		for _, m := range r.members {
			if m.pc.ConnectionState() == webrtc.PeerConnectionStateClosed {
				continue // его уберёт remove()
			}

			existing := map[string]bool{}
			for _, sender := range m.pc.GetSenders() {
				if sender.Track() == nil {
					continue
				}
				sid := sender.Track().StreamID()
				tr, ok := r.tracks[sid]
				if !ok || tr.owner == m.ID {
					if err := m.pc.RemoveTrack(sender); err != nil {
						return true
					}
					continue
				}
				existing[sid] = true
			}
			for sid, tr := range r.tracks {
				if tr.owner == m.ID || existing[sid] {
					continue // свои треки назад не шлём
				}
				if _, err := m.pc.AddTrack(tr.local); err != nil {
					return true
				}
			}

			if m.pc.SignalingState() != webrtc.SignalingStateStable {
				return true // ждёт answer — повторим позже
			}
			offer, err := m.pc.CreateOffer(nil)
			if err != nil {
				return true
			}
			if err = m.pc.SetLocalDescription(offer); err != nil {
				return true
			}
			m.send(out{"t": "offer", "sdp": offer.SDP})
		}
		return false
	}

	for attempt := 0; ; attempt++ {
		if attempt == 25 {
			// кто-то застрял в негосиации — отпускаем лок и пробуем через 3с
			time.AfterFunc(3*time.Second, r.signal)
			return
		}
		if !attemptSync() {
			break
		}
	}
	r.dispatchKeyFrames()
}

// dispatchKeyFrames шлёт PLI всем паблишерам видео. Вызывать под r.mu.
func (r *Room) dispatchKeyFrames() {
	for _, m := range r.members {
		for _, recv := range m.pc.GetReceivers() {
			if recv.Track() == nil {
				continue
			}
			_ = m.pc.WriteRTCP([]rtcp.Packet{
				&rtcp.PictureLossIndication{MediaSSRC: uint32(recv.Track().SSRC())},
			})
		}
	}
}
