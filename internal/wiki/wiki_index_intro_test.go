package wiki

import "testing"

func TestNormalizeIndexIntroOnlyReplacesLeadingTitle(t *testing.T) {
	for _, tc := range []struct {
		name  string // Meaning of the test case.
		input string // Model-generated or historical body.
		want  string // Complete body after fixing the heading.
	}{
		{"dynamic", "\n# 旧标题\n\n范围与条件。\n\n## 目录\n[[concept/a]]", "# Wiki Index\n\n范围与条件。\n\n## 目录\n[[concept/a]]"},
		{"setext", "旧标题\n====\n\n范围。", "# Wiki Index\n\n范围。"},
		{"no-heading", "范围。\n\n## 更多\n保留", "# Wiki Index\n\n范围。\n\n## 更多\n保留"},
		{"code", "```md\n# 示例标题\n```", "# Wiki Index\n\n```md\n# 示例标题\n```"},
		{"indented-code", "    # 示例标题\n    保留代码", "# Wiki Index\n\n    # 示例标题\n    保留代码"},
		{"fixed", "# Wiki Index\n\n范围。", "# Wiki Index\n\n范围。"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := NormalizeIndexIntro(tc.input); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}
