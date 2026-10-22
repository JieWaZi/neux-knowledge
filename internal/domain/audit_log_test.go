// Only the current library's public activity scope is retained; global administration and third-party storage actions are excluded.
package types

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAuditActionDotNamespaceConvention(t *testing.T) {
	all := []AuditAction{AuditActionKBCreated, AuditActionKBUpdated, AuditActionKBDeleted, AuditActionKnowledgeCreated, AuditActionKnowledgeUpdated, AuditActionKnowledgeDeleted, AuditActionKnowledgeReparseStarted, AuditActionKnowledgeParseCanceled, AuditActionWikiContentChanged}
	seen := map[AuditAction]bool{}
	for _, a := range all {
		s := string(a)
		area, event, ok := strings.Cut(s, ".")
		assert.True(t, ok, "action %q must contain exactly one dot separator", s)
		assert.NotEmpty(t, area, "action %q has empty area", s)
		assert.NotEmpty(t, event, "action %q has empty event", s)
		if seen[a] {
			t.Fatalf("collision: %q", a)
		}
		seen[a] = true
	}
}
