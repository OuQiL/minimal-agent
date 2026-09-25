package tool

import (
	"fmt"
	"strings"
)

// 结果截断的默认上限。
//
// 真实搜索服务的 count 上限为 50，开启摘要素后单条结果可达数千字符。
// 原始响应若直接回填，一次搜索就能撑爆上下文，还会连带触发压缩。
const (
	DefaultMaxItemChars  = 300
	DefaultMaxTotalChars = 2000
)

// Budget 描述一次工具输出的大小上限。
type Budget struct {
	MaxItemChars  int
	MaxTotalChars int
}

// DefaultBudget 返回默认的截断预算。
func DefaultBudget() Budget {
	return Budget{MaxItemChars: DefaultMaxItemChars, MaxTotalChars: DefaultMaxTotalChars}
}

// Truncate 把若干条文本拼成一段输出，并施加单条与总量双重上限。
//
// 返回值第二项报告是否发生了截断。截断必须被标注：模型需要知道信息不完整，
// 否则会把被截断的内容当作全部事实。
func (b Budget) Truncate(items []string) (string, bool) {
	maxItem := b.MaxItemChars
	if maxItem <= 0 {
		maxItem = DefaultMaxItemChars
	}
	maxTotal := b.MaxTotalChars
	if maxTotal <= 0 {
		maxTotal = DefaultMaxTotalChars
	}

	truncated := false
	parts := make([]string, 0, len(items))

	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if r := []rune(item); len(r) > maxItem {
			item = string(r[:maxItem]) + "……（该条内容已截断）"
			truncated = true
		}
		parts = append(parts, item)
	}

	joined := strings.Join(parts, "\n\n")
	if r := []rune(joined); len(r) > maxTotal {
		joined = string(r[:maxTotal])
		truncated = true
		joined += "\n\n……（结果总量超出上限，其余内容已省略）"
	}
	return joined, truncated
}

// externalOpen 与 externalClose 是外部内容的边界标记。
//
// 真实搜索结果属于不可信的外部输入，可能携带「忽略之前的指令」这类文本。
// 用标记把它与对话本身隔开，配合系统提示中的声明，构成一道纵深防御。
const (
	externalOpen  = "<external-content"
	externalClose = "</external-content>"
)

// WrapExternal 把来自外部服务的内容包裹在带来源标注的边界标记内。
func WrapExternal(source, content string) string {
	return fmt.Sprintf("%s source=%q>\n%s\n%s", externalOpen, source, content, externalClose)
}
