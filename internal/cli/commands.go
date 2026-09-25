// Package cli 实现命令行交互界面。
package cli

import "strings"

// CommandKind 是斜杠命令的种类。
type CommandKind int

// 支持的命令。
const (
	CmdNone CommandKind = iota
	CmdNew
	CmdList
	CmdSwitch
	CmdHistory
	CmdTrace
	CmdTools
	CmdCompact
	CmdHelp
	CmdQuit
	CmdUnknown
)

// Command 是一条解析后的命令。
type Command struct {
	Kind CommandKind
	// Arg 是命令的参数，例如 /switch 的会话标识、/new 的标题。
	Arg string
}

// CommandNames 是命令名到种类的映射，供解析与帮助文本共用。
var CommandNames = map[string]CommandKind{
	"new":     CmdNew,
	"list":    CmdList,
	"switch":  CmdSwitch,
	"history": CmdHistory,
	"trace":   CmdTrace,
	"tools":   CmdTools,
	"compact": CmdCompact,
	"help":    CmdHelp,
	"quit":    CmdQuit,
	"exit":    CmdQuit,
}

// ParseCommand 解析一行输入。
//
// 以 "/" 开头的一律按命令处理，不做关键词映射——否则无法区分
// 「用户想执行命令」与「用户真的想聊一段斜杠开头的文本」。
// 反过来，不以 "/" 开头的一律当作对话内容，即使它长得像命令。
func ParseCommand(input string) Command {
	trimmed := strings.TrimSpace(input)
	if !strings.HasPrefix(trimmed, "/") {
		return Command{Kind: CmdNone}
	}

	body := strings.TrimPrefix(trimmed, "/")
	if body == "" {
		return Command{Kind: CmdUnknown, Arg: ""}
	}

	name, arg, _ := strings.Cut(body, " ")
	kind, ok := CommandNames[strings.ToLower(strings.TrimSpace(name))]
	if !ok {
		return Command{Kind: CmdUnknown, Arg: name}
	}
	return Command{Kind: kind, Arg: strings.TrimSpace(arg)}
}

// HelpText 返回命令帮助。
func HelpText() string {
	return strings.TrimRight(`
可用命令：
  /new [标题]      新建会话并切换过去
  /list            列出全部会话
  /switch <标识>   切换到指定会话
  /history [n]     查看当前会话最近 n 条消息（默认 20）
  /trace [n]       查看当前会话的工具调用记录（默认 10）
  /tools           列出已注册的工具及其参数
  /compact         手动触发一次上下文压缩
  /help            显示本帮助
  /quit            退出

直接输入文字即为对话内容。
`, "\n")
}
