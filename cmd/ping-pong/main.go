package main

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"syscall/js"
)

const (
	canvasWidth  = 800.0
	canvasHeight = 400.0
	paddleWidth  = 10.0
	paddleHeight = 80.0
	ballSize     = 10.0
)

var (
	doc         js.Value
	canvas      js.Value
	ctx         js.Value
	ws          js.Value
	pc          js.Value // RTCPeerConnection
	dataChannel js.Value // RTCDataChannel

	role        string
	p2pReady    bool
	myPaddleY   = (canvasHeight - paddleHeight) / 2
	peerPaddleY = (canvasHeight - paddleHeight) / 2

	ballX  = canvasWidth / 2
	ballY  = canvasHeight / 2
	ballVX = 4.0
	ballVY = 4.0

	leftScore  int
	rightScore int

	upPressed   bool
	downPressed bool

	myID   string // 自分のユニークID
	peerID string // 相手のユニークID
)

// P2P通信用データ構造体（Goネイティブ構造体なので json.Marshal OK）
type Message struct {
	PlayerID   string  `json:"playerId"`
	Y          float64 `json:"y"`
	BX         float64 `json:"bx"`
	BY         float64 `json:"by"`
	LeftScore  int     `json:"leftScore"`
	RightScore int     `json:"rightScore"`
}

func main() {
	// セッションごとに異なるユニークIDを生成 (例: "Player-8F3A")
	myID = fmt.Sprintf("Player-%04X", rand.Intn(0x10000))

	c := make(chan struct{})

	doc = js.Global().Get("document")
	canvas = doc.Call("getElementById", "gameCanvas")
	ctx = canvas.Call("getContext", "2d")

	setupInputHandlers()

	// 1. シグナリング接続
	wsURL := "wss://pppp-9hxy.onrender.com/ws"
	setupSignaling(wsURL)

	// 2. ゲームループ
	var renderFrame js.Func
	renderFrame = js.FuncOf(func(this js.Value, args []js.Value) any {
		update()
		draw()
		js.Global().Call("requestAnimationFrame", renderFrame)
		return nil
	})
	js.Global().Call("requestAnimationFrame", renderFrame)

	fmt.Println("WebRTC Go/WASM Pong Started")
	<-c
}

// --------------------------------------------------
// WebRTC & シグナリング処理 (修正版)
// --------------------------------------------------

// JSオブジェクトを安全にJSON文字列化して送信するヘルパー関数
func sendJSSignal(msgType string, key string, val js.Value) {
	obj := js.Global().Get("Object").New()
	obj.Set("type", msgType)
	obj.Set(key, val)
	jsonStr := js.Global().Get("JSON").Call("stringify", obj).String()
	ws.Call("send", jsonStr)
}

func setupSignaling(url string) {
	ws = js.Global().Get("WebSocket").New(url)

	ws.Set("onmessage", js.FuncOf(func(this js.Value, args []js.Value) any {
		dataStr := args[0].Get("data").String()

		// JSのJSON.parseでパースしてJavaScriptオブジェクトを保持
		msgObj := js.Global().Get("JSON").Call("parse", dataStr)
		msgType := msgObj.Get("type").String()

		switch msgType {
		case "paired":
			role = msgObj.Get("role").String()
			fmt.Println("Role assigned:", role)
			initWebRTC()

		case "offer":
			if role == "joiner" {
				offer := msgObj.Get("sdp")
				handleOffer(offer)
			}

		case "answer":
			if role == "host" {
				answer := msgObj.Get("sdp")
				pc.Call("setRemoteDescription", answer)
			}

		case "candidate":
			cand := msgObj.Get("candidate")
			if !cand.IsNull() && !cand.IsUndefined() {
				rtcCand := js.Global().Get("RTCIceCandidate").New(cand)
				pc.Call("addIceCandidate", rtcCand)
			}
		}
		return nil
	}))
}

func initWebRTC() {
	config := map[string]any{
		"iceServers": []any{
			map[string]any{"urls": "stun:stun.l.google.com:19302"},
		},
	}
	pc = js.Global().Get("RTCPeerConnection").New(js.ValueOf(config))

	// ICE Candidate 発生時
	pc.Set("onicecandidate", js.FuncOf(func(this js.Value, args []js.Value) any {
		evt := args[0]
		candidate := evt.Get("candidate")
		if !candidate.IsNull() && !candidate.IsUndefined() {
			sendJSSignal("candidate", "candidate", candidate)
		}
		return nil
	}))

	if role == "host" {
		// HostがDataChannelを作成
		dc := pc.Call("createDataChannel", "gameData")
		setupDataChannel(dc)

		// Offer作成
		promise := pc.Call("createOffer")
		promise.Call("then", js.FuncOf(func(this js.Value, args []js.Value) any {
			offer := args[0]
			pc.Call("setLocalDescription", offer)
			sendJSSignal("offer", "sdp", offer)
			return nil
		}))
	} else {
		// JoinerはDataChannel受信を待機
		pc.Set("ondatachannel", js.FuncOf(func(this js.Value, args []js.Value) any {
			dc := args[0].Get("channel")
			setupDataChannel(dc)
			return nil
		}))
	}
}

func handleOffer(offer js.Value) {
	pc.Call("setRemoteDescription", offer)
	promise := pc.Call("createAnswer")
	promise.Call("then", js.FuncOf(func(this js.Value, args []js.Value) any {
		answer := args[0]
		pc.Call("setLocalDescription", answer)
		sendJSSignal("answer", "sdp", answer)
		return nil
	}))
}

func setupDataChannel(dc js.Value) {
	dataChannel = dc

	dataChannel.Set("onopen", js.FuncOf(func(this js.Value, args []js.Value) any {
		fmt.Println("WebRTC DataChannel OPEN! P2P Connected!")
		p2pReady = true
		return nil
	}))

	dataChannel.Set("onmessage", js.FuncOf(func(this js.Value, args []js.Value) any {
		dataStr := args[0].Get("data").String()
		var msg Message
		if err := json.Unmarshal([]byte(dataStr), &msg); err != nil {
			return nil
		}

		peerPaddleY = msg.Y
		peerID = msg.PlayerID

		if role == "joiner" {
			leftScore = msg.LeftScore
			rightScore = msg.RightScore
			ballX = msg.BX
			ballY = msg.BY
		}
		return nil
	}))
}

func sendStateP2P() {
	if !p2pReady || dataChannel.Get("readyState").String() != "open" {
		return
	}

	msg := Message{
		PlayerID:   myID,
		Y:          myPaddleY,
		BX:         ballX,
		BY:         ballY,
		LeftScore:  leftScore,
		RightScore: rightScore,
	}
	b, _ := json.Marshal(msg)
	dataChannel.Call("send", string(b))
}

// --------------------------------------------------
// 入力・ロジック・描画処理
// --------------------------------------------------
func setupInputHandlers() {
	doc.Call("addEventListener", "keydown", js.FuncOf(func(this js.Value, args []js.Value) any {
		key := args[0].Get("key").String()
		if key == "ArrowUp" || key == "w" {
			upPressed = true
		}
		if key == "ArrowDown" || key == "s" {
			downPressed = true
		}
		return nil
	}))

	doc.Call("addEventListener", "keyup", js.FuncOf(func(this js.Value, args []js.Value) any {
		key := args[0].Get("key").String()
		if key == "ArrowUp" || key == "w" {
			upPressed = false
		}
		if key == "ArrowDown" || key == "s" {
			downPressed = false
		}
		return nil
	}))

	handleTouch := js.FuncOf(func(this js.Value, args []js.Value) any {
		e := args[0]
		e.Call("preventDefault")
		touches := e.Get("touches")
		if touches.Get("length").Int() > 0 {
			touch := touches.Index(0)
			rect := canvas.Call("getBoundingClientRect")
			clientY := touch.Get("clientY").Float()
			rectTop := rect.Get("top").Float()
			rectHeight := rect.Get("height").Float()
			scaleY := canvasHeight / rectHeight
			myPaddleY = (clientY-rectTop)*scaleY - (paddleHeight / 2)
			clampPaddle()
		}
		return nil
	})

	canvas.Call("addEventListener", "touchstart", handleTouch, map[string]any{"passive": false})
	canvas.Call("addEventListener", "touchmove", handleTouch, map[string]any{"passive": false})
}

func clampPaddle() {
	if myPaddleY < 0 {
		myPaddleY = 0
	}
	if myPaddleY > canvasHeight-paddleHeight {
		myPaddleY = canvasHeight - paddleHeight
	}
}

func update() {
	if upPressed {
		myPaddleY -= 6.0
	}
	if downPressed {
		myPaddleY += 6.0
	}
	clampPaddle()

	if role == "host" {
		ballX += ballVX
		ballY += ballVY

		if ballY <= 0 || ballY >= canvasHeight-ballSize {
			ballVY *= -1
		}

		if ballX <= paddleWidth && ballY+ballSize >= myPaddleY && ballY <= myPaddleY+paddleHeight {
			ballVX *= -1
			ballX = paddleWidth
		}

		paddleMarginY := 20.0
		if ballX >= canvasWidth-paddleWidth-ballSize &&
			ballY+ballSize >= (peerPaddleY-paddleMarginY) &&
			ballY <= (peerPaddleY+paddleHeight+paddleMarginY) {
			ballVX *= -1
			ballX = canvasWidth - paddleWidth - ballSize
		}

		if ballX < 0 {
			rightScore++
			resetBall(1)
		} else if ballX > canvasWidth {
			leftScore++
			resetBall(-1)
		}
	}

	sendStateP2P()
}

func resetBall(dirX float64) {
	ballX = canvasWidth / 2
	ballY = canvasHeight / 2
	ballVX = 4.0 * dirX
	if rand.Intn(2) == 0 {
		ballVY = 4.0
	} else {
		ballVY = -4.0
	}
}

func draw() {
	ctx.Set("fillStyle", "black")
	ctx.Call("fillRect", 0, 0, canvasWidth, canvasHeight)

	ctx.Set("strokeStyle", "white")
	ctx.Call("setLineDash", []any{5, 5})
	ctx.Call("beginPath")
	ctx.Call("moveTo", canvasWidth/2, 0)
	ctx.Call("lineTo", canvasWidth/2, canvasHeight)
	ctx.Call("stroke")
	ctx.Call("setLineDash", []any{})

	if !p2pReady {
		ctx.Set("fillStyle", "yellow")
		ctx.Set("font", "20px sans-serif")
		ctx.Set("textAlign", "center")
		ctx.Call("fillText", "Connecting P2P (WebRTC)...", canvasWidth/2, canvasHeight/2)
		return
	}

	ctx.Set("fillStyle", "white")
	ctx.Set("font", "48px sans-serif")
	ctx.Set("textAlign", "center")
	ctx.Call("fillText", fmt.Sprintf("%d", leftScore), canvasWidth/4, 60)
	ctx.Call("fillText", fmt.Sprintf("%d", rightScore), (canvasWidth*3)/4, 60)

	ctx.Set("font", "16px sans-serif")
	leftID, rightID := myID, peerID
	if role == "joiner" {
		leftID, rightID = peerID, myID
	}
	ctx.Call("fillText", leftID, canvasWidth/4, 90)
	ctx.Call("fillText", rightID, (canvasWidth*3)/4, 90)

	leftY, rightY := myPaddleY, peerPaddleY
	if role == "joiner" {
		leftY, rightY = peerPaddleY, myPaddleY
	}
	ctx.Call("fillRect", 0, leftY, paddleWidth, paddleHeight)
	ctx.Call("fillRect", canvasWidth-paddleWidth, rightY, paddleWidth, paddleHeight)
	ctx.Call("fillRect", ballX, ballY, ballSize, ballSize)
}
