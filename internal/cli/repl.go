package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"

	"minimal-agent/internal/agent"
	"minimal-agent/internal/contextmgr"
	"minimal-agent/internal/model"
	"minimal-agent/internal/session"
	"minimal-agent/internal/store"
	"minimal-agent/internal/tool"
)

const (
	ansiReset = "\033[0m"
	ansiDim   = "\033[2m"
	ansiCyan  = "\033[36m"
	ansiBold  = "\033[1m"
)

// REPL 是命令行交互界面。
//
// 「窗口」在这里以会话命令模拟：/new 开一个，/switch 切回去，
// 每个会话的对话历史与待办彼此独立。
type REPL struct {
	loop      *agent.Loop
	manager   *session.Manager
	store     *store.Store
	registry  *tool.Registry
	compactor *contextmgr.Compactor

	in    io.Reader
	out   io.Writer
	color bool
	model string
}

// Options 汇总 REPL 的依赖。
type Options struct {
	Loop      *agent.Loop
	Manager   *session.Manager
	Store     *store.Store
	Registry  *tool.Registry
	Compactor *contextmgr.Compactor
	In        io.Reader
	Out       io.Writer
	Color     bool
	Model     string
}

// New 创建 REPL。
func New(o Options) *REPL {
	return &REPL{
		loop:      o.Loop,
		manager:   o.Manager,
		store:     o.Store,
		registry:  o.Registry,
		compactor: o.Compactor,
		in:        o.In,
		out:       o.Out,
		color:     o.Color,
		model:     o.Model,
	}
}

func (r *REPL) paint(code, s string) string {
	if !r.color {
		return s
	}
	return code + s + ansiReset
}

func (r *REPL) printf(format string, args ...any) {
	fmt.Fprintf(r.out, format, args...)
}

// Run 启动交互循环，直到用户退出或输入结束。
func (r *REPL) Run(ctx context.Context) error {
	sess, err := r.manager.Ensure()
	if err != nil {
		return err
	}

	r.printf("%s\n", r.paint(ansiBold, "最小可用 Agent"))
	r.printf("模型：%s    当前会话：%s\n", r.model, sessionLabel(sess))
	r.printf("输入 /help 查看命令，/quit 退出。\n\n")

	scanner := bufio.NewScanner(r.in)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for {
		// 提示符只显示用户能直接使用的编号，不显示内部标识。
		r.printf("%s ", r.paint(ansiCyan, fmt.Sprintf("[%d]>", r.manager.CurrentNum())))
		if !scanner.Scan() {
			r.printf("\n")
			return scanner.Err()
		}

		input := strings.TrimSpace(scanner.Text())
		if input == "" {
			continue
		}

		cmd := ParseCommand(input)
		if cmd.Kind == CmdNone {
			if err := r.handleChat(ctx, input); err != nil {
				return err
			}
			continue
		}
		quit, err := r.handleCommand(ctx, cmd)
		if err != nil {
			r.printf("%s\n", r.paint(ansiDim, "错误："+err.Error()))
			continue
		}
		if quit {
			return nil
		}
	}
}

// handleChat 处理一次对话。
func (r *REPL) handleChat(ctx context.Context, input string) error {
	sessionID := r.manager.CurrentID()
	if sessionID == "" {
		if _, err := r.manager.Ensure(); err != nil {
			return err
		}
		sessionID = r.manager.CurrentID()
	}

	sink := agent.NewConsoleSink(r.out, r.color)
	ex, err := r.loop.Run(ctx, sessionID, input, sink)
	if err != nil {
		return err
	}
	if ex.Failed || ex.ReachedLimit {
		r.printf("%s\n", r.paint(ansiDim, ex.Answer))
	} else {
		r.printf("\n")
	}
	return nil
}

// handleCommand 处理一条斜杠命令。返回值报告是否应当退出。
func (r *REPL) handleCommand(ctx context.Context, cmd Command) (bool, error) {
	switch cmd.Kind {
	case CmdNew:
		sess, err := r.manager.New(cmd.Arg)
		if err != nil {
			return false, err
		}
		r.printf("已新建会话 %s，并切换过去。\n", sessionLabel(sess))
	case CmdList:
		return false, r.showList()
	case CmdSwitch:
		if cmd.Arg == "" {
			return false, fmt.Errorf("用法：/switch <编号>，例如 /switch 2；用 /list 查看全部会话")
		}
		sess, err := r.manager.Switch(cmd.Arg)
		if err != nil {
			return false, err
		}
		n, err := r.store.CountMessages(sess.ID)
		if err != nil {
			return false, err
		}
		r.printf("已切换到会话 %s，该会话有 %d 条历史消息。\n", sessionLabel(sess), n)
	case CmdHistory:
		return false, r.showHistory(parseLimit(cmd.Arg, 20))
	case CmdTrace:
		return false, r.showTrace(parseLimit(cmd.Arg, 10))
	case CmdTools:
		return false, r.showTools()
	case CmdCompact:
		return false, r.compact(ctx)
	case CmdHelp:
		r.printf("%s\n", HelpText())
	case CmdQuit:
		r.printf("再见。\n")
		return true, nil
	case CmdUnknown:
		return false, fmt.Errorf("未知命令 /%s，输入 /help 查看可用命令", cmd.Arg)
	}
	return false, nil
}

func (r *REPL) showList() error {
	sessions, err := r.manager.List()
	if err != nil {
		return err
	}
	if len(sessions) == 0 {
		r.printf("还没有任何会话。用 /new 创建一个。\n")
		return nil
	}

	current := r.manager.CurrentID()
	r.printf("共 %d 个会话：\n\n", len(sessions))

	for _, s := range sessions {
		mark := " "
		if s.ID == current {
			mark = "*"
		}
		title := s.Title
		if strings.TrimSpace(s.Summary) != "" {
			title += "（已压缩）"
		}
		// 编号醒目、标题常规、时间与开头内容暗色——扫一眼先看到编号，
		// 需要辨认时才看内容。
		r.printf("%s %s %s · %s\n",
			mark,
			r.paint(ansiBold, fmt.Sprintf("[%d]", s.Num)),
			title,
			r.paint(ansiDim, humanTime(s.UpdatedAt)))

		if p := strings.TrimSpace(s.Preview); p != "" {
			r.printf("      %s\n", r.paint(ansiDim, truncate(oneLine(p), 56)))
		} else {
			r.printf("      %s\n", r.paint(ansiDim, "（还没有对话）"))
		}
	}
	return nil
}

// humanTime 把时间压成人一眼能扫过的形式。
//
// 会话列表里精确到秒没有意义——用户想知道的是「哪个是刚聊过的」，
// 而不是它发生在 14:44:27 还是 14:44:31。
func humanTime(t time.Time) string { return humanizeAt(t, time.Now()) }

// humanizeAt 是 humanTime 的实现，把「现在」显式传入以便测试。
// 否则跨天边界（比如测试恰好在午夜前后运行）会让断言不稳定。
func humanizeAt(t, now time.Time) string {
	if t.IsZero() {
		return "未知时间"
	}
	t, now = t.Local(), now.Local()

	switch d := now.Sub(t); {
	case d < time.Minute:
		return "刚刚"
	case d < time.Hour:
		return fmt.Sprintf("%d 分钟前", int(d.Minutes()))
	case sameDay(t, now):
		return "今天 " + t.Format("15:04")
	case sameDay(t, now.AddDate(0, 0, -1)):
		return "昨天 " + t.Format("15:04")
	case t.Year() == now.Year():
		return t.Format("1月2日")
	default:
		return t.Format("2006年1月2日")
	}
}

func sameDay(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}

func (r *REPL) showHistory(limit int) error {
	sessionID := r.manager.CurrentID()
	msgs, err := r.store.Messages(sessionID, limit)
	if err != nil {
		return err
	}
	if len(msgs) == 0 {
		r.printf("当前会话还没有消息。\n")
		return nil
	}
	r.printf("当前会话最近 %d 条消息：\n", len(msgs))
	for _, m := range msgs {
		switch m.Role {
		case model.RoleUser:
			r.printf("  %s %s\n", r.paint(ansiBold, "[用户]"), oneLine(m.Content))
		case model.RoleAssistant:
			if strings.TrimSpace(m.Content) != "" {
				r.printf("  %s %s\n", r.paint(ansiCyan, "[助手]"), oneLine(m.Content))
			}
			for _, tc := range m.ToolCalls {
				r.printf("  %s 调用 %s，参数 %s\n", r.paint(ansiDim, "[工具调用]"), tc.Name, oneLine(tc.Args))
			}
			if strings.TrimSpace(m.Reasoning) != "" {
				r.printf("  %s %s\n", r.paint(ansiDim, "[思考]"),
					r.paint(ansiDim, truncate(oneLine(m.Reasoning), 80)))
			}
		case model.RoleTool:
			r.printf("  %s %s\n", r.paint(ansiDim, "[工具结果]"), r.paint(ansiDim, truncate(oneLine(m.Content), 120)))
		}
	}
	return nil
}

func (r *REPL) showTrace(limit int) error {
	sessionID := r.manager.CurrentID()
	traces, err := r.store.Traces(sessionID, limit)
	if err != nil {
		return err
	}
	if len(traces) == 0 {
		r.printf("当前会话还没有工具调用记录。\n")
		return nil
	}
	r.printf("当前会话最近 %d 条工具调用记录：\n", len(traces))
	for _, t := range traces {
		flags := ""
		if t.Alias {
			flags += " [别名命中]"
		}
		if t.Repeated {
			flags += " [重复调用]"
		}
		status := "成功"
		if t.Status == model.TraceError {
			status = "失败"
		}
		r.printf("  #%d %s  %s  耗时 %dms  参数 %s%s\n",
			t.ID, t.ToolName, status, t.DurationMS, truncate(oneLine(t.Args), 60), flags)
		if t.Error != "" {
			r.printf("      错误：%s\n", truncate(oneLine(t.Error), 120))
		}
		r.printf("      %s\n", r.paint(ansiDim, truncate(oneLine(t.Result), 120)))
	}
	return nil
}

func (r *REPL) showTools() error {
	tools := r.registry.All()
	r.printf("已注册 %d 个工具：\n", len(tools))
	for _, t := range tools {
		r.printf("\n  %s\n", r.paint(ansiBold, t.Name()))
		if aliases := t.Aliases(); len(aliases) > 0 {
			r.printf("    别名：%s\n", strings.Join(aliases, "、"))
		}
		r.printf("    说明：%s\n", t.Description())
		schema := t.Parameters()
		if len(schema.Properties) > 0 {
			required := make(map[string]bool, len(schema.Required))
			for _, n := range schema.Required {
				required[n] = true
			}
			var parts []string
			for name, prop := range schema.Properties {
				mark := ""
				if required[name] {
					mark = "（必填）"
				}
				parts = append(parts, fmt.Sprintf("%s: %s%s", name, prop.Type, mark))
			}
			slices.Sort(parts)
			r.printf("    参数：%s\n", strings.Join(parts, "，"))
		}
	}
	return nil
}

func (r *REPL) compact(ctx context.Context) error {
	sessionID := r.manager.CurrentID()
	changed, err := r.compactor.CompactNow(ctx, sessionID)
	if err != nil {
		return err
	}
	if !changed {
		r.printf("当前历史尚未超出保留窗口，无需压缩。\n")
		return nil
	}
	sess, err := r.store.GetSession(sessionID)
	if err != nil {
		return err
	}
	r.printf("已压缩历史。当前摘要：\n%s\n",
		r.paint(ansiDim, truncate(sess.Summary, 400)))
	return nil
}

// --- 小工具 ---

// sessionLabel 是会话在提示信息里的呈现形式：编号在前、标题在后。
// 内部标识不出现在任何面向用户的文案里——它太长，手输不现实。
func sessionLabel(s *model.Session) string {
	if s == nil {
		return "[?]"
	}
	return fmt.Sprintf("[%d] %s", s.Num, s.Title)
}

func parseLimit(arg string, def int) int {
	if arg == "" {
		return def
	}
	n, err := strconv.Atoi(arg)
	if err != nil || n <= 0 {
		return def
	}
	return n
}

// oneLine 把多行文本压成一行，便于列表展示。
func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.Join(strings.Fields(s), " ")
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "……"
}
