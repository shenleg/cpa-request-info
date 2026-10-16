//go:build legacy_patched

// 本文件保留枚举宿主 Key 并判断有效性的旧契约；当前已发现匿名目录及重启由 TestOfficialKnownCallerDirectory 验证。
package system

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

// TestInvalidKeyKeepsHistoricalCatalog 验证 Key 退出宿主名单后仍有历史筛选和改名入口，不能重新启用无效 Key。
func TestInvalidKeyKeepsHistoricalCatalog(t *testing.T) {
	h := startHost(t, 1<<30, true, "")
	h.monitorAll(t)
	marker := fmt.Sprintf("LIFECYCLE-CATALOG-%d", time.Now().UnixNano())
	lifecycleSubmit(t, h, keyA, marker)
	record := lifecycleSaved(t, h, marker)
	lifecycleWrite(t, h, "/remove", map[string]string{"object_id": record.ObjectID})
	lifecycleKeys(t, h, []string{keyB})
	found := false
	for _, object := range lifecycleObjects(t, h) {
		if object.ID == record.ObjectID {
			found = true
			if object.Valid || object.Monitored || !object.HasHistory || object.Name != record.Name {
				t.Fatal("旧对象目录没有区分无效 Key、停用和历史")
			}
		}
	}
	if !found {
		t.Fatal("Key 失效后旧对象从历史目录消失")
	}
	lifecycleWrite(t, h, "/rename", map[string]string{"object_id": record.ObjectID, "name": "失效 Key 历史名称"})
	body, _ := json.Marshal(map[string]string{"object_id": record.ObjectID, "name": "不应重新启用"})
	code, _ := h.request(t, "POST", api+"/monitor", adminKey, body)
	if code != 400 {
		t.Fatal("无效客户端 Key 被重新启用")
	}
	if records, total := lifecycleRecords(t, h, record.ObjectID); total != 1 || len(records) != 1 || records[0].Name != "失效 Key 历史名称" {
		t.Fatal("旧对象无法筛选历史或显示当前名称")
	}
}
