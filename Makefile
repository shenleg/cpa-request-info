# 本文件统一快速检查、暂存门禁、真实系统/浏览器验收；不提交、部署或删除数据。
.PHONY: build build-plugin build-retention-test check check-staged deps browser-install demo test test-one tools-test browser browser-run verify verify-linux-disk verify-linux-disk-tools-test verify-official-probe
build:
	bash scripts/build.sh
build-plugin:
	bash scripts/build-plugin.sh
build-retention-test:
	bash scripts/build-retention-test.sh
check:
	bash scripts/check-fast.sh
check-staged:
	bash scripts/check-staged.sh
deps:
	go mod download
	npm ci --ignore-scripts
browser-install:
	npm run browser:install
demo:
	bash scripts/fetch-management-ui.sh
	go build -o .build/bin/audit-demo ./cmd/audit-demo
test: build build-retention-test
	go test ./... -count=1
test-one:
	go test ./tests/system -run '$(TEST)' -count=1 -v
tools-test: demo verify-linux-disk-tools-test
	go test ./tests/tooling -count=1
	npm test
browser: build demo
	npm run browser
browser-run:
	npm run browser
# Linux磁盘故障须调用方显式以root启动；不属于check/verify，不自动sudo或构建。
verify-linux-disk:
	bash scripts/verify-linux-disk.sh --source "$(SOURCE)" --output "$(OUTPUT)" --user "$(RUN_USER)"
verify-linux-disk-tools-test:
	node --test scripts/verify-linux-disk.test.cjs
verify:
	$(MAKE) check
	$(MAKE) test
	$(MAKE) tools-test
	$(MAKE) browser-run
# 官方插件探针单独运行，不能用默认构建的补丁宿主代替。
verify-official-probe:
	bash scripts/verify-official-probe.sh
