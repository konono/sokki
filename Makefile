BIN := bin/sokki

.PHONY: build run test test-e2e fmt vet clean

build:
	go build -o $(BIN) ./cmd/sokki

run: build
	./$(BIN)

test:
	go test ./...

## 実機テスト: whisper-server と実モデルを使う (音声デバイスは使わない)
test-e2e:
	MEETSCRIBE_E2E=1 go test ./... -run Integration -v

fmt:
	gofmt -w .

vet:
	go vet ./...

clean:
	rm -rf bin
