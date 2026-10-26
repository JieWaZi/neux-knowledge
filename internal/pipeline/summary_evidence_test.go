package pipeline

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestSummaryInputKeepsLateTables 验证长摘要输入显式标注省略，预算内仍覆盖前缀之外的原始表格。
func TestSummaryInputKeepsLateTables(t *testing.T) {
	table := "| 条件 | 行为 |\n|---|---|\n| 尾部身份 | 当前值 |\n"
	body := strings.Repeat("背景文字。", 10000) + "\n" + table + strings.Repeat("附录文字。", 10000)
	got := wikiSummarySourceInput(body)
	if !strings.Contains(got, "| 尾部身份 | 当前值 |") || !strings.Contains(got, "omitted") || utf8.RuneCountInString(got) > maxContentForWiki {
		t.Fatalf("long summary input lost table or exceeded budget: length=%d", utf8.RuneCountInString(got))
	}
}

// TestSummaryTablesSpreadAcrossWholeSource 防止前部大表耗尽表格预算后再次饿死后部不同表格。
func TestSummaryTablesSpreadAcrossWholeSource(t *testing.T) {
	early := "| 名称 | 内容 |\n|---|---|\n" + strings.Repeat("| 早期身份 | 长表其他值 |\n", 3000)
	late := "\n| 名称 | 内容 |\n|---|---|\n| 后部单独主题 | 必须看到 |\n"
	input := wikiSummarySourceInput(early + late + strings.Repeat("附录文字。", 20000))
	if !strings.Contains(input, "| 后部单独主题 | 必须看到 |") || utf8.RuneCountInString(input) > maxContentForWiki {
		t.Fatalf("large early table starved later source table: length=%d", utf8.RuneCountInString(input))
	}
}
