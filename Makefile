.PHONY: proto generate build test lint clean

proto:
	protoc --go_out=. --go_opt=module=github.com/sixfathoms/lplex \
		--go-grpc_out=. --go-grpc_opt=module=github.com/sixfathoms/lplex \
		proto/replication/v1/replication.proto

generate:
	go generate ./pgn/...

build: generate
	go build -o lplex ./cmd/lplex
	go build -o lplex-cloud ./cmd/lplex-cloud
	go build -o lplexdump ./cmd/lplexdump

test: generate
	go test ./... -v -count=1

lint: generate
	golangci-lint run

clean:
	rm -f lplex lplex-cloud lplexdump
	rm -f pgn/pgn_gen.go pgn/helpers_gen.go pgn/schema.json
	rm -rf pgn/proto/
