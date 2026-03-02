.PHONY: proto build test lint

proto:
	protoc --go_out=. --go_opt=module=github.com/sixfathoms/lplex \
		--go-grpc_out=. --go-grpc_opt=module=github.com/sixfathoms/lplex \
		proto/replication/v1/replication.proto

build:
	go build ./...

test:
	go test ./... -v -count=1

lint:
	golangci-lint run
