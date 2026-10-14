// tooling 包通过公开脚本命令验证快速检查、暂存区门禁和无覆盖 hook 安装。
package tooling

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestFastRejectsBadFormat 验证公开快速检查拒绝未格式化的第一方 Go 文件。
func TestFastRejectsBadFormat(t *testing.T) {
	root := newFixture(t)
	writeFixture(t, root, "sample.go", "package fixture\nfunc Value()int{return 1}\n")
	output, err := runScript(t, root, "check-fast.sh")
	if err == nil || !strings.Contains(output, "gofmt") {
		t.Fatalf("未格式化源码应因 gofmt 被拒绝，err=%v\n%s", err, output)
	}
}

// TestStagedRejectsBadIndexWithFixedWorktree 验证工作树修复不能掩盖暂存区的格式错误。
func TestStagedRejectsBadIndexWithFixedWorktree(t *testing.T) {
	root := newFixture(t)
	writeFixture(t, root, "sample.go", "package fixture\nfunc Value()int{return 1}\n")
	runFixture(t, root, "git", "add", "sample.go")
	writeFixture(t, root, "sample.go", "package fixture\n\nfunc Value() int { return 1 }\n")
	output, err := runScript(t, root, "check-staged.sh")
	if err == nil || !strings.Contains(output, "gofmt") {
		t.Fatalf("暂存区格式错误应被拒绝，err=%v\n%s", err, output)
	}
}

// TestStagedAcceptsValidIndexWithBrokenWorktree 验证未暂存的格式及编译错误不会阻止有效 index。
func TestStagedAcceptsValidIndexWithBrokenWorktree(t *testing.T) {
	root := newFixture(t)
	writeFixture(t, root, "sample.go", "package fixture\nfunc Value()int{return missing}\n")
	output, err := runScript(t, root, "check-staged.sh")
	if err != nil {
		t.Fatalf("有效暂存区不应读取工作树错误：%v\n%s", err, output)
	}
}

// TestFastIncludesUntrackedGo 验证未忽略的新源码同样受格式门禁约束。
func TestFastIncludesUntrackedGo(t *testing.T) {
	root := newFixture(t)
	writeFixture(t, root, "new.go", "package fixture\nfunc New()int{return 2}\n")
	output, err := runScript(t, root, "check-fast.sh")
	if err == nil || !strings.Contains(output, "new.go") {
		t.Fatalf("新源码格式错误应被拒绝，err=%v\n%s", err, output)
	}
}

// TestFastRejectsBrokenGitMetadata 验证 Git 清单读取失败时停止，不能只打印错误后假装通过。
func TestFastRejectsBrokenGitMetadata(t *testing.T) {
	root := newFixture(t)
	writeFixture(t, root, ".git/config", "[broken\n")
	output, err := runScript(t, root, "check-fast.sh")
	if err == nil {
		t.Fatalf("Git 元数据损坏必须拒绝检查：\n%s", output)
	}
}

// TestFastCompilesWithoutRunningTests 验证快速门禁编译测试但不执行宿主或业务测试。
func TestFastCompilesWithoutRunningTests(t *testing.T) {
	root := newFixture(t)
	writeFixture(t, root, "sample_test.go", "package fixture\n\nimport \"testing\"\n\nfunc TestMustNotRun(t *testing.T) { panic(\"business test executed\") }\n")
	output, err := runScript(t, root, "check-fast.sh")
	if err != nil {
		t.Fatalf("快速检查只能编译测试而不执行：%v\n%s", err, output)
	}
}

// TestFastIgnoresForeignGitEnvironment 验证其他仓库的 Git 环境不能漏检目标根目录的新源码。
func TestFastIgnoresForeignGitEnvironment(t *testing.T) {
	root := newFixture(t)
	foreign := newFixture(t)
	writeFixture(t, root, "new.go", "package fixture\nfunc New()int{return 2}\n")
	output, err := commandFixture(root, "env", "GIT_DIR="+filepath.Join(foreign, ".git"), "GIT_WORK_TREE="+foreign, "bash", filepath.Join(projectRoot(t), "scripts/check-fast.sh"), root)
	if err == nil || !strings.Contains(output, "new.go") {
		t.Fatalf("外部 Git 环境不能跳过目标新源码，err=%v\n%s", err, output)
	}
}

// TestStagedIgnoresCachedHostGo 验证强制暂存的忽略宿主缓存不是第一方 Go 检查目标。
func TestStagedIgnoresCachedHostGo(t *testing.T) {
	root := newFixture(t)
	writeFixture(t, root, ".build/host/bad.go", "package host\nfunc Broken()int{return missing}\n")
	runFixture(t, root, "git", "add", "--force", ".build/host/bad.go")
	output, err := runScript(t, root, "check-staged.sh")
	if err != nil {
		t.Fatalf("忽略宿主缓存不应阻止第一方检查：%v\n%s", err, output)
	}
}

// TestFastFailsOfflineWithMissingDependency 验证缺少依赖时明确失败，不下载或自动更新模块文件。
func TestFastFailsOfflineWithMissingDependency(t *testing.T) {
	root := newFixture(t)
	mod := "module fixture\n\ngo 1.26.0\n\nrequire example.invalid/tooling-missing-dependency v0.0.1\n"
	writeFixture(t, root, "go.mod", mod)
	writeFixture(t, root, "sample.go", "package fixture\n\nimport _ \"example.invalid/tooling-missing-dependency\"\n")
	output, err := runScript(t, root, "check-fast.sh")
	if err == nil || !strings.Contains(output, "example.invalid/tooling-missing-dependency") {
		t.Fatalf("缺失依赖必须失败，err=%v\n%s", err, output)
	}
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil || string(data) != mod {
		t.Fatalf("检查不能改写 go.mod：%v，%q", err, data)
	}
	if _, err := os.Stat(filepath.Join(root, "go.sum")); !os.IsNotExist(err) {
		t.Fatalf("检查不能自动生成 go.sum：%v", err)
	}
}

// TestStagedRejectsCompileErrorInIndex 验证已格式化的 index 编译错误不能被工作树修复掩盖。
func TestStagedRejectsCompileErrorInIndex(t *testing.T) {
	root := newFixture(t)
	writeFixture(t, root, "sample.go", "package fixture\n\nfunc Value() int { return missing }\n")
	runFixture(t, root, "git", "add", "sample.go")
	writeFixture(t, root, "sample.go", "package fixture\n\nfunc Value() int { return 1 }\n")
	output, err := runScript(t, root, "check-staged.sh")
	if err == nil || !strings.Contains(output, "undefined: missing") {
		t.Fatalf("index 编译错误必须被拒绝，err=%v\n%s", err, output)
	}
}

// TestStagedRefusesExportIgnoreLoss 验证 archive 不能悄悄漏掉 export-ignore 标记的 tracked 文件。
func TestStagedRefusesExportIgnoreLoss(t *testing.T) {
	root := newFixture(t)
	writeFixture(t, root, ".gitattributes", "sample.go export-ignore\n")
	runFixture(t, root, "git", "add", ".gitattributes")
	output, err := runScript(t, root, "check-staged.sh")
	if err == nil || !strings.Contains(output, "导出缺失") || !strings.Contains(output, "sample.go") {
		t.Fatalf("导出漏检必须被拒绝，err=%v\n%s", err, output)
	}
}

// TestStagedChecksWhitespaceWithPatchAttributes 验证源码尾空白被拒绝而统一 diff 的上下文前缀被保留。
func TestStagedChecksWhitespaceWithPatchAttributes(t *testing.T) {
	t.Run("source-trailing-space", func(t *testing.T) {
		root := newFixture(t)
		writeFixture(t, root, "notes.txt", "trailing space \n")
		runFixture(t, root, "git", "add", "notes.txt")
		output, err := runScript(t, root, "check-staged.sh")
		if err == nil || !strings.Contains(output, "trailing whitespace") {
			t.Fatalf("源码尾空白必须由 git diff --check 拒绝，err=%v\n%s", err, output)
		}
	})
	t.Run("patch-context-space", func(t *testing.T) {
		root := newFixture(t)
		writeFixture(t, root, ".gitattributes", "host/patches/*.patch -whitespace\n")
		writeFixture(t, root, "host/patches/context.patch", "diff --git a/a.txt b/a.txt\n--- a/a.txt\n+++ b/a.txt\n@@ -1 +1,2 @@\n \n+new\n")
		runFixture(t, root, "git", "add", ".gitattributes", "host/patches/context.patch")
		output, err := runScript(t, root, "check-staged.sh")
		if err != nil {
			t.Fatalf("补丁属性必须保留统一 diff 前缀空格：%v\n%s", err, output)
		}
	})
}

// TestStagedRequiresOwnModule 验证 index 未含 go.mod 时不能借用快照上层工作树模块。
func TestStagedRequiresOwnModule(t *testing.T) {
	root := newFixture(t)
	index := filepath.Join(root, ".git", "tooling-no-module-index")
	runFixture(t, root, "env", "GIT_INDEX_FILE="+index, "git", "add", ".gitignore", "sample.go")
	output, err := commandFixture(root, "env", "GIT_INDEX_FILE="+index, "bash", filepath.Join(projectRoot(t), "scripts/check-staged.sh"), root)
	if err == nil || !strings.Contains(output, "go.mod") {
		t.Fatalf("快照必须具有自身模块，err=%v\n%s", err, output)
	}
}

// TestStagedUsesSelectedRealIndex 验证 hook 传入的真实 index 生效而快照不继承仓库定位环境。
func TestStagedUsesSelectedRealIndex(t *testing.T) {
	root := newFixture(t)
	writeFixture(t, root, "sample.go", "package fixture\nfunc Value()int{return missing}\n")
	runFixture(t, root, "git", "add", "sample.go")
	writeFixture(t, root, "sample.go", "package fixture\n\nfunc Value() int { return 1 }\n")
	index := filepath.Join(root, ".git", "tooling-selected-index")
	runFixture(t, root, "env", "GIT_INDEX_FILE="+index, "git", "add", "go.mod", ".gitignore", "sample.go")
	output, err := commandFixture(root, "env", "GIT_INDEX_FILE="+index, "GIT_DIR="+filepath.Join(root, ".git"), "GIT_WORK_TREE="+root, "bash", filepath.Join(projectRoot(t), "scripts/check-staged.sh"), root)
	if err != nil {
		t.Fatalf("必须检查传入的有效 index，而非默认坏 index：%v\n%s", err, output)
	}
}

// TestInstallRefusesConflictingHooksPath 验证既有 hooksPath 被拒绝且原配置不被覆盖。
func TestInstallRefusesConflictingHooksPath(t *testing.T) {
	root := newFixture(t)
	runFixture(t, root, "git", "config", "--local", "core.hooksPath", "existing-hooks")
	output, err := runScript(t, root, "install-hooks.sh")
	if err == nil || !strings.Contains(output, "hooksPath") {
		t.Fatalf("既有配置应导致明确拒绝，err=%v\n%s", err, output)
	}
	if got := strings.TrimSpace(runFixture(t, root, "git", "config", "--local", "--get", "core.hooksPath")); got != "existing-hooks" {
		t.Fatalf("原有配置被覆盖：%q", got)
	}
}

// TestInstallTargetsRootDespiteForeignGitEnvironment 验证安装只修改参数指定仓库的本地配置。
func TestInstallTargetsRootDespiteForeignGitEnvironment(t *testing.T) {
	root := newFixture(t)
	foreign := newFixture(t)
	copyPublicTools(t, root)
	output, err := commandFixture(root, "env", "GIT_DIR="+filepath.Join(foreign, ".git"), "GIT_WORK_TREE="+foreign, "bash", filepath.Join(projectRoot(t), "scripts/install-hooks.sh"), root)
	if err != nil {
		t.Fatalf("目标仓库安装应成功：%v\n%s", err, output)
	}
	if output, err := commandFixture(root, "git", "config", "--local", "--get", "core.hooksPath"); err != nil || strings.TrimSpace(output) != ".githooks" {
		t.Fatalf("应启用目标而非外部仓库，err=%v，%q", err, output)
	}
	if output, err := commandFixture(foreign, "git", "config", "--local", "--get", "core.hooksPath"); err == nil {
		t.Fatalf("不应修改外部仓库配置：%q", output)
	}
}

// TestInstallRefusesEmptyHooksPath 验证已有空 hooksPath 仍是用户配置，不被静默覆盖。
func TestInstallRefusesEmptyHooksPath(t *testing.T) {
	root := newFixture(t)
	copyPublicTools(t, root)
	runFixture(t, root, "git", "config", "--local", "core.hooksPath", "")
	output, err := runScript(t, root, "install-hooks.sh")
	if err == nil || !strings.Contains(output, "hooksPath") {
		t.Fatalf("已有空配置仍应拒绝覆盖，err=%v\n%s", err, output)
	}
}

// TestInstallPreservesExistingHook 验证默认 hooks 目录的真实 pre-commit 导致拒绝而非覆盖。
func TestInstallPreservesExistingHook(t *testing.T) {
	root := newFixture(t)
	original := "#!/bin/sh\n# existing user hook\nexit 0\n"
	writeFixture(t, root, ".git/hooks/pre-commit", original)
	output, err := runScript(t, root, "install-hooks.sh")
	if err == nil || !strings.Contains(output, "pre-commit") {
		t.Fatalf("既有 hook 应被拒绝，err=%v\n%s", err, output)
	}
	data, err := os.ReadFile(filepath.Join(root, ".git/hooks/pre-commit"))
	if err != nil || string(data) != original {
		t.Fatalf("既有 hook 未被保留：%v，%q", err, data)
	}
	if got, err := commandFixture(root, "git", "config", "--local", "--get", "core.hooksPath"); err == nil {
		t.Fatalf("拒绝后不应新增 hooksPath：%q", got)
	}
}

// TestInstallRequiresExecutableOwnHook 验证自带入口缺失或不可执行时不启用无效门禁。
func TestInstallRequiresExecutableOwnHook(t *testing.T) {
	for _, present := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing", true: "not-executable"}[present], func(t *testing.T) {
			root := newFixture(t)
			if present {
				writeFixture(t, root, ".githooks/pre-commit", "#!/bin/sh\nexit 0\n")
			}
			output, err := runScript(t, root, "install-hooks.sh")
			if err == nil || !strings.Contains(output, "可执行") {
				t.Fatalf("无有效入口应拒绝安装，err=%v\n%s", err, output)
			}
		})
	}
}

// TestInstallIsIdempotentAndHookChecksIndex 验证自带安装可重复执行且入口实际拒绝坏 index。
func TestInstallIsIdempotentAndHookChecksIndex(t *testing.T) {
	root := newFixture(t)
	copyPublicTools(t, root)
	for range 2 { // 两次调用验证重复启用不会覆盖其他设置或破坏入口。
		output, err := runScript(t, root, "install-hooks.sh")
		if err != nil {
			t.Fatalf("自带安装应成功：%v\n%s", err, output)
		}
	}
	if got := strings.TrimSpace(runFixture(t, root, "git", "config", "--local", "--get", "core.hooksPath")); got != ".githooks" {
		t.Fatalf("应仅启用当前仓库自带 hook：%q", got)
	}
	writeFixture(t, root, "sample.go", "package fixture\nfunc Value()int{return 1}\n")
	runFixture(t, root, "git", "add", "sample.go")
	output, err := commandFixture(root, "bash", filepath.Join(root, ".githooks/pre-commit"))
	if err == nil || !strings.Contains(output, "gofmt") {
		t.Fatalf("自带入口必须调用暂存检查，err=%v\n%s", err, output)
	}
}

// copyPublicTools 把公开脚本及入口复制至夹具并保留执行权限；读取、写入或设权失败时终止测试。
func copyPublicTools(t *testing.T, root string) {
	t.Helper()
	for _, path := range []string{"scripts/check-fast.sh", "scripts/check-staged.sh", "scripts/install-hooks.sh", ".githooks/pre-commit"} {
		data, err := os.ReadFile(filepath.Join(projectRoot(t), path))
		if err != nil {
			t.Fatal(err)
		}
		writeFixture(t, root, path, string(data))
		if err := os.Chmod(filepath.Join(root, path), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

// projectRoot 返回公开脚本所在仓库；定位失败时终止当前测试。
func projectRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// newFixture 创建保留在忽略目录的独立 Git 仓库并返回路径；创建或初始化失败时终止测试。
func newFixture(t *testing.T) string {
	t.Helper()
	base := filepath.Join(projectRoot(t), ".test-data")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(base, "tooling-fixture-")
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, root, "go.mod", "module fixture\n\ngo 1.26.0\n")
	writeFixture(t, root, ".gitignore", ".test-data/\n.build/\nnode_modules/\nvendor/\n")
	writeFixture(t, root, "sample.go", "package fixture\n\nfunc Value() int { return 1 }\n")
	runFixture(t, root, "git", "init", "--quiet")
	runFixture(t, root, "git", "add", "go.mod", ".gitignore", "sample.go")
	return root
}

// writeFixture 在夹具内写入指定相对路径；父目录或文件写入失败时终止测试。
func writeFixture(t *testing.T, root, path, content string) {
	t.Helper()
	full := filepath.Join(root, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// commandFixture 在夹具目录执行命令并返回输出及退出错误；沿用缓存但禁止联网下载。
func commandFixture(root, command string, args ...string) (string, error) {
	cmd := exec.Command(command, args...)
	cmd.Dir = root
	for _, entry := range os.Environ() {
		key := strings.SplitN(entry, "=", 2)[0]
		if strings.HasPrefix(key, "GIT_") || key == "GOPROXY" || key == "GOSUMDB" || key == "GOFLAGS" || key == "GOTOOLCHAIN" || key == "GOWORK" {
			continue
		}
		cmd.Env = append(cmd.Env, entry)
	}
	cmd.Env = append(cmd.Env, "GOPROXY=off", "GOSUMDB=off", "GOFLAGS=-mod=readonly", "GOTOOLCHAIN=local", "GOWORK=off")
	output, err := cmd.CombinedOutput()
	return string(output), err
}

// runFixture 执行必须成功的夹具命令并返回输出；非零退出时终止测试。
func runFixture(t *testing.T, root, command string, args ...string) string {
	t.Helper()
	output, err := commandFixture(root, command, args...)
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", command, args, err, output)
	}
	return output
}

// runScript 通过 bash 调用仓库的公开检查脚本，传入夹具根路径并返回输出和退出错误。
func runScript(t *testing.T, root, script string) (string, error) {
	t.Helper()
	return commandFixture(root, "bash", filepath.Join(projectRoot(t), "scripts", script), root)
}
