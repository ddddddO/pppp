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
	dataChannel js.Value // RTCDataChannel (P2P通信用)

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
)

type Message struct {
	Y          float64 `json:"y"`
	BX         float64 `json:"bx"`
	BY         float64 `json:"by"`
	LeftScore  int     `json:"leftScore"`
	RightScore int     `json:"rightScore"`
}

func main() {
	c := make(chan struct{})

	doc = js.Global().Get("document")
	canvas = doc.Call("getElementById", "gameCanvas")
	ctx = canvas.Call("getContext", "2d")

	setupInputHandlers()

	// WebSocketシグナリングサーバー接続
	wsURL := "wss://pppp-9hxy.onrender.com/ws"
	setupSignaling(wsURL)

	// メインループ
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
// WebRTC & シグナリング処理
// --------------------------------------------------
func setupSignaling(url string) {
	ws = js.Global().Get("WebSocket").New(url)

	ws.Set("onmessage", js.FuncOf(func(this js.Value, args []js.Value) any {
		dataStr := args[0].Get("data").String()

		var msg map[string]any
		json.Unmarshal([]byte(dataStr), &msg)

		msgType, _ := msg["type"].(string)

		switch msgType {
		case "paired":
			role, _ = msg["role"].(string)
			fmt.Println("Role assigned:", role)
			initWebRTC()

		case "offer":
			if role == "joiner" {
				offer := js.Global().Get("JSON").Call("parse", dataStr).Get("sdp")
				handleOffer(offer)
			}

		case "answer":
			if role == "host" {
				answer := js.Global().Get("JSON").Call("parse", dataStr).Get("sdp")
				pc.Call("setRemoteDescription", answer)
			}

		case "candidate":
			candidate := js.Global().Get("JSON").Call("parse", dataStr).Get("candidate")
			pc.Call("addIceCandidate", candidate)
		}
		return nil
	}))
}

func initWebRTC() {
	// STUN サーバー設定
	config := map[string]any{
		"iceServers": []any{
			map[string]any{"urls": "stun:stun.l.google.com:19302"},
		},
	}
	pc = js.Global().Get("RTCPeerConnection").New(js.ValueOf(config))

	// ICE Candidate 発生時の処理
	pc.Set("onicecandidate", js.FuncOf(func(this js.Value, args []js.Value) any {
		evt := args[0]
		candidate := evt.Get("candidate")
		if !candidate.IsNull() && !candidate.IsUndefined() {
			sendSignal(map[string]any{
				"type":      "candidate",
				"candidate": candidate,
			})
		}
		return nil
	}))

	if role == "host" {
		// HostがDataChannelを生成
		dc := pc.Call("createDataChannel", "gameData")
		setupDataChannel(dc)

		// SDP Offer の生成
		promise := pc.Call("createOffer")
		promise.Call("then", js.FuncOf(func(this js.Value, args []js.Value) any {
			offer := args[0]
			pc.Call("setLocalDescription", offer)
			sendSignal(map[string]any{
				"type": "offer",
				"sdp":  offer,
			})
			return nil
		}))
	} else {
		// JoinerはDataChannelの受信を待つ
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
		sendSignal(map[string]any{
			"type": "answer",
			"sdp":  answer,
		})
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
		json.Unmarshal([]byte(dataStr), &msg)

		peerPaddleY = msg.Y
		leftScore = msg.LeftScore
		rightScore = msg.RightScore

		if role == "joiner" {
			ballX = msg.BX
			ballY = msg.BY
		}
		return nil
	}))
}

func sendSignal(data map[string]any) {
	b, _ := json.Marshal(data)
	ws.Call("send", string(b))
}

// P2P (DataChannel) 経由でゲーム状態を直接送信
func sendStateP2P() {
	if !p2pReady || dataChannel.Get("readyState").String() != "open" {
		return
	}

	msg := Message{
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

	// 接続待機中の表示
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

	leftY, rightY := myPaddleY, peerPaddleY
	if role == "joiner" {
		leftY, rightY = peerPaddleY, myPaddleY
	}

	ctx.Call("fillRect", 0, leftY, paddleWidth, paddleHeight)
	ctx.Call("fillRect", canvasWidth-paddleWidth, rightY, paddleWidth, paddleHeight)
	ctx.Call("fillRect", ballX, ballY, ballSize, ballSize)
}
