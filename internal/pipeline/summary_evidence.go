package pipeline

import (
	"strings"
	"unicode/utf8"

	"github.com/JieWaZi/neux-knowledge/internal/wiki"
)

// wikiSummarySourceInput 在原预算内优先保留全文分布的完整表行，再补普通正文的首中尾样本。
func wikiSummarySourceInput(content string) string {
	if utf8.RuneCountInString(content) <= maxContentForWiki {
		return content
	}
	const omission = "\n[Source excerpts; unshown text and table rows were omitted.]\n"
	rows := wiki.MatchingTableRows(content, nil)
	selected := make([]bool, len(rows))
	budget := maxContentForWiki / 2
	// 先覆盖两端，再逐层取区间中点；前部大表不能独占有限的表格预算。
	order := []int{}
	if len(rows) > 0 {
		order = append(order, 0)
	}
	if len(rows) > 1 {
		order = append(order, len(rows)-1)
	}
	ranges := [][2]int{{1, len(rows) - 2}}
	for i := 0; i < len(ranges); i++ {
		lo, hi := ranges[i][0], ranges[i][1]
		if lo > hi {
			continue
		}
		mid := lo + (hi-lo)/2
		order = append(order, mid)
		ranges = append(ranges, [2]int{lo, mid - 1}, [2]int{mid + 1, hi})
	}
	for _, i := range order {
		size := utf8.RuneCountInString(rows[i].Header + "\n" + rows[i].Row + "\n")
		if size <= budget {
			selected[i] = true
			budget -= size
		}
	}
	var tables strings.Builder
	for i, row := range rows {
		if selected[i] {
			tables.WriteString(row.Header + "\n" + row.Row + "\n")
		}
	}
	tableText := tables.String()
	proseBudget := maxContentForWiki - utf8.RuneCountInString(tableText) - utf8.RuneCountInString(omission)
	return sampleLongContent(content, proseBudget) + omission + tableText
}
