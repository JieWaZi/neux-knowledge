package wiki

import "strings"

// TableRowContext 保存按现有保护规则识别的原始数据行及其列标题，不把旧摘要转换为证据。
type TableRowContext struct {
	Identity string // 与表格保护一致的正规化行身份。
	Header   string // 当前表格的原始列标题和分隔行，缺失时为空。
	Row      string // 未改写的原始数据行。
}

// MatchingTableRows 从完整文本提取指定身份的原始表行；identities 为 nil 时提取全部数据行。
func MatchingTableRows(content string, identities []string) []TableRowContext {
	wanted := map[string]bool{}
	for _, id := range identities {
		wanted[id] = true
	}
	lines := strings.Split(content, "\n")
	var rows []TableRowContext
	header := ""
	for i, line := range lines {
		if !isWikiWriteTableRow(line) {
			header = ""
			continue
		}
		if isWikiWriteDelimiterRow(line) {
			continue
		}
		if i+1 < len(lines) && isWikiWriteTableRow(lines[i+1]) && isWikiWriteDelimiterRow(lines[i+1]) {
			header = strings.TrimSpace(line) + "\n" + strings.TrimSpace(lines[i+1])
			continue
		}
		id := wikiWriteRowIdentity(line)
		if id != "" && (identities == nil || wanted[id]) {
			rows = append(rows, TableRowContext{Identity: id, Header: header, Row: strings.TrimSpace(line)})
		}
	}
	return rows
}
