// Package testsupport 提供测试用的假服务。
//
// 它只被测试代码导入。把假服务做成可编排的「脚本」而非固定桩，
// 是为了能确定性地重现「请求工具 → 收到结果 → 再请求工具 → 给最终答复」
// 这类多轮序列——而这正是循环控制最容易出错的地方。
package testsupport

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"time"
)

// ToolCallScript 描述一次工具调用，其参数可按分片给出。
//
// 分片是刻意的：真实服务把 arguments 拆成多块陆续下发，id 只在首片出现，
// 多路调用靠 index 归位。让测试也走这条路，才能真正覆盖拼装逻辑。
type ToolCallScript struct {
	ID           string
	Name         string
	ArgFragments []string
}

// Script 描述一次流式响应的完整过程。
type Script struct {
	// Reasoning 是思维链的各个分片。
	Reasoning []string
	// Content 是正文的各个分片。
	Content []string
	// ToolCalls 是本轮请求的工具调用。
	ToolCalls []ToolCallScript
	// Finish 是结束原因；留空时按有无工具调用自动推断。
	Finish string
	// Status 非零且非 200 时，直接返回该 HTTP 状态码而不产生流。
	Status int
	// Delay 是分片之间的间隔，用于超时相关用例。
	Delay time.Duration
	// CloseAfter 大于零时，在发出这么多分片后强行断开连接，用于模拟流中断。
	CloseAfter int
}

// Answer 构造一个只返回文本的脚本，按字符分片，模拟真实的逐字输出。
func Answer(text string) Script {
	var parts []string
	for _, r := range text {
		parts = append(parts, string(r))
	}
	return Script{Content: parts}
}

// ToolCall 构造一个请求调用工具的脚本。
//
// argsJSON 会被拆成两个分片下发，以覆盖跨片拼装。
func ToolCall(id, name, argsJSON string) Script {
	mid := len(argsJSON) / 2
	return Script{
		Content: []string{"我来查一下。"},
		ToolCalls: []ToolCallScript{{
			ID:           id,
			Name:         name,
			ArgFragments: []string{argsJSON[:mid], argsJSON[mid:]},
		}},
		Finish: "tool_calls",
	}
}

// FakeLLM 是一个可编排的模型服务模拟器。
type FakeLLM struct {
	server *httptest.Server

	mu       sync.Mutex
	scripts  []Script
	requests int
	bodies   []string
}

// NewFakeLLM 创建模拟器。第 n 次请求返回第 n 个脚本；
// 请求次数超出脚本数量时，重复使用最后一个脚本。
func NewFakeLLM(scripts ...Script) *FakeLLM {
	f := &FakeLLM{scripts: scripts}
	f.server = httptest.NewServer(http.HandlerFunc(f.handle))
	return f
}

// URL 返回模拟服务的地址，用作客户端配置里的 BaseURL。
func (f *FakeLLM) URL() string { return f.server.URL }

// Close 关闭模拟服务。
func (f *FakeLLM) Close() { f.server.Close() }

// Requests 返回收到的请求次数。
func (f *FakeLLM) Requests() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests
}

// Body 返回第 i 次请求的原始请求体。
func (f *FakeLLM) Body(i int) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if i < 0 || i >= len(f.bodies) {
		return ""
	}
	return f.bodies[i]
}

// Bodies 返回全部请求的原始请求体，按到达顺序。
func (f *FakeLLM) Bodies() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.bodies))
	copy(out, f.bodies)
	return out
}

func (f *FakeLLM) handle(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)

	f.mu.Lock()
	idx := f.requests
	f.requests++
	f.bodies = append(f.bodies, string(body))
	script := f.pickLocked(idx)
	f.mu.Unlock()

	if script.Status != 0 && script.Status != http.StatusOK {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(script.Status)
		fmt.Fprintf(w, `{"error":{"message":"模拟的错误 %d","type":"mock_error"}}`, script.Status)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)

	flusher, _ := w.(http.Flusher)
	for i, chunk := range script.chunks() {
		if script.CloseAfter > 0 && i >= script.CloseAfter {
			// 强制断开：模拟响应中途连接中断。
			panic(http.ErrAbortHandler)
		}
		fmt.Fprintf(w, "data: %s\n\n", chunk)
		if flusher != nil {
			flusher.Flush()
		}
		if script.Delay > 0 {
			time.Sleep(script.Delay)
		}
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
}

func (f *FakeLLM) pickLocked(idx int) Script {
	if len(f.scripts) == 0 {
		return Script{Content: []string{"（没有配置脚本）"}}
	}
	if idx < len(f.scripts) {
		return f.scripts[idx]
	}
	return f.scripts[len(f.scripts)-1]
}

// chunks 把一个脚本展开成符合协议的 SSE 分片序列。
func (s Script) chunks() []string {
	out := []string{s.chunk(map[string]any{"role": "assistant"}, "")}

	for _, r := range s.Reasoning {
		out = append(out, s.chunk(map[string]any{"reasoning_content": r}, ""))
	}
	for _, c := range s.Content {
		out = append(out, s.chunk(map[string]any{"content": c}, ""))
	}
	for i, tc := range s.ToolCalls {
		// 首片携带 index、id 与函数名，arguments 为空。
		out = append(out, s.chunk(map[string]any{
			"tool_calls": []any{map[string]any{
				"index": i,
				"id":    tc.ID,
				"type":  "function",
				"function": map[string]any{
					"name":      tc.Name,
					"arguments": "",
				},
			}},
		}, ""))
		// 后续分片只携带 arguments 的片段，靠 index 归位。
		for _, frag := range tc.ArgFragments {
			out = append(out, s.chunk(map[string]any{
				"tool_calls": []any{map[string]any{
					"index": i,
					"function": map[string]any{
						"arguments": frag,
					},
				}},
			}, ""))
		}
	}

	out = append(out, s.chunk(map[string]any{}, s.effectiveFinish()))
	return out
}

func (s Script) effectiveFinish() string {
	if s.Finish != "" {
		return s.Finish
	}
	if len(s.ToolCalls) > 0 {
		return "tool_calls"
	}
	return "stop"
}

func (s Script) chunk(delta map[string]any, finish string) string {
	choice := map[string]any{"index": 0, "delta": delta}
	if finish != "" {
		choice["finish_reason"] = finish
	}
	payload := map[string]any{
		"id":      "chatcmpl-mock",
		"object":  "chat.completion.chunk",
		"created": 1700000000,
		"model":   "mock-model",
		"choices": []any{choice},
	}
	b, err := json.Marshal(payload)
	if err != nil {
		// 这里的输入全部由测试构造，序列化失败属于测试自身的问题。
		panic(err)
	}
	return string(b)
}
