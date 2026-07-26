## P2P Ping-Pong!

Play!👉 https://ddddddo.github.io/pppp/

If two people connect, they can play a game of table tennis!

![](./assets/image.png)

> [!TIP]
> If the game doesn't start even after both players have joined, try reloading the page a few times! (This is because the instance hosting the signaling server may be down and taking a while to start up.)

### Flow

```console
                       +--------------------------------+
                       | Static Web Host (GitHub Pages) |
                       +---------------+----------------+
                                       |
                           1. Download WASM (HTTPS)
                                       |
           +---------------------------+---------------------------+
           |                                                       |
           v                                                       v
+--------------------+                                    +--------------------+
| Browser (Client A) |                                    | Browser (Client B) |
+--+-------+---------+                                    +---------+-------+--+
   |       |                                                        |       |
   |       |          2. Matchmaking & Connection (wss://)          |       |
   |       +----------------> [ Signaling Server ] <----------------+       |
   |                            (Render.com)                                |
   |                                                                        |
   |                  3. Discover Public IP/Port (UDP)                      |
   +------------------------> [   STUN Server    ] <------------------------+
   |                            (Google STUN)                               |
   |                                                                        |
   |                  4. Exchange IP/Port Info (ICE Candidate)              |
   |   +--------------------> [ Signaling Server ] -------------------+     |
   |   |                        (Render.com)                          |     |
   |   +<-------------------------------------------------------------+     |
   |                                                                        |
   |                  5. Direct P2P Game Data (WebRTC)                      |
   +========================================================================+
```

1. ゲーム本体（WASM）のダウンロード
1. マッチングとシグナリング接続の確立
1. 自分のパブリックIP・ポート番号の確認

    各ブラウザが自動的に STUN サーバーに問い合わせる

1. IP・ポート情報（ICE Candidate / SDP）の交換

    3で判明した自分のパブリックIP・ポート番号や通信設定（SDP / ICE Candidate）を、シグナリングサーバーをバケツリレーして相手のブラウザに届ける（WebSocket経由）

1. WebRTC による P2P 直接通信の開始 

    お互いのIPアドレスとポート番号が揃ったため、ブラウザ同士が直接トンネル（DataChannel）を繋ぐ。ここから先は Render.com や Google のサーバーを一切通さず、パドル位置やボール座標などのゲームデータを直接やり取りする（P2P）

## ppChat

### Installation
```console
go install github.com/ddddddO/pppp/cmd/ppchat@latest
```

- Host

```console
ppchat -room test-room1
```

- Guest

```console
ppchat -room test-room1
```

### Flow

```console
                    +--------------------------+
                    |     Signaling Server     |
                    |  (WebSocket / Room Mgmt) |
                    +------------+-------------+
                                 |
                  2. Signaling   |   2. Signaling
              (Offer/Answer/ICE) |   (Offer/Answer/ICE)
                                 v
+------------------+                    +------------------+
|   ppchat Host    |                    |   ppchat Guest   |
|                  |                    | (Auto-retry join)|
+----+--------+----+                    +----+--------+----+
     |        |                              |          |
     |        +========= 3. Direct P2P ======+          |
     |             (WebRTC DataChannel)                 |
     |            [Encrypted Chat Data]                 |
     |                                                  |
     | 1. Query Public IP            1. Query Public IP |
     v                                                  v
+----------------------------------------------------------+
|                       STUN Server                        |
|                (e.g., stun.l.google.com)                 |
+----------------------------------------------------------+
```

## Note
- シグナリングサーバーをホストしてるサービス(https://dashboard.render.com/project)
    - しばらく音沙汰ないとインスタンスが落ちるから、その状態でリクエスト受けると立ち上げで時間かかるのでレスポンスも遅くなるとのこと
- [UDPホールパンチングのメモ](https://github.com/ddddddO/work/issues/63)
- パケットキャプチャ
    ```console
    sudo tcpdump -U -i any -w - | /mnt/c/Program\ Files/Wireshark/Wireshark.exe -k -i -
    ```
    - PC-aは光回線/PC-bはモバイル回線(ドコモ・テザリング)でそれぞれppchatを起動して、Wiresharkを眺めてた。
    - すると、googleへのstunプロトコルのリクエスト後に、peer宛てのstunリクエストを何度もしていることがわかった。以下役割とのこと
        - stunサーバー(google)へのリクエスト: 自分のパブリックIPとポート番号の確認
        - peerへのリクエスト: 通信先候補のホールパンチングと疎通確認
            - [RFCはこのあたり](https://tex2e.github.io/rfc-translater/html/rfc8445.html#2-2--Connectivity-Checks)
- IPv6ではstunサーバーとシグナリングサーバーは不要になるか？
    - 各々でグローバルなIPを持てるのでstunサーバーは不要
    - しかし、相手のIPとポートを知る必要があるので、シグナリングサーバーは必要
    - また、IPv6でNATは必要ないがファイアウォールはあるため、ファイアウォールを突破するためにホールパンチは必要