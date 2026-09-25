// Package llm 封装与模型服务的交互。
//
// 传输、SSE 解析与工具调用分片的拼装交给官方 SDK；本包负责的是把它
// 转换成项目自己的领域模型，并处理 SDK 覆盖不到的事——非标准字段
// reasoning_content 的提取、错误分类、以及流式中断的语义。
package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/packages/param"
	"github.com/openai/openai-go/shared"

	"minimal-agent/internal/model"
)

// HTTPDoer 是 SDK 可接受的最小 HTTP 客户端接口，便于测试注入。
type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// Options 是构造客户端所需的配置。
type Options struct {
	BaseURL    string
	APIKey     string
	Model      string
	MaxRetries int
	Timeout    time.Duration
	HTTPClient HTTPDoer
}

// ToolSpec 是提交给模型的工具声明。
//
// 这里用独立的结构体而不是直接引用 tool.Tool，是为了不让 LLM 层依赖
// 工具的执行语义——它只需要知道「有哪些工具、各自接受什么参数」。
type ToolSpec struct {
	Name        string
	Description string
	// Parameters 是参数的 JSON Schema。
	Parameters map[string]any
}

// Client 是与模型服务交互的客户端。
type Client struct {
	sdk   openai.Client
	model string
}

// New 构造客户端。
func New(opts Options) *Client {
	reqOpts := []option.RequestOption{
		option.WithAPIKey(opts.APIKey),
		option.WithBaseURL(opts.BaseURL),
		option.WithMaxRetries(opts.MaxRetries),
		option.WithRequestTimeout(opts.Timeout),
	}
	if opts.HTTPClient != nil {
		reqOpts = append(reqOpts, option.WithHTTPClient(opts.HTTPClient))
	}
	return &Client{
		sdk:   openai.NewClient(reqOpts...),
		model: opts.Model,
	}
}

// Stream 发起一次流式请求，把增量交给 sink，返回累加后的完整响应。
//
// 重试语义：请求建立阶段的失败由 SDK 按 WithMaxRetries 自动重试（含流式请求，
// 已实测）。一旦已经开始向用户输出内容后中断，则不再重试，改为返回
// StreamInterruptedError——此时重试会导致内容重复显示。
func (c *Client) Stream(
	ctx context.Context,
	msgs []model.Message,
	system string,
	tools []ToolSpec,
	sink Sink,
) (*model.Response, error) {
	if sink == nil {
		sink = NopSink{}
	}

	params := openai.ChatCompletionNewParams{
		Model:    c.model,
		Messages: toSDKMessages(system, msgs),
	}
	if len(tools) > 0 {
		params.Tools = toSDKTools(tools)
	}

	stream := c.sdk.Chat.Completions.NewStreaming(ctx, params)
	defer stream.Close()

	var (
		acc       openai.ChatCompletionAccumulator
		reasoning strings.Builder
		content   strings.Builder
		emitted   int
	)

	for stream.Next() {
		chunk := stream.Current()
		acc.AddChunk(chunk)

		if len(chunk.Choices) > 0 {
			delta := chunk.Choices[0].Delta
			if r := reasoningOf(delta); r != "" {
				reasoning.WriteString(r)
				emitted += len(r)
				sink.OnReasoning(r)
			}
			if delta.Content != "" {
				content.WriteString(delta.Content)
				emitted += len(delta.Content)
				sink.OnContent(delta.Content)
			}
		}
		if tc, ok := acc.JustFinishedToolCall(); ok {
			sink.OnToolCall(tc.Name)
		}
	}

	if err := stream.Err(); err != nil {
		if emitted > 0 {
			return &model.Response{
					Reasoning: reasoning.String(),
					Content:   content.String(),
				},
				&StreamInterruptedError{
					EmittedChars: emitted,
					Reasoning:    reasoning.String(),
					Content:      content.String(),
					Err:          err,
				}
		}
		return nil, Classify(err)
	}

	if len(acc.Choices) == 0 {
		return nil, &RequestError{Kind: KindUnknown, Message: "模型未返回任何内容"}
	}

	choice := acc.Choices[0]
	resp := &model.Response{
		Reasoning: reasoning.String(),
		Content:   choice.Message.Content,
		Finish:    mapFinish(choice.FinishReason),
	}
	for _, tc := range choice.Message.ToolCalls {
		resp.ToolCalls = append(resp.ToolCalls, model.ToolCall{
			ID:   tc.ID,
			Name: tc.Function.Name,
			Args: tc.Function.Arguments,
		})
	}

	// 兜底：部分兼容实现不返回 finish_reason。既然拿到了工具调用，
	// 就按「需要执行工具」处理，否则循环会误判为最终答复。
	if resp.Finish != model.FinishToolCalls && len(resp.ToolCalls) > 0 {
		resp.Finish = model.FinishToolCalls
	}

	sink.OnDone()
	return resp, nil
}

// Complete 发起一次不关心增量过程的请求，返回完整响应。
//
// 用于摘要生成这类内部调用：它们不需要把增量渲染给用户，但仍要走
// 与主流程完全相同的请求与重试路径。
func (c *Client) Complete(ctx context.Context, msgs []model.Message, system string) (*model.Response, error) {
	return c.Stream(ctx, msgs, system, nil, NopSink{})
}

// reasoningOf 从分片中取出思维链增量。
//
// reasoning_content 是非标准字段，有三个容易踩的点：
//
//  1. SDK 的类型定义里没有它，累加器也不会保留它（累加完毕后 ExtraFields
//     是空 map），因此只能在逐块消费时现场捕获。
//  2. Field.Raw() 返回的是带引号的 JSON 字面量，需要再解一次才能得到真实
//     字符串，否则思维链会带着转义符显示。
//  3. **不要以 Field.Valid() 作为判据**。SDK 对所有「不在类型定义内的额外
//     字段」一律报告 Valid()==false，但 Raw() 的内容是有效的。若按 Valid()
//     过滤，思维链会被整体丢弃——判断依据只能是原始内容本身。
func reasoningOf(delta openai.ChatCompletionChunkChoiceDelta) string {
	f, ok := delta.JSON.ExtraFields["reasoning_content"]
	if !ok {
		return ""
	}
	raw := f.Raw()
	if raw == "" || raw == "null" {
		return ""
	}
	var s string
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return ""
	}
	return s
}

func mapFinish(s string) model.FinishReason {
	switch s {
	case "stop":
		return model.FinishStop
	case "tool_calls":
		return model.FinishToolCalls
	case "length":
		return model.FinishLength
	default:
		return model.FinishOther
	}
}

// toSDKMessages 把领域消息转换成 SDK 的消息参数。
//
// 注意这里刻意不使用 m.Reasoning：思维链只落库供查看，不回填到后续请求。
// 见 model.Message.Reasoning 的说明。
func toSDKMessages(system string, msgs []model.Message) []openai.ChatCompletionMessageParamUnion {
	out := make([]openai.ChatCompletionMessageParamUnion, 0, len(msgs)+1)
	if system != "" {
		out = append(out, openai.SystemMessage(system))
	}
	for _, m := range msgs {
		switch m.Role {
		case model.RoleSystem:
			out = append(out, openai.SystemMessage(m.Content))
		case model.RoleUser:
			out = append(out, openai.UserMessage(m.Content))
		case model.RoleAssistant:
			out = append(out, assistantMessage(m))
		case model.RoleTool:
			out = append(out, openai.ToolMessage(m.Content, m.ToolCallID))
		}
	}
	return out
}

func assistantMessage(m model.Message) openai.ChatCompletionMessageParamUnion {
	p := openai.ChatCompletionAssistantMessageParam{}
	if m.Content != "" {
		p.Content = openai.ChatCompletionAssistantMessageParamContentUnion{
			OfString: param.NewOpt(m.Content),
		}
	}
	for _, tc := range m.ToolCalls {
		p.ToolCalls = append(p.ToolCalls, openai.ChatCompletionMessageToolCallParam{
			ID: tc.ID,
			Function: openai.ChatCompletionMessageToolCallFunctionParam{
				Name:      tc.Name,
				Arguments: tc.Args,
			},
		})
	}
	return openai.ChatCompletionMessageParamUnion{OfAssistant: &p}
}

func toSDKTools(specs []ToolSpec) []openai.ChatCompletionToolParam {
	out := make([]openai.ChatCompletionToolParam, 0, len(specs))
	for _, s := range specs {
		out = append(out, openai.ChatCompletionToolParam{
			Function: shared.FunctionDefinitionParam{
				Name:        s.Name,
				Description: param.NewOpt(s.Description),
				Parameters:  shared.FunctionParameters(s.Parameters),
			},
		})
	}
	return out
}

// Model 返回当前使用的模型名，供日志与 `/help` 展示。
func (c *Client) Model() string { return c.model }
