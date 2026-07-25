package main

import (
	"log"
	"net/http"
	"os"
	"sync"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

type Client struct {
	conn *websocket.Conn
	peer *Client
	role string
}

var (
	mutex         sync.Mutex
	waitingClient *Client
)

func handleWSPingPong(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("Upgrade err:", err)
		return
	}

	client := &Client{conn: conn}

	mutex.Lock()
	if waitingClient == nil {
		// 1名目: Host
		client.role = "host"
		waitingClient = client
		mutex.Unlock()

		log.Println("Client A connected (Host), waiting for peer...")
		conn.WriteJSON(map[string]string{"type": "paired", "role": "host"})
	} else {
		// 2名目: Joiner
		client.role = "joiner"
		peer := waitingClient
		waitingClient = nil
		mutex.Unlock()

		client.peer = peer
		peer.peer = client

		log.Println("Client B connected (Joiner), pairing complete!")

		// 両者に通知
		peer.conn.WriteJSON(map[string]string{"type": "paired", "role": "host"})
		client.conn.WriteJSON(map[string]string{"type": "paired", "role": "joiner"})
	}

	// メッセージの中継（WebRTCシグナリングデータのパススルー）
	defer func() {
		conn.Close()
		mutex.Lock()
		if waitingClient == client {
			waitingClient = nil
		}
		mutex.Unlock()
	}()

	for {
		var msg map[string]any
		if err := conn.ReadJSON(&msg); err != nil {
			break
		}
		// 相手へシグナリングメッセージをそのまま転送
		if client.peer != nil {
			client.peer.conn.WriteJSON(msg)
		}
	}
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8081"
	}

	http.HandleFunc("/ws_pingpong", handleWSPingPong)
	log.Printf("Signaling server listening on :%s...", port)
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Fatal(err)
	}
}
