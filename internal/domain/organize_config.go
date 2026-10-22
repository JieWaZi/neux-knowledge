package types

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// OrganizeConfigDigest pins library Wiki/index configuration, the rules digest, and stage prompt versions used by an organization job.
func OrganizeConfigDigest(lib *Library, rulesSHA256, promptVersion string) string {
	if lib == nil {
		return ""
	}
	cfg := lib.WikiConfig
	if cfg == nil {
		cfg = &WikiConfig{}
	}
	payload := struct {
		Kind               string           `json:"kind"`
		WikiLanguage       string           `json:"wikiLanguage"`
		WikiConfig         WikiConfig       `json:"wikiConfig"`
		IndexingStrategy   IndexingStrategy `json:"indexingStrategy"`
		EmbeddingModelID   string           `json:"embeddingModelId"`
		RulesSHA256        string           `json:"rulesSha256"`
		StagePromptVersion string           `json:"stagePromptVersion"`
	}{
		Kind:               lib.Kind,
		WikiLanguage:       lib.WikiLanguage,
		WikiConfig:         *cfg,
		IndexingStrategy:   lib.IndexingStrategy,
		EmbeddingModelID:   lib.EmbeddingModelID,
		RulesSHA256:        rulesSHA256,
		StagePromptVersion: promptVersion,
	}
	raw, _ := json.Marshal(payload)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// ValidateOrganizeLibrary checks whether library configuration permits accepting a Wiki organization job.
func ValidateOrganizeLibrary(lib *Library) error {
	if lib == nil {
		return &Error{Code: ErrInvalid, Operation: "job.accept", Detail: "library is required"}
	}
	if lib.Kind != "general" || !lib.IndexingStrategy.HasAnyIndexing() {
		return &Error{Code: ErrInvalid, Operation: "job.accept", Detail: "general library requires at least one supported indexing strategy"}
	}
	if lib.IndexingStrategy.VectorEnabled && lib.EmbeddingModelID == "" {
		return &Error{Code: ErrInvalid, Operation: "job.accept", Detail: "vector indexing requires embeddingModelId"}
	}
	return nil
}
