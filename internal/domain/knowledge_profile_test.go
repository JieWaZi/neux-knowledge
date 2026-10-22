package types

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKnowledgeProfileNormalizeBoundsAndDedupes(t *testing.T) {
	p := (&KnowledgeProfile{
		Gist: "  A   guide to   Kubernetes networking  ",
		Topics: []string{
			"Kubernetes", " kubernetes ", "Networking.", "", "CNI", "Ingress", "Service mesh", "extra",
		},
		DocType:         " user manual ",
		TypicalQuestion: "How do pods talk to each other?",
	}).Normalize()
	require.NotNil(t, p)
	assert.Equal(t, "A guide to Kubernetes networking", p.Gist)
	assert.Equal(t, []string{"Kubernetes", "Networking.", "CNI", "Ingress", "Service mesh"}, p.Topics,
		"case-insensitive duplicates collapse and the list is capped at five")
	assert.Equal(t, "user manual", p.DocType)
}

func TestKnowledgeProfileNormalizeEmptyBecomesNil(t *testing.T) {
	assert.Nil(t, (&KnowledgeProfile{Topics: []string{"  ", "..."}}).Normalize())
	assert.Nil(t, (*KnowledgeProfile)(nil).Normalize())
	assert.True(t, (*KnowledgeProfile)(nil).IsEmpty())
}

func TestTopicKeyFoldsCaseWhitespaceAndPunctuation(t *testing.T) {
	assert.Equal(t, "kubernetes", TopicKey(" Kubernetes. "))
	assert.Equal(t, "service mesh", TopicKey("Service   Mesh!"))
	assert.Equal(t, "", TopicKey("..."))
	assert.NotEqual(t, TopicKey("K8s"), TopicKey("Kubernetes"), "synonyms are left to the model")
}

func TestKnowledgeProfileCloneIsIndependent(t *testing.T) {
	assert.Nil(t, (*KnowledgeProfile)(nil).Clone())
	src := &KnowledgeProfile{Gist: "g", Topics: []string{"a", "b"}}
	dst := src.Clone()
	dst.Topics[0] = "changed"
	assert.Equal(t, "a", src.Topics[0])
	assert.Equal(t, "g", dst.Gist)
}

func TestKnowledgeProfileScanRoundTrip(t *testing.T) {
	src := KnowledgeProfile{Gist: "g", Topics: []string{"a"}, DocType: "t", TypicalQuestion: "q"}
	raw, err := src.Value()
	require.NoError(t, err)
	var dst KnowledgeProfile
	require.NoError(t, dst.Scan(raw))
	assert.Equal(t, src, dst)
	var untouched KnowledgeProfile
	require.NoError(t, untouched.Scan(nil))
	assert.True(t, untouched.IsEmpty())
}
