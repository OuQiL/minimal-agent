// Package tool 定义工具的声明、注册、查找与执行。
//
// 工具自描述（名称、描述、参数 Schema）会被原样提交给模型，由模型自主
// 决定调用哪个、传什么参数。这里不做任何「哪类问题该用哪个工具」的映射，
// 那是模型的职责。
package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ErrInvalidTool 表示工具的声明不完整。
var ErrInvalidTool = errors.New("工具声明不完整")

// Property 描述工具的一个参数。
type Property struct {
	Type        string   `json:"type"`
	Description string   `json:"description,omitempty"`
	Enum        []string `json:"enum,omitempty"`
}

// Schema 是工具参数的 JSON Schema 子集。
//
// 只覆盖内置工具实际用到的约束：类型、必填、枚举。刻意不实现完整
// JSON Schema——为一个子集引入一个完整实现，其错误信息还不适合直接
// 回填给模型，性价比为负。
type Schema struct {
	Type       string              `json:"type"`
	Properties map[string]Property `json:"properties,omitempty"`
	Required   []string            `json:"required,omitempty"`
}

// Tool 是一个可被模型调用的工具。
type Tool interface {
	Name() string
	Description() string
	// Aliases 返回模型可能使用的其他名称，用于兜住模型对工具名的幻觉。
	Aliases() []string
	Parameters() Schema
	// Execute 执行工具并返回面向模型的文本结果。
	// args 是模型给出的原始 JSON 参数，由各工具自行解码。
	Execute(ctx context.Context, args json.RawMessage) (string, error)
}

// --- 会话上下文传递 ---
//
// todo 这类工具需要知道自己作用于哪个会话，但工具是在启动时一次性注册的，
// 而当前会话会随用户切换而变。因此会话标识经由 context 传递，
// 而不是把工具做成每会话一份。

type sessionCtxKey struct{}

// WithSessionID 把当前会话标识放入 context。
func WithSessionID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, sessionCtxKey{}, id)
}

// SessionIDFrom 从 context 取出当前会话标识；不存在时返回空串。
func SessionIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(sessionCtxKey{}).(string)
	return id
}

// --- 注册表 ---

// Registry 持有全部已注册工具。
type Registry struct {
	// canonical 以规范名为键。
	canonical map[string]Tool
	// lookup 以归一化后的名称为键，值为规范名。规范名与别名都登记在这里。
	lookup map[string]string
	order  []string
}

// NewRegistry 创建一个空注册表。
func NewRegistry() *Registry {
	return &Registry{
		canonical: make(map[string]Tool),
		lookup:    make(map[string]string),
	}
}

// Register 注册一个工具。声明不完整或名称冲突时返回错误。
func (r *Registry) Register(t Tool) error {
	if t == nil {
		return fmt.Errorf("%w: 工具为空", ErrInvalidTool)
	}
	name := strings.TrimSpace(t.Name())
	if name == "" {
		return fmt.Errorf("%w: 缺少名称", ErrInvalidTool)
	}
	if strings.TrimSpace(t.Description()) == "" {
		return fmt.Errorf("%w: 工具 %s 缺少描述", ErrInvalidTool, name)
	}
	if strings.TrimSpace(t.Parameters().Type) == "" {
		return fmt.Errorf("%w: 工具 %s 缺少参数 Schema", ErrInvalidTool, name)
	}
	key := normalize(name)
	if _, dup := r.lookup[key]; dup {
		return fmt.Errorf("%w: 名称或别名 %q 已被占用", ErrInvalidTool, name)
	}

	r.canonical[name] = t
	r.lookup[key] = name
	r.order = append(r.order, name)

	for _, a := range t.Aliases() {
		ak := normalize(a)
		// 归一化后与规范名相同（例如工具叫 get_weather、别名写作 getWeather），
		// 或与同一工具的另一个别名相同（web_search 与 websearch），
		// 都指向同一个工具，重复登记没有意义，也不算冲突。
		if ak == "" || ak == key || r.lookup[ak] == name {
			continue
		}
		if owner, dup := r.lookup[ak]; dup {
			return fmt.Errorf("%w: 工具 %s 的别名 %q 与工具 %s 冲突", ErrInvalidTool, name, a, owner)
		}
		r.lookup[ak] = name
	}
	return nil
}

// Lookup 按名称解析工具。
//
// 返回值依次为：工具本身、是否命中、以及是否经由别名命中。
// 解析过程只做归一化与显式别名匹配，不做模糊匹配——把 weather 与 whether
// 匹配上会把拼写错误变成静默的成功调用，掩盖模型正在幻觉工具名这一信号。
func (r *Registry) Lookup(name string) (Tool, bool, bool) {
	key := normalize(name)
	canonical, ok := r.lookup[key]
	if !ok {
		return nil, false, false
	}
	t, ok := r.canonical[canonical]
	if !ok {
		return nil, false, false
	}
	return t, true, normalize(canonical) != key
}

// All 按注册顺序返回全部工具。
func (r *Registry) All() []Tool {
	out := make([]Tool, 0, len(r.order))
	for _, n := range r.order {
		out = append(out, r.canonical[n])
	}
	return out
}

// Names 按注册顺序返回全部规范名。
func (r *Registry) Names() []string {
	out := make([]string, len(r.order))
	copy(out, r.order)
	return out
}

// UnknownMessage 生成未知工具的回填内容。
//
// 关键在于列出候选集：只回一句「工具不存在」，模型极可能重复调用同一个
// 错名；给出可用清单后，模型通常一次就能纠正。
func (r *Registry) UnknownMessage(name string) string {
	names := r.Names()
	sort.Strings(names)
	return fmt.Sprintf("工具 %q 不存在。当前可用工具为：%s。请从其中选择。",
		name, strings.Join(names, "、"))
}

// normalize 把名称归一化：转小写并剥离下划线与连字符。
// 使 get_weather / getWeather / Get-Weather 落到同一个键上。
func normalize(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	name = strings.ReplaceAll(name, "_", "")
	name = strings.ReplaceAll(name, "-", "")
	return name
}
