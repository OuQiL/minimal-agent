package cli

import (
	"testing"
	"time"
)

func TestHumanizeAt(t *testing.T) {
	// 固定的「现在」，让断言不受运行时影响。
	now := time.Date(2026, 9, 25, 15, 30, 0, 0, time.Local)

	cases := []struct {
		name string
		in   time.Time
		want string
	}{
		{"零值", time.Time{}, "未知时间"},
		{"几秒前", now.Add(-10 * time.Second), "刚刚"},
		{"不到一分钟", now.Add(-59 * time.Second), "刚刚"},
		{"几分钟前", now.Add(-5 * time.Minute), "5 分钟前"},
		{"几十分钟前", now.Add(-59 * time.Minute), "59 分钟前"},
		{"今天稍早", now.Add(-2 * time.Hour), "今天 13:30"},
		{"今天更早（跨过整点）", time.Date(2026, 9, 25, 3, 5, 0, 0, time.Local), "今天 03:05"},
		{"昨天", time.Date(2026, 9, 24, 22, 10, 0, 0, time.Local), "昨天 22:10"},
		{"本周内", time.Date(2026, 9, 20, 9, 0, 0, 0, time.Local), "9月20日"},
		{"今年更早", time.Date(2026, 1, 3, 9, 0, 0, 0, time.Local), "1月3日"},
		{"去年", time.Date(2025, 12, 31, 9, 0, 0, 0, time.Local), "2025年12月31日"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := humanizeAt(tc.in, now); got != tc.want {
				t.Errorf("humanizeAt(%v) = %q，期望 %q", tc.in, got, tc.want)
			}
		})
	}
}

// 一小时的边界两侧分属不同分支，值得钉住。
func TestHumanizeAt_HourBoundary(t *testing.T) {
	now := time.Date(2026, 9, 25, 15, 0, 0, 0, time.Local)

	if got := humanizeAt(now.Add(-59*time.Minute-59*time.Second), now); got != "59 分钟前" {
		t.Errorf("略少于一小时 = %q，期望「59 分钟前」", got)
	}
	if got := humanizeAt(now.Add(-time.Hour), now); got != "今天 14:00" {
		t.Errorf("整一小时 = %q，期望「今天 14:00」", got)
	}
}

// 时间以 UTC 存储，展示时须转成本地时区——否则用户看到的会是相差数小时的时间。
func TestHumanizeAt_ConvertsToLocalTime(t *testing.T) {
	now := time.Now()
	local := time.Date(now.Year(), now.Month(), now.Day(), 10, 0, 0, 0, time.Local)

	got := humanizeAt(local.UTC(), now)
	if got != "今天 10:00" {
		t.Errorf("UTC 存储的时间应转成本地时区展示，实际 %q", got)
	}
}

func TestHumanTime_ZeroValueIsReadable(t *testing.T) {
	// 从数据库读出的时间解析失败时会是零值，不能显示成 0001 年。
	if got := humanTime(time.Time{}); got != "未知时间" {
		t.Errorf("零值时间 = %q", got)
	}
}
