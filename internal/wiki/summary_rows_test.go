package wiki

import "testing"

// TestMatchingTableRowsPreservesHeaderAndCurrentCells 保证按保护规则匹配行身份，不将正文提及或其他列误当成该行。
func TestMatchingTableRowsPreservesHeaderAndCurrentCells(t *testing.T) {
	body := "正文提及张三。\n| 姓名 | 当前编号 |\n|---|---|\n| **张三** | NEW-2026 |\n| 李四 | 张三 |\n\n| 人员 | 当前值 |\n|---|---|\n| 张三 | 现状 |\n"
	rows := MatchingTableRows(body, []string{"张三"})
	if len(rows) != 2 || rows[0].Header != "| 姓名 | 当前编号 |\n|---|---|" || rows[0].Row != "| **张三** | NEW-2026 |" || rows[1].Row != "| 张三 | 现状 |" {
		t.Fatalf("wrong row selection: %+v", rows)
	}
	if all := MatchingTableRows(body, nil); len(all) != 3 {
		t.Fatalf("headers counted as data rows: %+v", all)
	}
}
