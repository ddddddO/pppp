package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"sync"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true }, // CORS許可
}

type Client struct {
	conn *websocket.Conn
	peer *Client
}

var (
	mu            sync.Mutex
	waitingClient *Client
)

func handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("Upgrade error:", err)
		return
	}

	client := &Client{conn: conn}

	mu.Lock()
	if waitingClient != nil {
		// 待機中がいればペアリング
		peer := waitingClient
		waitingClient = nil
		mu.Unlock()

		client.peer = peer
		peer.peer = client

		// ホストとゲストの割り当て
		peer.conn.WriteJSON(map[string]string{"type": "paired", "role": "host"})
		client.conn.WriteJSON(map[string]string{"type": "paired", "role": "joiner"})

		go relay(peer)
		go relay(client)
	} else {
		waitingClient = client
		mu.Unlock()
	}
}

// ピアへのメッセージ転送ルーチン
func relay(c *Client) {
	for {
		msgType, msg, err := c.conn.ReadMessage()
		if err != nil {
			if c.peer != nil && c.peer.conn != nil {
				c.peer.conn.Close()
			}
			break
		}
		if c.peer != nil && c.peer.conn != nil {
			c.peer.conn.WriteMessage(msgType, msg)
		}
	}
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8081"
	}

	http.HandleFunc("/ws", handleWS)
	fmt.Printf("Signaling server listening on :%s...\n", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}
