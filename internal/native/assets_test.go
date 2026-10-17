// 本文件验证嵌入页面的 CSP 摘要与实际脚本/样式一致，防止 UI 改动后浏览器拒绝执行。
package native

import (
	"crypto/sha256"
	"encoding/base64"
	"regexp"
	"strings"
	"testing"
)

// TestEmbeddedContentSecurityPolicy 验证用途标签等内联改动同步更新 CSP，不以 unsafe-inline 放宽页面执行权限。
func TestEmbeddedContentSecurityPolicy(t *testing.T) {
	page, err := assets.ReadFile("assets/index.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(page)
	for _, kind := range []string{"script", "style"} {
		blocks := regexp.MustCompile(`(?s)<`+kind+`>(.*?)</`+kind+`>`).FindAllStringSubmatch(html, -1)
		if len(blocks) != 1 {
			t.Fatalf("预期单个 %s 内联块", kind)
		}
		digest := sha256.Sum256([]byte(blocks[0][1]))
		directive := kind + "-src 'sha256-" + base64.StdEncoding.EncodeToString(digest[:]) + "'"
		if !strings.Contains(html, directive) {
			t.Fatalf("%s CSP 摘要未同步", kind)
		}
	}
	if strings.Contains(html, "'unsafe-inline'") {
		t.Fatal("不能通过允许任意内联脚本绕过摘要校验")
	}
}
