package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"minimal-agent/internal/store"
)

// TodoTool 在当前会话内新增或查看待办事项。
//
// 数据按会话隔离：当前会话标识从 context 取得（见 WithSessionID），
// 因此同一个工具实例在不同会话中操作的是各自独立的待办列表。
type TodoTool struct {
	store *store.Store
}

// NewTodo 创建待办工具。
func NewTodo(s *store.Store) *TodoTool { return &TodoTool{store: s} }

func (t *TodoTool) Name() string { return "todo" }

func (t *TodoTool) Description() string {
	return "记录或查看待办事项。action 为 add 时新增一条待办，为 list 时列出当前全部待办。" +
		"待办数据按会话隔离，只在当前会话内可见。"
}

func (t *TodoTool) Aliases() []string { return []string{"todos", "task", "remember"} }

func (t *TodoTool) Parameters() Schema {
	return Schema{
		Type: "object",
		Properties: map[string]Property{
			"action": {
				Type:        "string",
				Description: "要执行的操作",
				Enum:        []string{"add", "list"},
			},
			"content": {
				Type:        "string",
				Description: "待办内容。当 action 为 add 时必填。",
			},
		},
		Required: []string{"action"},
	}
}

func (t *TodoTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Action  string `json:"action"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("参数解析失败：%v", err)
	}

	sessionID := SessionIDFrom(ctx)
	if sessionID == "" {
		return "", fmt.Errorf("无法确定当前会话")
	}

	switch in.Action {
	case "add":
		content := strings.TrimSpace(in.Content)
		if content == "" {
			return "", fmt.Errorf("action 为 add 时必须提供 content")
		}
		item, err := t.store.AddTodo(sessionID, content)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("已记录待办（编号 %d）：%s", item.ID, item.Content), nil

	case "list":
		items, err := t.store.Todos(sessionID)
		if err != nil {
			return "", err
		}
		if len(items) == 0 {
			return "当前会话没有待办事项。", nil
		}
		var b strings.Builder
		fmt.Fprintf(&b, "当前会话共有 %d 条待办：\n", len(items))
		for i, it := range items {
			mark := " "
			if it.Done {
				mark = "x"
			}
			fmt.Fprintf(&b, "%d. [%s] %s\n", i+1, mark, it.Content)
		}
		return strings.TrimRight(b.String(), "\n"), nil

	default:
		return "", fmt.Errorf("不支持的 action %q，只支持 add 或 list", in.Action)
	}
}
