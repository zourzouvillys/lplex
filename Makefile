.PHONY: proto build test lint clean

proto:
	protoc --go_out=. --go_opt=module=github.com/sixfathoms/lplex \
		--go-grpc_out=. --go-grpc_opt=module=github.com/sixfathoms/lplex \
		proto/replication/v1/replication.proto

build:
	go build -o lplex ./cmd/lplex
	go build -o lplex-cloud ./cmd/lplex-cloud
	go build -o lplexdump ./cmd/lplexdump

test:
	go test ./... -v -count=1

lint:
	golangci-lint run

clean:
	rm -f lplex lplex-cloud lplexdump
