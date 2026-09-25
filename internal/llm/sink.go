package llm

import (
	"strings"
	"sync"
)

// Sink 接收流式响应中的增量。
//
// 把它抽成接口是为了把「渲染」与「业务」解耦：终端实现负责染色输出，
// 测试实现负责缓冲成字符串。这样流式行为可以被确定性地断言，
// 且断言不依赖 TTY。
type Sink interface {
	// OnReasoning 收到一段思维链增量。
	OnReasoning(delta string)
	// OnContent 收到一段正文增量。
	OnContent(delta string)
	// OnToolCall 报告一个工具调用已拼装完成。
	OnToolCall(name string)
	// OnDone 报告一个完整响应已收齐。
	OnDone()
}

// NopSink 丢弃全部增量。
type NopSink struct{}

func (NopSink) OnReasoning(string) {}
func (NopSink) OnContent(string)   {}
func (NopSink) OnToolCall(string)  {}
func (NopSink) OnDone()            {}

// BufferSink 把增量缓冲下来供断言使用。
type BufferSink struct {
	mu        sync.Mutex
	reasoning strings.Builder
	content   strings.Builder
	tools     []string
	done      bool
}

// NewBufferSink 创建一个缓冲型 Sink。
func NewBufferSink() *BufferSink { return &BufferSink{} }

func (b *BufferSink) OnReasoning(delta string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.reasoning.WriteString(delta)
}

func (b *BufferSink) OnContent(delta string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.content.WriteString(delta)
}

func (b *BufferSink) OnToolCall(name string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.tools = append(b.tools, name)
}

func (b *BufferSink) OnDone() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.done = true
}

// Reasoning 返回缓冲到的全部思维链。
func (b *BufferSink) Reasoning() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.reasoning.String()
}

// Content 返回缓冲到的全部正文。
func (b *BufferSink) Content() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.content.String()
}

// Tools 返回已拼装完成的工具调用名称，按完成顺序。
func (b *BufferSink) Tools() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]string, len(b.tools))
	copy(out, b.tools)
	return out
}

// Done 报告是否收到过完成信号。
func (b *BufferSink) Done() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.done
}
