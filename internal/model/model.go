// Package model 定义各层共享的领域类型。
//
// 它不依赖本项目的任何其他包，因此 llm、store、tool、session、contextmgr、agent
// 都可以自由引用它而不产生循环依赖。字段命名与 OpenAI 兼容协议对齐，
// 使存储与请求组装之间不需要额外转换。
package model

import "time"

// Role 是消息在对话中的角色。
type Role string

// 协议定义的五种角色中，本项目实际使用以下四种。
const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// ToolCall 是模型请求的一次工具调用。
type ToolCall struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Args 是原始的 JSON 参数串，保持与协议一致，不做预解析，
	// 以便原样落库并在回填时还原。
	Args string `json:"args"`
}

// Message 是一条会话消息。
type Message struct {
	ID        int64
	SessionID string
	Seq       int
	Role      Role
	Content   string
	// Reasoning 是模型本轮返回的思维链，仅落库供查看与排查，
	// 绝不进入后续请求的上下文——它体积增长快，且推理模型并不要求
	// 把上一轮的思维链再喂回去。
	Reasoning string
	// ToolCalls 仅在 Role 为 assistant 且该轮请求了工具时非空。
	ToolCalls []ToolCall
	// ToolCallID 仅在 Role 为 tool 时非空，指向它所回应的那次调用。
	ToolCallID string
	CreatedAt  time.Time
}

// FinishReason 是一轮响应的结束原因，用于决定循环走向。
type FinishReason string

// 协议可能返回多种结束原因，本项目需要区分的是「正常收尾」与「请求工具」。
const (
	FinishStop      FinishReason = "stop"
	FinishToolCalls FinishReason = "tool_calls"
	FinishLength    FinishReason = "length"
	FinishOther     FinishReason = "other"
)

// WantsTools 判断该响应是否要求执行工具。
func (r Response) WantsTools() bool {
	return len(r.ToolCalls) > 0
}

// Response 是一轮模型响应的领域模型。
type Response struct {
	// Reasoning 是模型的思维链（推理模型通过 reasoning_content 返回）。
	Reasoning string
	// Content 是正文。与 ToolCalls 并存时，它是过程说明而非最终答复。
	Content string
	// ToolCalls 是本轮请求的全部工具调用，顺序即模型给出的调用序号。
	ToolCalls []ToolCall
	Finish    FinishReason
}

// Session 是一个会话。
type Session struct {
	// ID 是内部唯一标识（形如 sess_2efdb9c24d），程序用它做一切关联。
	ID string
	// Num 是面向用户的编号，按创建顺序从 1 开始递增。
	//
	// 单用户场景下，手输一个随机标识并不现实，因此对外只暴露这个编号。
	// 它由创建顺序推导，因此在会话不被删除的前提下是稳定的——
	// 列表按最近活动排序会让位置变化，编号则不会跟着变。
	Num     int
	Title   string
	Summary string
	// Preview 是会话首条用户消息的片段，用于让用户认出这是哪段对话。
	Preview string
	// CreatedAt 与 UpdatedAt 用于会话列表展示，UpdatedAt 表示最近活动时间。
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Todo 是一条待办事项，按会话隔离。
type Todo struct {
	ID        int64
	SessionID string
	Content   string
	Done      bool
	CreatedAt time.Time
}

// TraceStatus 是一次工具调用的执行状态。
type TraceStatus string

// 工具调用的两种终态。
const (
	TraceOK    TraceStatus = "ok"
	TraceError TraceStatus = "error"
)

// Trace 是一次工具调用的可追溯记录。
//
// Alias 与 Repeated 是行为诊断标记：前者说明模型用了别名而非规范名，
// 后者说明模型以相同参数重复调用了同一工具。两者都指向「模型行为异常
// 或工具描述不够清楚」，因此记录下来而非静默处理。
type Trace struct {
	ID         int64
	SessionID  string
	ToolName   string
	Args       string
	Result     string
	Status     TraceStatus
	Error      string
	StartedAt  time.Time
	EndedAt    time.Time
	DurationMS int64
	Alias      bool
	Repeated   bool
}
