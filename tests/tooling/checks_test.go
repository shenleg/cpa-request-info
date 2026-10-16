// tooling 包验证手动快速检查、真实暂存快照与未经补丁修改的官方隔离构建。
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

// TestStagedUsesSelectedRealIndex 验证调用者指定的真实 index 生效而快照不继承仓库定位环境。
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

// TestOfficialBuildWithSymlinkBuild 验证新构建直接使用独立官方源码，且 .build 在其他卷时仍采用物理路径。
func TestOfficialBuildWithSymlinkBuild(t *testing.T) {
	for _, script := range []string{"build.sh", "build-retention-test.sh"} {
		t.Run(script, func(t *testing.T) {
			root := newFixture(t)
			physical := newFixture(t)
			if err := os.Symlink(physical, filepath.Join(root, ".build")); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(projectRoot(t), "scripts", script))
			if err != nil {
				t.Fatal(err)
			}
			text := string(data)
			if strings.Contains(text, "patch -d") || strings.Contains(text, "-overlay") || !strings.Contains(text, `git -C "$SOURCE" archive "$COMMIT"`) {
				t.Fatal("精简版宿主必须从官方对象导出，不应用补丁或 overlay")
			}
			assignment := ""
			for _, line := range strings.Split(text, "\n") {
				if strings.HasPrefix(line, "WORK=") {
					assignment = line
				}
			}
			if assignment == "" {
				t.Fatal("官方源码必须构建于新目录")
			}
			runFixture(t, root, "mkdir", "-p", ".build/sources", ".build/retention-test/sources")
			setup := "set -euo pipefail\nROOT=\"$1\"\nOUT=\"$1/.build/retention-test\"\n" + assignment + "\nprintf '%s\\n' \"$WORK\"\n"
			work := strings.TrimSpace(runFixture(t, root, "bash", "-c", setup, "official-build-test", root))
			physicalPath, err := filepath.EvalSymlinks(work)
			if err != nil || work != physicalPath {
				t.Fatal("官方构建 WORK 未使用物理路径")
			}
			writeFixture(t, work, "go.mod", "module officialfixture\n\ngo 1.26.0\n")
			writeFixture(t, work, "main.go", "package main\n\nimport \"fmt\"\n\nfunc main() { fmt.Println(\"official-host\") }\n")
			binary := filepath.Join(work, "host")
			runFixture(t, root, "go", "-C", work, "build", "-o", binary, ".")
			if got := strings.TrimSpace(runFixture(t, root, binary)); got != "official-host" {
				t.Fatal("独立官方构建未实际运行")
			}
		})
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
