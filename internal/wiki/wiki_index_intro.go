package wiki

import (
	"regexp"
	"strings"
)

var indexLeadingTitle = regexp.MustCompile(`^ {0,3}#([ \t]+|$)`)
var indexSetextTitle = regexp.MustCompile(`^ {0,3}=+[ \t]*$`)

// NormalizeIndexIntro fixes the index's opening level-one heading while preserving the overview, directory, and code bodies.
// Used only for index generation and read-only projections; ordinary Wiki titles and persisted records are not modified during reads.
func NormalizeIndexIntro(content string) string {
	lines := strings.Split(strings.TrimRight(content, "\r\n"), "\n")
	for len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
		lines = lines[1:]
	}
	if len(lines) == 0 {
		return "# Wiki Index"
	}
	if indexLeadingTitle.MatchString(lines[0]) {
		lines[0] = "# Wiki Index"
		return strings.Join(lines, "\n")
	}
	if len(lines) > 1 && !strings.HasPrefix(lines[0], "    ") && !strings.HasPrefix(lines[0], "\t") && indexSetextTitle.MatchString(lines[1]) {
		return strings.Join(append([]string{"# Wiki Index"}, lines[2:]...), "\n")
	}
	return "# Wiki Index\n\n" + strings.Join(lines, "\n")
}
