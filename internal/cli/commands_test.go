package cli_test

import (
	"strings"
	"testing"

	"minimal-agent/internal/cli"
)

func TestParseCommand_RecognizesAllCommands(t *testing.T) {
	cases := []struct {
		input string
		kind  cli.CommandKind
		arg   string
	}{
		{"/new", cli.CmdNew, ""},
		{"/new 我的会话", cli.CmdNew, "我的会话"},
		{"/list", cli.CmdList, ""},
		{"/switch sess_abc123", cli.CmdSwitch, "sess_abc123"},
		{"/history", cli.CmdHistory, ""},
		{"/history 5", cli.CmdHistory, "5"},
		{"/trace", cli.CmdTrace, ""},
		{"/trace 3", cli.CmdTrace, "3"},
		{"/tools", cli.CmdTools, ""},
		{"/compact", cli.CmdCompact, ""},
		{"/help", cli.CmdHelp, ""},
		{"/quit", cli.CmdQuit, ""},
		{"/exit", cli.CmdQuit, ""},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			got := cli.ParseCommand(tc.input)
			if got.Kind != tc.kind {
				t.Errorf("命令种类 = %v，期望 %v", got.Kind, tc.kind)
			}
			if got.Arg != tc.arg {
				t.Errorf("参数 = %q，期望 %q", got.Arg, tc.arg)
			}
		})
	}
}

func TestParseCommand_PlainTextIsChat(t *testing.T) {
	cases := []string{
		"你好",
		"帮我查一下北京天气",
		"解释一下 http://example.com 这个链接", // 含斜杠但不在开头
		"1 + 1 等于几",
		"  前面有空格的一句话",
		"list", // 与命令同名但没有斜杠，应当作对话内容
	}
	for _, in := range cases {
		t.Run(in, func(t *testing.T) {
			if got := cli.ParseCommand(in); got.Kind != cli.CmdNone {
				t.Errorf("%q 应被当作对话内容，实际解析为 %v", in, got.Kind)
			}
		})
	}
}

func TestParseCommand_TolerantOfFormatting(t *testing.T) {
	cases := []struct {
		input string
		kind  cli.CommandKind
		arg   string
	}{
		{"  /list  ", cli.CmdList, ""},
		{"/LIST", cli.CmdList, ""},
		{"/Switch sess_x", cli.CmdSwitch, "sess_x"},
		{"/new   多个    空格", cli.CmdNew, "多个    空格"},
		{"/switch  sess_x  ", cli.CmdSwitch, "sess_x"},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			got := cli.ParseCommand(tc.input)
			if got.Kind != tc.kind {
				t.Errorf("命令种类 = %v，期望 %v", got.Kind, tc.kind)
			}
			if got.Arg != tc.arg {
				t.Errorf("参数 = %q，期望 %q", got.Arg, tc.arg)
			}
		})
	}
}

func TestParseCommand_Unknown(t *testing.T) {
	for _, in := range []string{"/nonsense", "/", "/list-extra-arg-not-a-command"} {
		t.Run(in, func(t *testing.T) {
			got := cli.ParseCommand(in)
			if got.Kind != cli.CmdUnknown {
				t.Errorf("%q 应解析为未知命令，实际 %v", in, got.Kind)
			}
		})
	}
}

func TestHelpText_ListsEveryCommand(t *testing.T) {
	help := cli.HelpText()
	// 帮助文本与实际支持的命令必须一致，否则用户会被误导。
	for name := range cli.CommandNames {
		if name == "exit" {
			continue // exit 是 quit 的同义写法，不必单独列出
		}
		if !strings.Contains(help, "/"+name) {
			t.Errorf("帮助文本中缺少命令 /%s", name)
		}
	}
}
