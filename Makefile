.PHONY: build build-linux-amd64 build-linux-arm64 test vet fmt lint clean release-check

BINARY := bugpk-api
VERSION ?= $(shell git describe --tags --always 2>/dev/null || date +%Y%m%d)

build: ## 编译当前平台
	go build -trimpath -ldflags "-s -w" -o $(BINARY) ./cmd/bugpk-api

build-linux-amd64: ## 交叉编译 linux/amd64
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o $(BINARY)-linux-amd64 ./cmd/bugpk-api

build-linux-arm64: ## 交叉编译 linux/arm64
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o $(BINARY)-linux-arm64 ./cmd/bugpk-api

test: ## 运行测试
	go test -count=1 ./...

vet: ## 静态检查
	go vet ./...

fmt: ## 格式化
	gofmt -l -w cmd internal

lint: ## 代码规范检查（需要 golangci-lint）
	golangci-lint run ./...

clean: ## 清理编译产物
	rm -f $(BINARY) $(BINARY)-*
	rm -rf release/ dist/ build/

release-check: ## 发布前自检（build+vet+test）
	go build ./... && go vet ./... && go test -count=1 ./...
