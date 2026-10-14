# 本文件提供成对构建、类型检查和真实 HTTP 行为验收入口；不部署或清理数据。
.PHONY: build check test test-one
build:
	bash scripts/build.sh
check:
	go vet ./...
	go test ./... -run '^$$'
test: build
	go test ./... -count=1
test-one:
	go test ./tests/system -run '$(TEST)' -count=1 -v
