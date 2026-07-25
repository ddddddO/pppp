package main

import (
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

var upgraderPPChat = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

type ppchatClient struct {
	conn *websocket.Conn
	send chan []byte
}

type Room struct {
	mu      sync.Mutex
	clients map[*ppchatClient]bool
}

type ppchatSignalingServer struct {
	mu    sync.RWMutex
	rooms map[string]*Room
}

func NewPPChatSignalingServer() *ppchatSignalingServer {
	return &ppchatSignalingServer{
		rooms: make(map[string]*Room),
	}
}

func (s *ppchatSignalingServer) getOrCreateRoom(roomID string) *Room {
	s.mu.Lock()
	defer s.mu.Unlock()

	room, exists := s.rooms[roomID]
	if !exists {
		room = &Room{
			clients: make(map[*ppchatClient]bool),
		}
		s.rooms[roomID] = room
	}
	return room
}

func (s *ppchatSignalingServer) removeClient(roomID string, client *ppchatClient) {
	s.mu.Lock()
	defer s.mu.Unlock()

	room, exists := s.rooms[roomID]
	if !exists {
		return
	}

	room.mu.Lock()
	delete(room.clients, client)
	close(client.send)
	isEmpty := len(room.clients) == 0
	room.mu.Unlock()

	// 部屋が空になったらメモリから削除
	if isEmpty {
		delete(s.rooms, roomID)
		log.Printf("[Room %s] 空になったため削除しました", roomID)
	}
}

func (s *ppchatSignalingServer) handleWSPPChat(w http.ResponseWriter, r *http.Request) {
	roomID := r.URL.Query().Get("room")
	if roomID == "" {
		http.Error(w, "room query parameter is required", http.StatusBadRequest)
		return
	}

	conn, err := upgraderPPChat.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("Upgrade error: %v", err)
		return
	}

	client := &ppchatClient{
		conn: conn,
		send: make(chan []byte, 256),
	}

	room := s.getOrCreateRoom(roomID)
	room.mu.Lock()
	room.clients[client] = true
	room.mu.Unlock()

	log.Printf("[Room %s] クライアントが接続しました", roomID)

	// 書き込み用 goroutine (Ping/Pong 含む)
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer func() {
			ticker.Stop()
			conn.Close()
		}()

		for {
			select {
			case message, ok := <-client.send:
				conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
				if !ok {
					conn.WriteMessage(websocket.CloseMessage, []byte{})
					return
				}
				if err := conn.WriteMessage(websocket.TextMessage, message); err != nil {
					return
				}
			case <-ticker.C:
				conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
				if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
					return
				}
			}
		}
	}()

	// 読み込み用 goroutine (受信パケットの転送)
	defer func() {
		s.removeClient(roomID, client)
		conn.Close()
	}()

	conn.SetReadLimit(65536)
	conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			break
		}

		// ルーム内の自分以外のクライアントへリレー
		room.mu.Lock()
		for c := range room.clients {
			if c != client {
				select {
				case c.send <- msg:
				default:
					close(c.send)
					delete(room.clients, c)
				}
			}
		}
		room.mu.Unlock()
	}
}
