wasm:
	cd cmd/ping-pong && GOOS=js GOARCH=wasm go build -o main.wasm main.go && mv main.wasm ../../docs/ && cd -

serve:
	cd docs && python3 -m http.server 8080

build:
	go build -o server cmd/signaling-server/*.go

cpwasmexec:
	cp /usr/local/go/lib/wasm/wasm_exec.js docs/
