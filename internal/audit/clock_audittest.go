//go:build audittest

// 本文件仅在隔离验收构建中读取文件时钟；夹具必须原子替换，解析失败立即终止测试进程。
package audit

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

// auditNow 返回夹具中的服务端时间；未设置控制路径时使用真实时间，夹具错误使进程退出。
func auditNow() time.Time {
	path := os.Getenv("CPA_AUDIT_TEST_CLOCK")
	if path == "" {
		return time.Now()
	}
	now, _ := readAuditTestClock(path)
	return now
}

// newMaintenanceTimer 返回维护信号及幂等停止函数；有夹具时只在 sweep 改变后发信号。
// 初始化只读取基准，不发信号，启动清理由调用方主动执行；夹具错误使进程退出。
func newMaintenanceTimer() (<-chan time.Time, func()) {
	path := os.Getenv("CPA_AUDIT_TEST_CLOCK")
	if path == "" {
		ticker := time.NewTicker(time.Minute)
		return ticker.C, ticker.Stop
	}
	_, sweep := readAuditTestClock(path)
	// 10ms 只影响测试指令响应速度；期限计算始终使用文件时间，不推进真实网络超时。
	ticker := time.NewTicker(10 * time.Millisecond)
	signals := make(chan time.Time, 1)
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				now, nextSweep := readAuditTestClock(path)
				if nextSweep == sweep {
					continue
				}
				sweep = nextSweep
				select {
				case signals <- now:
				case <-done:
					return
				}
			}
		}
	}()
	var once sync.Once
	return signals, func() {
		once.Do(func() { close(done) })
		<-stopped
	}
}

// readAuditTestClock 读取完整夹具并返回当前时间和维护序号；文件或字段错误明确终止验收进程。
func readAuditTestClock(path string) (time.Time, int) {
	var fixture struct {
		Now   string `json:"now"`
		Sweep *int   `json:"sweep"`
	}
	raw, err := os.ReadFile(path)
	if err == nil {
		err = json.Unmarshal(raw, &fixture)
	}
	if err == nil && fixture.Sweep == nil {
		err = fmt.Errorf("missing sweep")
	}
	var now time.Time
	if err == nil {
		now, err = time.Parse(time.RFC3339Nano, fixture.Now)
	}
	if err != nil {
		// 宿主 WS/native 边界会恢复 panic；显式退出才能防止坏夹具被伪装为正常跳过。
		fmt.Fprintf(os.Stderr, "CPA_AUDIT_TEST_CLOCK invalid: %v\n", err)
		os.Exit(2)
	}
	return now, *fixture.Sweep
}
