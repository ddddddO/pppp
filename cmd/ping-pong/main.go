package main

import (
	"encoding/json"
	"fmt"
	"syscall/js"
)

// 設定パラメータ
const (
	canvasWidth  = 800.0
	canvasHeight = 400.0
	paddleWidth  = 10.0
	paddleHeight = 80.0
	ballSize     = 10.0
)

// 状態変数
var (
	doc         js.Value
	canvas      js.Value
	ctx         js.Value
	ws          js.Value
	role        string // "host" または "joiner"
	myPaddleY   = (canvasHeight - paddleHeight) / 2
	peerPaddleY = (canvasHeight - paddleHeight) / 2

	// ボール状態（Host側のみ計算）
	ballX  = canvasWidth / 2
	ballY  = canvasHeight / 2
	ballVX = 4.0
	ballVY = 4.0

	// キー入力状態
	upPressed   bool
	downPressed bool
)

// 通信メッセージ構造体
type Message struct {
	Type string  `json:"type"`
	Role string  `json:"role,omitempty"`
	Y    float64 `json:"y,omitempty"`
	BX   float64 `json:"bx,omitempty"`
	BY   float64 `json:"by,omitempty"`
}

func main() {
	c := make(chan struct{})

	doc = js.Global().Get("document")
	canvas = doc.Call("getElementById", "gameCanvas")
	ctx = canvas.Call("getContext", "2d")

	// 1. キーボード・タッチイベントの登録
	setupInputHandlers()

	// 2. WebSocket接続
	wsURL := "wss://pppp-9hxy.onrender.com/ws"
	setupWebSocket(wsURL)

	// 3. ゲームループ開始
	var renderFrame js.Func
	renderFrame = js.FuncOf(func(this js.Value, args []js.Value) any {
		update()
		draw()
		js.Global().Call("requestAnimationFrame", renderFrame)
		return nil
	})
	js.Global().Call("requestAnimationFrame", renderFrame)

	fmt.Println("Go/WASM Game Engine Started")
	<-c
}

// --------------------------------------------------
// 入力処理（キーボード ＆ タッチ操作）
// --------------------------------------------------
func setupInputHandlers() {
	// キーボード（PC）
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

	// タッチ操作関数（TouchStart / TouchMove 共通）
	handleTouch := js.FuncOf(func(this js.Value, args []js.Value) any {
		e := args[0]
		e.Call("preventDefault") // スマホの画面スクロールを防止

		touches := e.Get("touches")
		if touches.Get("length").Int() > 0 {
			touch := touches.Index(0)
			rect := canvas.Call("getBoundingClientRect")

			// Canvas内でのY座標を計算
			clientY := touch.Get("clientY").Float()
			rectTop := rect.Get("top").Float()
			rectHeight := rect.Get("height").Float()

			// 表示サイズと内部解像度（400px）のスケール比率を補正
			scaleY := canvasHeight / rectHeight
			canvasTouchY := (clientY - rectTop) * scaleY

			// パドルの中心に指が来るよう設定
			myPaddleY = canvasTouchY - (paddleHeight / 2)
			clampPaddle()
		}
		return nil
	})

	// スマホ（Touch）イベント登録
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

// --------------------------------------------------
// WebSocket通信処理
// --------------------------------------------------
func setupWebSocket(url string) {
	ws = js.Global().Get("WebSocket").New(url)

	ws.Set("onmessage", js.FuncOf(func(this js.Value, args []js.Value) any {
		data := args[0].Get("data").String()
		var msg Message
		if err := json.Unmarshal([]byte(data), &msg); err != nil {
			return nil
		}

		switch msg.Type {
		case "paired":
			role = msg.Role
			fmt.Println("Paired as role:", role)
		case "state":
			// 相手のパドル位置を受信
			peerPaddleY = msg.Y
			// Joinerの場合はHostから送られたボール位置も同期
			if role == "joiner" {
				ballX = msg.BX
				ballY = msg.BY
			}
		}
		return nil
	}))
}

func sendState() {
	if ws.Get("readyState").Int() != 1 { // 1 = OPEN
		return
	}

	msg := Message{
		Type: "state",
		Y:    myPaddleY,
		BX:   ballX,
		BY:   ballY,
	}
	b, _ := json.Marshal(msg)
	ws.Call("send", string(b))
}

// --------------------------------------------------
// ゲームロジック ＆ 描画処理
// --------------------------------------------------
func update() {
	// キーボード移動
	if upPressed {
		myPaddleY -= 6.0
	}
	if downPressed {
		myPaddleY += 6.0
	}
	clampPaddle()

	// Hostがボールの物理演算と判定を担当
	if role == "host" {
		ballX += ballVX
		ballY += ballVY

		// 上下壁バウンド
		if ballY <= 0 || ballY >= canvasHeight-ballSize {
			ballVY *= -1
		}

		// パドル当たり判定（左: Host, 右: Joiner）
		if ballX <= paddleWidth && ballY >= myPaddleY && ballY <= myPaddleY+paddleHeight {
			ballVX *= -1
		}
		if ballX >= canvasWidth-paddleWidth-ballSize && ballY >= peerPaddleY && ballY <= peerPaddleY+paddleHeight {
			ballVX *= -1
		}

		// 得点リセット
		if ballX < 0 || ballX > canvasWidth {
			ballX = canvasWidth / 2
			ballY = canvasHeight / 2
		}
	}

	// 位置情報を相手へ送信
	sendState()
}

func draw() {
	// 背景クリア
	ctx.Set("fillStyle", "black")
	ctx.Call("fillRect", 0, 0, canvasWidth, canvasHeight)

	// 中央点線
	ctx.Set("strokeStyle", "white")
	ctx.Call("setLineDash", []any{5, 5})
	ctx.Call("beginPath")
	ctx.Call("moveTo", canvasWidth/2, 0)
	ctx.Call("lineTo", canvasWidth/2, canvasHeight)
	ctx.Call("stroke")
	ctx.Call("setLineDash", []any{}) // クリア

	// パドル描画（左と右の位置決定）
	leftY, rightY := myPaddleY, peerPaddleY
	if role == "joiner" {
		leftY, rightY = peerPaddleY, myPaddleY
	}

	ctx.Set("fillStyle", "white")
	ctx.Call("fillRect", 0, leftY, paddleWidth, paddleHeight)                        // 左パドル
	ctx.Call("fillRect", canvasWidth-paddleWidth, rightY, paddleWidth, paddleHeight) // 右パドル

	// ボール描画
	ctx.Call("fillRect", ballX, ballY, ballSize, ballSize)
}
