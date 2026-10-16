//go:build legacy_patched

// 本文件保留私有 observer 桥接与兼容恢复的旧契约；当前真实存储故障及恢复见 TestGapHistoryActiveFault 和 TestHTTPProtocolsStorageFailure。
package system

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// TestGapHistoryCompatibility 验证无提交的不兼容也有历史，恢复和已阅互不改变健康，复发重新提醒。
func TestGapHistoryCompatibility(t *testing.T) {
	h, control := startCompatibilityHost(t, "extension-missing", "")
	before := readGapAlerts(t, h)
	if !gapItem(t, before, "observer_extension_missing").Active {
		t.Fatal("初始化接口缺失未显示活动故障")
	}
	if acknowledgeGaps(t, h, before, adminKey) != http.StatusOK {
		t.Fatal("无法确认活动不兼容")
	}
	if after := readGapAlerts(t, h); after.Unread || !gapItem(t, after, "observer_extension_missing").Active {
		t.Fatal("已阅隐藏活动故障或未减少提醒")
	}
	compatibilityControl(t, control, "")
	after := readGapAlerts(t, h)
	if gapItem(t, after, "observer_extension_missing").Active || after.Unread {
		t.Fatal("恢复丢失了已阅或仍显示活动不兼容")
	}
	compatibilityControl(t, control, "extension-missing")
	if again := readGapAlerts(t, h); !again.Unread || !gapItem(t, again, "observer_extension_missing").Active {
		t.Fatal("故障复发没有重新提醒")
	}
}

// TestGapHistoryPendingFaultRecovery 验证目录不能打开时的已阅仅在内存，可写后同进程移交历史且不补报完整。
func TestGapHistoryPendingFaultRecovery(t *testing.T) {
	h := startHost(t, 1<<30, true, "")
	h.monitorAll(t)
	h.stop()
	lock := filepath.Join(h.stateDir, ".audit.lock")
	if err := os.Chmod(lock, 0400); err != nil {
		t.Fatal(err)
	}
	broken := startHost(t, 1<<30, true, "", h.stateDir)
	alerts := readGapAlerts(t, broken)
	if len(alerts.Items) == 0 || !alerts.Items[0].Active {
		t.Fatal("无法打开目录时未显示活动运行故障")
	}
	if acknowledgeGaps(t, broken, alerts, adminKey) != http.StatusOK {
		t.Fatal("运行期故障不能确认已阅")
	}
	broken.request(t, "POST", "/v1/chat/completions", keyA, snapshotInput(t, "gap-pending-failure"))
	fresh := readGapAlerts(t, broken)
	if !fresh.Unread || fresh.Revision <= alerts.Revision || acknowledgeGaps(t, broken, alerts, adminKey) != http.StatusConflict {
		t.Fatal("无法打开存储时的新桥接缺口没有重新提醒")
	}
	if item := gapItem(t, fresh, "observer_unavailable"); item.AffectedCount != nil || item.CountAccuracy != "unknown" {
		t.Fatal("无法读取保留策略时伪造精确次数")
	}
	if acknowledgeGaps(t, broken, fresh, adminKey) != http.StatusOK {
		t.Fatal("不能确认运行期新缺口")
	}
	alerts = fresh
	if err := os.Chmod(lock, 0600); err != nil {
		t.Fatal(err)
	}
	after := readGapAlerts(t, broken)
	for _, previous := range alerts.Items {
		item := gapItem(t, after, previous.Reason)
		if item.Active || !item.HistoryRetained || item.AffectedCount != nil || item.Unread {
			t.Fatal("恢复打开目录丢失已阅历史、虚报精确损失或仍显示旧活动故障")
		}
	}
	waitBudgetState(t, broken, func(state budgetState) bool { return state.DiagnosticsPersisted })
	broken.stop()
	again := startHost(t, 1<<30, true, "", h.stateDir)
	for _, previous := range alerts.Items {
		if item := gapItem(t, readGapAlerts(t, again), previous.Reason); item.Unread || !item.HistoryRetained {
			t.Fatal("恢复写入后的正常重启丢失已阅历史")
		}
	}
}
