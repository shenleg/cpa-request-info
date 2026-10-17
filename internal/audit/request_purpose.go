// 本文件定义请求用途的有限分类；分类不证明正文由人工输入，辅助分类只用于采集前跳过。
package audit

// RequestPurpose 表示宿主按已有用途标记判定的执行用途，不携带原始请求头或子任务身份。
// 新记录仅保存 main、turn、unknown；skip 不进入存储，空值仅表示旧记录未记录用途。
type RequestPurpose string

const (
	// PurposeUnknown 表示没有可一致解释的用途标记，正文继续使用既有未知用途规则。
	PurposeUnknown RequestPurpose = "unknown"
	// PurposeMain 表示明确的主会话调用，不代表人工原文认证。
	PurposeMain RequestPurpose = "main"
	// PurposeTurn 表示明确的对话轮次，不提升为主会话或人工来源保证。
	PurposeTurn RequestPurpose = "turn"
	// PurposeSkip 表示明确的辅助或子任务执行，仅发现匿名身份，不提取或保存正文。
	PurposeSkip RequestPurpose = "skip"
)
