// 本文件在接收边界冻结有界采集决策；快照只随请求传递，不建立待提交表或保存名称/凭据。
package audit

// Snapshot 是单次提交的不可变资格与上限；CollectorGeneration 仅区分同进程重开的采集器，不持久化。
type Snapshot struct {
	Version             int    `json:"version"`
	Decision            string `json:"decision"`
	MaxTextBytes        int    `json:"max_text_bytes"`
	CollectorGeneration string `json:"collector_generation"`
}

// Prepare 根据接收时配置给 event 返回独立决策；event 仅含可信身份和提交元数据，不传正文。
// 无磁盘 I/O 或异常；未监控/主动暂停为skip，无效事件或存储故障为unavailable，不影响模型调用。
func (c *Collector) Prepare(event Event) Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	result := Snapshot{Version: 3, Decision: "unavailable", CollectorGeneration: c.generation}
	if !validEvent(event) {
		c.incrementGap("invalid_event")
		return result
	}
	if !c.configValid {
		return result
	}
	if !c.config.Objects[event.ObjectID].Monitored || c.config.CapturePaused {
		result.Decision = "skip"
		return result
	}
	if c.closed || c.status.State != "active" {
		return result
	}
	result.Decision = "capture"
	result.MaxTextBytes = c.config.MaxTextBytes
	return result
}
