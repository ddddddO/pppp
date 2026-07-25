package main

import (
	"bufio"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pion/webrtc/v4"
)

// シグナリング用メッセージ構造体
type SignalMessage struct {
	Type      string                     `json:"type"`
	Room      string                     `json:"room"`
	SenderID  string                     `json:"senderId"` // 自分のID
	SDP       *webrtc.SessionDescription `json:"sdp,omitempty"`
	Candidate *webrtc.ICECandidateInit   `json:"candidate,omitempty"`
}

func main() {
	roomID := flag.String("room", "room1", "接続用ルームID")
	role := flag.String("role", "host", "役割: 'host' (Offer作成側) または 'guest' (Answer作成側)")
	debug := flag.Bool("debug", false, "Output detailed logs.")
	flag.Parse()

	myID := fmt.Sprintf("peer-%d", time.Now().UnixNano())
	serverURL := "wss://pppp-9hxy.onrender.com/ws"

	// 1. シグナリングサーバー (WebSocket) に接続
	log.Printf("[Signaling] サーバーに接続中: %s ...", serverURL)
	wsConn, _, err := websocket.DefaultDialer.Dial(serverURL, nil)
	if err != nil {
		log.Fatalf("[Signaling] 接続失敗: %v", err)
	}
	defer wsConn.Close()
	log.Println("[Signaling] サーバー接続完了！")

	// 2. WebRTC PeerConnection の作成
	config := webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{
			{URLs: []string{"stun:stun.l.google.com:19302"}},
		},
	}
	peerConnection, err := webrtc.NewPeerConnection(config)
	if err != nil {
		log.Fatalf("PeerConnection 作成失敗: %v", err)
	}
	defer peerConnection.Close()

	// 3. ICE Candidate がローカルで発見されたら、シグナリングサーバー経由で送信
	peerConnection.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil {
			return
		}

		if *debug {
			if cc, err := c.ToICE(); err == nil {
				log.Printf(
					"[debug:Candidate] Typ: %6s, Protocol: %s, Addr: %s, Port:%d\n",
					cc.Type().String(),
					cc.NetworkType().String(),
					cc.Address(),
					cc.Port(),
				)
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

	// 4. Guest 側: Host から届いた DataChannel をセットアップする準備
	peerConnection.OnDataChannel(func(d *webrtc.DataChannel) {
		setupDataChannel(d)
	})

	// 5. WebSocket からのメッセージ受信ループをバックグラウンドで開始
	go handleSignalingIncoming(wsConn, peerConnection, *roomID, *role, myID)

	// 6. 接続シーケンスの開始
	if *role == "guest" {
		// Offer を受領するまで 2 秒おきに join を再送するゴルーチン
		go func() {
			ticker := time.NewTicker(2 * time.Second)
			defer ticker.Stop()

			// 1 回目の join を即座に送信
			sendSignal(wsConn, SignalMessage{
				Type:     "join",
				Room:     *roomID,
				SenderID: myID,
			})
			log.Println("[Guest] 入室通知 (join) を送信しました。Host からの Offer を待っています...")

			for range ticker.C {
				// Host から Offer を受信して SetRemoteDescription が完了したら再送を停止
				if peerConnection.RemoteDescription() != nil {
					log.Println("[Guest] Host とのハンドシェイクが始まったため join 再送を停止します。")
					return
				}

				log.Println("[Guest] 応答がないため、入室通知 (join) を再送中...")
				sendSignal(wsConn, SignalMessage{
					Type:     "join",
					Room:     *roomID,
					SenderID: myID,
				})
			}
		}()
	} else {
		// Host 側: 何もせず Guest が「join」してくるのを待機する
		log.Println("[Host] Guest の入室を待っています... (Guest 側を起動してください)")
	}

	// Ctrl+C が押されるまでメインスレッドを待機
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan
	fmt.Println("\n終了します。")
}

// シグナリングサーバーへ JSON メッセージを送信
func sendSignal(ws *websocket.Conn, msg SignalMessage) {
	if err := ws.WriteJSON(msg); err != nil {
		log.Printf("[Signaling] 送信エラー: %v", err)
	}
}

// シグナリングサーバーからのメッセージ受信・分岐処理
func handleSignalingIncoming(ws *websocket.Conn, pc *webrtc.PeerConnection, roomID, role, myID string) {
	for {
		var msg SignalMessage
		err := ws.ReadJSON(&msg)
		if err != nil {
			log.Printf("[Signaling] 切断されました: %v", err)
			return
		}

		// 「自分が送ったメッセージ」および「別ルームのメッセージ」は無視する
		if msg.SenderID == myID || msg.Room != roomID {
			continue
		}

		switch msg.Type {
		case "join":
			// 【Host 側のみ】Guest の入室（join）を確認してから DataChannel と Offer を作成する
			if role == "host" {
				log.Println("[Host] Guest が入室しました。Offer を作成します...")

				// DataChannel の作成
				dataChannel, err := pc.CreateDataChannel("ppchat", nil)
				if err != nil {
					log.Printf("DataChannel 作成失敗: %v", err)
					continue
				}
				setupDataChannel(dataChannel)

				// Offer SDP の作成と送信
				offer, err := pc.CreateOffer(nil)
				if err != nil {
					log.Printf("Offer 作成失敗: %v", err)
					continue
				}
				if err := pc.SetLocalDescription(offer); err != nil {
					log.Printf("SetLocalDescription (Offer) 失敗: %v", err)
					continue
				}

				sendSignal(ws, SignalMessage{
					Type:     "offer",
					Room:     roomID,
					SenderID: myID,
					SDP:      &offer,
				})
			}

		case "offer":
			// 【Guest 側のみ】Host から受け取った Offer に対する Answer を作成して返す
			if role == "guest" && msg.SDP != nil {
				log.Println("[Guest] Offer を受信しました。Answer を作成して返します...")
				if err := pc.SetRemoteDescription(*msg.SDP); err != nil {
					log.Printf("SetRemoteDescription (Offer) 失敗: %v", err)
					continue
				}

				answer, err := pc.CreateAnswer(nil)
				if err != nil {
					log.Printf("CreateAnswer 失敗: %v", err)
					continue
				}
				if err := pc.SetLocalDescription(answer); err != nil {
					log.Printf("SetLocalDescription (Answer) 失敗: %v", err)
					continue
				}

				sendSignal(ws, SignalMessage{
					Type:     "answer",
					Room:     roomID,
					SenderID: myID,
					SDP:      &answer,
				})
			}

		case "answer":
			// 【Host 側のみ】Guest から届いた Answer を登録してハンドシェイク完了
			if role == "host" && msg.SDP != nil {
				log.Println("[Host] Answer を受信しました。接続を確立します...")
				if err := pc.SetRemoteDescription(*msg.SDP); err != nil {
					log.Printf("SetRemoteDescription (Answer) 失敗: %v", err)
				}
			}

		case "candidate":
			// お互いに届いた ICE Candidate を登録する
			if msg.Candidate != nil {
				if err := pc.AddICECandidate(*msg.Candidate); err != nil {
					log.Printf("AddICECandidate 失敗: %v", err)
				}
			}
		}
	}
}

// DataChannel 開通後のメッセージ送受信処理
func setupDataChannel(d *webrtc.DataChannel) {
	d.OnOpen(func() {
		fmt.Printf("\n==================================================\n")
		fmt.Printf("[P2P 接続完了!] データチャネル '%s' が開通しました。\n", d.Label())
		fmt.Printf("メッセージを入力して Enter を押すと P2P 直送されます。\n")
		fmt.Printf("==================================================\n> ")

		// 標準入力を監視して P2P 送信
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			text := strings.TrimSpace(scanner.Text())
			if text != "" {
				if err := d.SendText(text); err != nil {
					log.Printf("P2P 送信エラー: %v", err)
				}
				fmt.Print("> ")
			}
		}
	})

	d.OnMessage(func(msg webrtc.DataChannelMessage) {
		fmt.Printf("\r[相手からの P2P 受信]: %s\n> ", string(msg.Data))
	})
}
