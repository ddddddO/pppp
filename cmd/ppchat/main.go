package main

import (
	"bufio"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pion/webrtc/v4"
)

// シグナリング用メッセージ構造体
type SignalMessage struct {
	Type      string                     `json:"type"` // "hello", "offer", "answer", "candidate"
	Room      string                     `json:"room"`
	SenderID  string                     `json:"senderId"` // 自分のID
	SDP       *webrtc.SessionDescription `json:"sdp,omitempty"`
	Candidate *webrtc.ICECandidateInit   `json:"candidate,omitempty"`
}

func main() {
	// 1. フラグの定義
	serverURL := flag.String("server", "wss://pppp-9hxy.onrender.com/ws_ppchat", "シグナリングサーバーのURL")
	roomID := flag.String("room", "room1", "接続用ルームID")
	debug := flag.Bool("debug", false, "Output detailed logs.")
	flag.Parse()

	myID := fmt.Sprintf("peer-%d", time.Now().UnixNano())

	// 2. シグナリングサーバー (WebSocket) に接続
	connectURL := fmt.Sprintf("%s?room=%s", *serverURL, *roomID)
	log.Printf("[Signaling] サーバーに接続中: %s ...", connectURL)
	wsConn, _, err := websocket.DefaultDialer.Dial(connectURL, nil)
	if err != nil {
		log.Fatalf("[Signaling] 接続失敗: %v", err)
	}
	defer wsConn.Close()
	log.Printf("[Signaling] サーバー接続完了！(My ID: %s)", myID)

	// 3. WebRTC Configuration の構築 (STUN の有無を制御)
	var iceServers []webrtc.ICEServer
	if *debug {
		log.Println("[WebRTC] STUN サーバーを使用します")
	}
	iceServers = append(iceServers, webrtc.ICEServer{
		URLs: []string{"stun:stun.l.google.com:19302"},
	})
	config := webrtc.Configuration{
		ICEServers: iceServers,
	}
	peerConnection, err := webrtc.NewPeerConnection(config)
	if err != nil {
		log.Fatalf("PeerConnection 作成失敗: %v", err)
	}
	defer peerConnection.Close()

	// ICE Candidate がローカルで発見されたらシグナリングサーバー経由で送信
	peerConnection.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil {
			return
		}
		if *debug {
			if cc, err := c.ToICE(); err == nil {
				log.Printf("[debug:ICE candidate] Typ: %6s, Addr: %s:%d\n", cc.Type().String(), cc.Address(), cc.Port())
			}
		}

		candidate := c.ToJSON()
		sendSignal(wsConn, SignalMessage{
			Type:      "candidate",
			Room:      *roomID,
			SenderID:  myID,
			Candidate: &candidate,
		})
	})

	peerConnection.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
		if state == webrtc.ICEConnectionStateConnected {
			pair, err := peerConnection.SCTP().Transport().ICETransport().GetSelectedCandidatePair()
			if err == nil && pair != nil && *debug {
				log.Printf("[debug:ICE established] Local: %s:%d (%s) <==> Remote: %s:%d (%s)",
					pair.Local.Address, pair.Local.Port, pair.Local.Typ,
					pair.Remote.Address, pair.Remote.Port, pair.Remote.Typ,
				)
			}
		}
	})

	// 4. DataChannel の準備
	// メインスレッドに DataChannel を渡すためのチャネル
	dcReady := make(chan *webrtc.DataChannel, 1)

	setupDataChannel := func(d *webrtc.DataChannel) {
		d.OnOpen(func() {
			fmt.Printf("\n==================================================\n")
			fmt.Printf("[P2P 接続完了!] データチャネル '%s' が開通しました。\n", d.Label())
			fmt.Printf("メッセージを入力して Enter を押すと P2P 直送されます。\n")
			fmt.Printf("==================================================\n> ")
			// メインスレッドに通知
			dcReady <- d
		})

		d.OnMessage(func(msg webrtc.DataChannelMessage) {
			fmt.Printf("\r\033[K[Peer]: %s\n> ", string(msg.Data))
		})
	}

	// Guest 側: Host から DataChannel が届いた時のハンドラ
	peerConnection.OnDataChannel(func(d *webrtc.DataChannel) {
		setupDataChannel(d)
	})

	// 5. ロール自動決定とシグナリング処理
	var roleOnce sync.Once
	var role string

	// 定期的に hello を送って相手を探す
	helloTicker := time.NewTicker(2 * time.Second)
	go func() {
		for range helloTicker.C {
			sendSignal(wsConn, SignalMessage{Type: "hello", Room: *roomID, SenderID: myID})
		}
	}()

	// 受信ループ
	go func() {
		for {
			var msg SignalMessage
			err := wsConn.ReadJSON(&msg)
			if err != nil {
				log.Printf("[Signaling] 切断されました: %v", err)
				return
			}

			// 自分自身のメッセージや、別ルームのメッセージは無視
			if msg.SenderID == myID || msg.Room != *roomID {
				continue
			}

			switch msg.Type {
			case "hello":
				roleOnce.Do(func() {
					helloTicker.Stop() // 相手が見つかったので hello の送信を止める

					// ID の大小でロールを決定（タイブレーク）
					if myID < msg.SenderID {
						role = "host"
						log.Printf("[Role] 私が Host (Offer側) になりました (Peer: %s)", msg.SenderID)

						// Host は DataChannel を作成して Offer を送る
						dataChannel, err := peerConnection.CreateDataChannel("ppchat", nil)
						if err != nil {
							log.Printf("DataChannel 作成失敗: %v", err)
							return
						}
						setupDataChannel(dataChannel)

						offer, err := peerConnection.CreateOffer(nil)
						if err != nil {
							log.Printf("Offer 作成失敗: %v", err)
							return
						}
						if err := peerConnection.SetLocalDescription(offer); err != nil {
							log.Printf("SetLocalDescription 失敗: %v", err)
							return
						}

						sendSignal(wsConn, SignalMessage{
							Type:     "offer",
							Room:     *roomID,
							SenderID: myID,
							SDP:      &offer,
						})

					} else {
						role = "guest"
						log.Printf("[Role] 私が Guest (Answer側) になりました (Peer: %s)", msg.SenderID)
					}
				})

			case "offer":
				if role == "guest" && msg.SDP != nil {
					log.Println("[Guest] Offer を受信しました。Answer を返します...")
					if err := peerConnection.SetRemoteDescription(*msg.SDP); err != nil {
						log.Printf("SetRemoteDescription 失敗: %v", err)
						continue
					}

					answer, err := peerConnection.CreateAnswer(nil)
					if err != nil {
						log.Printf("CreateAnswer 失敗: %v", err)
						continue
					}
					if err := peerConnection.SetLocalDescription(answer); err != nil {
						log.Printf("SetLocalDescription 失敗: %v", err)
						continue
					}

					sendSignal(wsConn, SignalMessage{
						Type:     "answer",
						Room:     *roomID,
						SenderID: myID,
						SDP:      &answer,
					})
				}

			case "answer":
				if role == "host" && msg.SDP != nil {
					log.Println("[Host] Answer を受信しました。接続を確立します...")
					if err := peerConnection.SetRemoteDescription(*msg.SDP); err != nil {
						log.Printf("SetRemoteDescription 失敗: %v", err)
					}
				}

			case "candidate":
				if msg.Candidate != nil {
					if err := peerConnection.AddICECandidate(*msg.Candidate); err != nil {
						log.Printf("AddICECandidate 失敗: %v", err)
					}
				}
			}
		}
	}()

	// 6. メインスレッドの制御
	log.Printf("[Waiting] 部屋 '%s' で相手を待っています...", *roomID)

	// Ctrl+C ハンドリングをバックグラウンド化
	go func() {
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
		<-sigChan
		fmt.Println("\n終了します。")
		os.Exit(0)
	}()

	// DataChannel が開通するまでここで待機
	dc := <-dcReady

	// 開通後はメインスレッドで標準入力を監視（Pion のイベントループをブロックしないため）
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		text := strings.TrimSpace(scanner.Text())
		if text != "" {
			if err := dc.SendText(text); err != nil {
				log.Printf("P2P 送信エラー: %v", err)
			}
		}
		fmt.Print("> ")
	}
}

// シグナリングサーバーへ JSON メッセージを送信
func sendSignal(ws *websocket.Conn, msg SignalMessage) {
	if err := ws.WriteJSON(msg); err != nil {
		log.Printf("[Signaling] 送信エラー: %v", err)
	}
}
