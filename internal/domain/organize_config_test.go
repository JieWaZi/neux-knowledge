package types

import "testing"

func TestOrganizeConfigDigestChangesWithWikiConfig(t *testing.T) {
	base := &Library{Kind: "general", WikiLanguage: "zh-CN", IndexingStrategy: DefaultGeneralLibraryIndexingStrategy(), WikiConfig: &WikiConfig{ExtractionGranularity: WikiExtractionStandard}}
	first := OrganizeConfigDigest(base, "rules-a", "prompt-v1")
	base.WikiConfig.ExtractionGranularity = WikiExtractionFocused
	second := OrganizeConfigDigest(base, "rules-a", "prompt-v1")
	if first == second {
		t.Fatal("granularity change must change digest")
	}
}

func TestValidateOrganizeLibraryRequiresEmbeddingForVector(t *testing.T) {
	lib := &Library{Kind: "general", IndexingStrategy: IndexingStrategy{WikiEnabled: true, VectorEnabled: true}}
	if err := ValidateOrganizeLibrary(lib); err == nil {
		t.Fatal("vector without embedding model must fail")
	}
}
