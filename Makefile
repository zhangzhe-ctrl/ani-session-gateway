GOBIN := $(CURDIR)/.bin
PATH := $(GOBIN):$(PATH)

.PHONY: tools generate lint contract check-generated manifests test race vet build diff-check check

tools:
	GOBIN=$(GOBIN) go install github.com/bufbuild/buf/cmd/buf@v1.60.0
	GOBIN=$(GOBIN) go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.12
	GOBIN=$(GOBIN) go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.6.0

generate:
	buf generate

lint:
	buf lint

contract:
	cd api && go test ./openapi/...

check-generated:
	./scripts/check-generated.sh

manifests:
	./scripts/check-manifests.sh

test:
	go test ./...
	cd api && go test ./...

race:
	go test -race ./...

vet:
	go vet ./...
	cd api && go vet ./...

build:
	go build ./cmd/session-gateway

diff-check:
	git diff --check

check: lint contract check-generated manifests test race vet build diff-check
