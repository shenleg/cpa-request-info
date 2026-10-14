// 本文件静态核对CI复用本地入口、最小权限和有限证据，避免把配置有效冒称远端运行成功。
package tooling

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestWorkflowUsesLocalVerification 验证CI只使用已支持平台及本地统一命令，并限制上传证据。
func TestWorkflowUsesLocalVerification(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, ".github/workflows/ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Permissions map[string]string `yaml:"permissions"`
		Jobs        map[string]struct {
			Runner  string `yaml:"runs-on"`
			Timeout int    `yaml:"timeout-minutes"`
			Steps   []struct {
				Run, Uses string
				With      map[string]string
			}
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(raw, &config); err != nil {
		t.Fatal(err)
	}
	if len(config.Permissions) != 1 || config.Permissions["contents"] != "read" {
		t.Fatal("CI权限不是最小只读范围")
	}
	if len(config.Jobs) != 1 {
		t.Fatal("本轮不应扩大平台矩阵")
	}
	job := config.Jobs["verify"]
	if job.Runner != "macos-15" || job.Timeout <= 0 {
		t.Fatal("缺少支持的平台或明确期限")
	}
	verified := false
	artifacts := false
	for _, step := range job.Steps {
		if step.Run == "make verify" {
			verified = true
		}
		if strings.Contains(step.Uses, "upload-artifact") {
			artifacts = true
			for _, pattern := range strings.Fields(step.With["path"]) {
				if pattern != ".test-data/verification-*/result.json" && pattern != ".test-data/verification-*/*.png" {
					t.Fatal("上传范围可能包含审计数据/认证/磁盘镜像")
				}
			}
		}
	}
	if !verified || !artifacts {
		t.Fatal("CI未复用统一验收或未保留有限证据")
	}
	if strings.Contains(string(raw), "secrets.") {
		t.Fatal("合成CI不应读取真实认证材料")
	}
}
