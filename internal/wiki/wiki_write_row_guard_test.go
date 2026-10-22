package wiki

import (
	"github.com/stretchr/testify/require"
	"testing"
)

// ledger is the shape that regressed in production: the rows ARE the data, so a
// rewrite that drops some of them shrinks the page. The first column names the
// holder, and the guard keys a row on exactly that column.
func ledger(title string, holders ...string) string {
	body := "# " + title + "\n\n## 持证人员明细\n\n" +
		"| 姓名 | 证书编号 | 有效期止 |\n| --- | --- | --- |\n"
	for _, holder := range holders {
		body += "| " + holder + " | CERT-2020 | 2099-01-01 |\n"
	}
	return body
}

func TestWikiWriteMissingRowIdentities(t *testing.T) {
	full := ledger("PMP", "张三", "李四", "王五")
	cases := []struct {
		// name identifies a distinct test scenario.
		name string
		// existing is the original body saved before the write.
		existing string
		// rewritten is the candidate rewritten body.
		rewritten string
		// want is the expected result for this behavior scenario.
		want []string
	}{
		{"identical body keeps every row", full, full, nil},
		{
			"reordered rows are all still there",
			full, ledger("PMP", "王五", "张三", "李四"), nil,
		},
		{
			"emphasis, padding and case changes are not lost rows",
			full, ledger("PMP", "**张三**", " 李四 ", "王五"), nil,
		},
		{
			"a renamed header is not a lost row",
			full,
			"# PMP 证书\n\n## 持证人员明细\n\n" +
				"| 持证人 | 证书编号 | 有效期至 |\n| --- | --- | --- |\n" +
				"| 张三 | CERT-2020 | 2099-01-01 |\n| 李四 | CERT-2020 | 2099-01-01 |\n" +
				"| 王五 | CERT-2020 | 2099-01-01 |\n",
			nil,
		},
		{
			"an added row is not a loss",
			full, ledger("PMP", "张三", "李四", "王五", "赵六"), nil,
		},
		{"prose-only pages have nothing to guard", "just prose", "shorter prose", nil},
		{
			"one dropped row is reported",
			full, ledger("PMP", "张三", "李四"),
			[]string{"王五"},
		},
		{
			"a truncated rewrite reports every missing row",
			full, ledger("PMP", "张三"),
			[]string{"李四", "王五"},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			require.Equal(t, testCase.want,
				wikiWriteMissingRowIdentities(testCase.existing, testCase.rewritten))
		})
	}
}

// Duplicate identities are compared as a set, not as a multiset: two stored
// rows whose first non-empty cell is the same are ONE identity, so a rewrite
// that keeps it once has kept it — merging them into one row is not a loss.
// Only when the identity no longer appears at all is it reported, and then
// once, not once per stored row.
func TestWikiWriteMissingRowIdentitiesComparesIdentitiesAsASet(t *testing.T) {
	existing := ledger("证书", "张三", "张三")
	require.Empty(t, wikiWriteMissingRowIdentities(existing, ledger("证书", "张三")))
	require.Equal(t, []string{"张三"}, wikiWriteMissingRowIdentities(existing, ledger("证书")))
}

// The ratio denominator is the number of DISTINCT identities: repeating a
// holder on the stored page must not dilute the share of identities lost, or a
// page full of duplicates could lose whole subjects under the threshold.
func TestWikiWriteUniqueRowIdentityCountCountsEachIdentityOnce(t *testing.T) {
	require.Equal(t, 0, wikiWriteUniqueRowIdentityCount("just prose"))
	require.Equal(t, 1, wikiWriteUniqueRowIdentityCount(ledger("证书", "张三", "张三")))
	repeated := ledger("证书", "张三", "张三", "李四", "王五", "李四")
	require.Equal(t, 3, wikiWriteUniqueRowIdentityCount(repeated))
}

// The threshold boundary is exact: a loss of exactly 20% is allowed, anything
// above it is not, and an empty stored identity set has no ratio at all.
func TestWikiWriteRowIdentityLossRatioBoundary(t *testing.T) {
	require.Equal(t, 0.0, wikiWriteRowIdentityLossRatio(2, 0), "an empty identity set cannot lose anything")
	require.LessOrEqual(t, wikiWriteRowIdentityLossRatio(1, 5), wikiWriteIngestRowIdentityLossLimit)
	require.Greater(t, wikiWriteRowIdentityLossRatio(1, 4), wikiWriteIngestRowIdentityLossLimit)
}

// The row above a `| --- |` delimiter names columns rather than an entity, and
// every table in the page is surveyed: a rewrite that keeps one table intact
// while emptying another is still a truncation.
func TestWikiWriteTableRowIdentitiesSkipsHeadersAndCoversEveryTable(t *testing.T) {
	content := "| 姓名 | 证书编号 |\n| --- | --- |\n| 张三 | CERT-1 |\n\nprose\n\n" +
		"| 项目 | 金额 |\n| --- | --- |\n| 差旅 | 100 |\n| 会务 | 200 |\n"
	require.Equal(t, []string{"张三", "差旅", "会务"}, wikiWriteTableRowIdentities(content))

	rewritten := "| 姓名 | 证书编号 |\n| --- | --- |\n| 张三 | CERT-1 |\n\nprose\n\n" +
		"| 项目 | 金额 |\n| --- | --- |\n| 差旅 | 100 |\n"
	require.Equal(t, []string{"会务"}, wikiWriteMissingRowIdentities(content, rewritten))
}

// A row whose first cell is empty still names an entity in its next non-empty
// cell, so it is counted rather than silently ignored.
func TestWikiWriteRowIdentityFallsBackToTheNextNonEmptyCell(t *testing.T) {
	content := "| 姓名 | 证书编号 |\n| --- | --- |\n|  | CERT-1 |\n"
	require.Equal(t, []string{"cert-1"}, wikiWriteTableRowIdentities(content))
}

// prose that merely mentions a pipe is not a table, so it cannot be read as one
// losing rows.
func TestWikiWriteTableRowIdentitiesIgnoresLonePipesInProse(t *testing.T) {
	require.Empty(t, wikiWriteTableRowIdentities("a | b\n\nc"))
}
