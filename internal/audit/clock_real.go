//go:build !audittest

// 本文件提供正式构建的服务端时钟与每分钟维护信号，不读取测试控制环境。
package audit

import "time"

// auditNow 返回当前服务端时间，不接受外部时钟控制。
func auditNow() time.Time {
	return time.Now()
}

// newMaintenanceTimer 返回每分钟维护信号及停止函数；调用方退出时负责停止计时器。
func newMaintenanceTimer() (<-chan time.Time, func()) {
	ticker := time.NewTicker(time.Minute)
	return ticker.C, ticker.Stop
}
