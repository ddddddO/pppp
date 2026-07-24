//go:build js && asm

package main

import (
	"encoding/json"
	"fmt"
	"image/color"
	"log"
	"syscall/js"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/ebitenutil"
)

const (
	wsURL = "wss://pppp-9hxy.onrender.com/ws"

	screenWidth  = 640
	screenHeight = 480
	paddleWidth  = 10
	paddleHeight = 60
	ballSize     = 10
)

// ゲームの状態管理
type Game struct {
	myPaddleY float64
	opPaddleY float64
	ballX     float64
	ballY     float64
	ballDX    float64
	ballDY    float64

	isHost    bool
	connected bool
	statusMsg string
}

// 互いに送受信するデータの構造体
type GameState struct {
	Type    string  `json:"type"`
	PaddleY float64 `json:"paddleY"`
	BallX   float64 `json:"ballX,omitempty"` // ホストのみ送信
	BallY   float64 `json:"ballY,omitempty"`
}

var (
	game = &Game{
		myPaddleY: screenHeight/2 - paddleHeight/2,
		opPaddleY: screenHeight/2 - paddleHeight/2,
		ballX:     screenWidth / 2,
		ballY:     screenHeight / 2,
		ballDX:    4,
		ballDY:    4,
		statusMsg: "Connecting to server...",
	}
	dataChannel js.Value
	window      = js.Global()
	pc          js.Value
	ws          js.Value
)

func main() {
	// 一旦ネットワーク設定をコメントアウトしてスキップします
	setupNetwork()
	// シグナリングサーバーを介さない場合、↑をコメントアウト・↓をコメントインにし、強制的にゲームを開始状態（ホスト側）にする
	// game.connected = true
	// game.isHost = true

	ebiten.SetWindowSize(screenWidth, screenHeight)
	ebiten.SetWindowTitle("P2P Ping Pong!")
	if err := ebiten.RunGame(game); err != nil {
		log.Fatal(err)
	}
}

// --- Ebitengine のループ ---

func (g *Game) Update() error {
	if !g.connected {
		return nil
	}

	// パドルの移動
	if ebiten.IsKeyPressed(ebiten.KeyArrowUp) && g.myPaddleY > 0 {
		g.myPaddleY -= 5
	}
	if ebiten.IsKeyPressed(ebiten.KeyArrowDown) && g.myPaddleY < screenHeight-paddleHeight {
		g.myPaddleY += 5
	}

	// ホスト側のみボールの物理演算を行う
	if g.isHost {
		g.ballX += g.ballDX
		g.ballY += g.ballDY

		// 上下壁の反射
		if g.ballY <= 0 || g.ballY >= screenHeight-ballSize {
			g.ballDY *= -1
		}

		// ホスト(左)パドルの反射
		if g.ballX <= paddleWidth && g.ballY+ballSize >= g.myPaddleY && g.ballY <= g.myPaddleY+paddleHeight {
			g.ballDX *= -1
			g.ballX = paddleWidth // 埋まり防止
		}
		// ゲスト(右)パドルの反射
		if g.ballX >= screenWidth-paddleWidth-ballSize && g.ballY+ballSize >= g.opPaddleY && g.ballY <= g.opPaddleY+paddleHeight {
			g.ballDX *= -1
			g.ballX = screenWidth - paddleWidth - ballSize
		}
	}

	// 状態をピアに送信 (DataChannelが開いていれば)
	if !dataChannel.IsUndefined() && !dataChannel.IsNull() && dataChannel.Get("readyState").String() == "open" {
		state := GameState{
			Type:    "state",
			PaddleY: g.myPaddleY,
		}
		if g.isHost {
			state.BallX = g.ballX
			state.BallY = g.ballY
		}
		b, _ := json.Marshal(state)
		dataChannel.Call("send", string(b))
	}

	return nil
}

func (g *Game) Draw(screen *ebiten.Image) {
	if !g.connected {
		ebitenutil.DebugPrint(screen, g.statusMsg)
		return
	}

	// 描画位置の決定（ホストは左、ゲストは右）
	myPadX, opPadX := 0.0, float64(screenWidth-paddleWidth)
	if !g.isHost {
		myPadX, opPadX = opPadX, myPadX
	}

	ebitenutil.DrawRect(screen, myPadX, g.myPaddleY, paddleWidth, paddleHeight, color.White)
	ebitenutil.DrawRect(screen, opPadX, g.opPaddleY, paddleWidth, paddleHeight, color.RGBA{255, 0, 0, 255})
	ebitenutil.DrawRect(screen, g.ballX, g.ballY, ballSize, ballSize, color.White)
}

func (g *Game) Layout(outsideWidth, outsideHeight int) (int, int) {
	return screenWidth, screenHeight
}

// --- WebRTC / WebSocket 通信処理 ---

func setupNetwork() {
	ws = window.Get("WebSocket").New(wsURL)

	configJSON := `{"iceServers":[{"urls":"stun:stun.l.google.com:19302"}]}`
	config := js.Global().Get("JSON").Call("parse", configJSON)
	pc = window.Get("RTCPeerConnection").New(config)

	// ICE候補の送信
	pc.Set("onicecandidate", js.FuncOf(func(this js.Value, args []js.Value) interface{} {
		candidate := args[0].Get("candidate")
		if !candidate.IsNull() {
			sendWS(map[string]interface{}{
				"type":      "ice",
				"candidate": js.Global().Get("JSON").Call("stringify", candidate).String(),
			})
		}
		return nil
	}))

	// DataChannel 受信時 (ゲスト側)
	pc.Set("ondatachannel", js.FuncOf(func(this js.Value, args []js.Value) interface{} {
		dataChannel = args[0].Get("channel")
		setupDataChannel(dataChannel)
		return nil
	}))

	// WebSocket メッセージ受信
	ws.Set("onmessage", js.FuncOf(func(this js.Value, args []js.Value) interface{} {
		dataStr := args[0].Get("data").String()
		var msg map[string]interface{}
		json.Unmarshal([]byte(dataStr), &msg)

		switch msg["type"] {
		case "paired":
			game.statusMsg = "Paired! Connecting P2P..."
			game.isHost = msg["role"] == "host"
			if game.isHost {
				// ホストはDataChannelを作成してOfferを出す
				dcOptsJSON := `{"ordered":false,"maxRetransmits":0}`
				dcOpts := js.Global().Get("JSON").Call("parse", dcOptsJSON)
				dataChannel = pc.Call("createDataChannel", "pong", dcOpts)
				setupDataChannel(dataChannel)

				pc.Call("createOffer").Call("then", js.FuncOf(func(this js.Value, args []js.Value) interface{} {
					offer := args[0]
					pc.Call("setLocalDescription", offer)
					sendWS(map[string]interface{}{
						"type":  "offer",
						"offer": js.Global().Get("JSON").Call("stringify", offer).String(),
					})
					return nil
				}))
			}

		case "offer":
			offerObj := window.Get("JSON").Call("parse", msg["offer"])
			pc.Call("setRemoteDescription", window.Get("RTCSessionDescription").New(offerObj)).Call("then", js.FuncOf(func(this js.Value, args []js.Value) interface{} {
				pc.Call("createAnswer").Call("then", js.FuncOf(func(this js.Value, args []js.Value) interface{} {
					answer := args[0]
					pc.Call("setLocalDescription", answer)
					sendWS(map[string]interface{}{
						"type":   "answer",
						"answer": js.Global().Get("JSON").Call("stringify", answer).String(),
					})
					return nil
				}))
				return nil
			}))

		case "answer":
			answerObj := window.Get("JSON").Call("parse", msg["answer"])
			pc.Call("setRemoteDescription", window.Get("RTCSessionDescription").New(answerObj))

		case "ice":
			iceObj := window.Get("JSON").Call("parse", msg["candidate"])
			pc.Call("addIceCandidate", window.Get("RTCIceCandidate").New(iceObj))
		}
		return nil
	}))
}

func setupDataChannel(dc js.Value) {
	dc.Set("onopen", js.FuncOf(func(this js.Value, args []js.Value) interface{} {
		game.connected = true
		fmt.Println("DataChannel Open!")
		return nil
	}))

	dc.Set("onmessage", js.FuncOf(func(this js.Value, args []js.Value) interface{} {
		dataStr := args[0].Get("data").String()
		var state GameState
		json.Unmarshal([]byte(dataStr), &state)

		if state.Type == "state" {
			game.opPaddleY = state.PaddleY
			// ゲスト側はホストからボールの位置も受け取って同期する
			if !game.isHost && state.BallX != 0 {
				// ホスト目線のX座標なので、画面を反転して描画位置を同期させないためにそのまま適用
				game.ballX = state.BallX
				game.ballY = state.BallY
			}
		}
		return nil
	}))
}

func sendWS(data map[string]interface{}) {
	b, _ := json.Marshal(data)
	ws.Call("send", string(b))
}
