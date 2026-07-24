# P2P Ping-Pong!

Play!👉 https://ddddddo.github.io/pppp/

If two people connect, they can play a game of table tennis!

![](./assets/image.png)

## Flow

```console
+-----------------------+                    +-----------------------+
|  Browser (Client A)   |                    |  Browser (Client B)   |
|  - Host (Physics)     |                    |  - Joiner (Sync)      |
+-----------+-----------+                    +-----------+-----------+
            |                                            |
            |             1. WebSocket (wss://)          |
            +-----------> [ Signaling Server ] <---------+
            |              (Render.com)                  |
            |                                            |
            |             2. STUN Query (UDP)            |
            +-----------> [   STUN Server    ] <---------+
            |              (Google STUN)                 |
            |                                            |
            |             3. WebRTC DataChannel (P2P)    |
            +============================================+
```

1. ブラウザ ↔ STUN サーバー

    各ブラウザが直接 Google の STUN サーバーに「自分の外側から見た IP アドレスとポート番号は何？」と問い合わせて自分の接続情報（ICE Candidate）を取得します。

1. ブラウザ ↔ Signaling Server ↔ ブラウザ

    取得した接続情報を、Signaling Server をバケツリレーのように経由して相手のブラウザへ渡します。

1. ブラウザ ↔ ブラウザ (P2P)

    相手の IP/ポート番号が分かったら、ブラウザ同士が直接 WebRTC で繋がります。

## Note
- https://dashboard.render.com/project