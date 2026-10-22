package types

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseSourcePurgeReviewWhitelistsIdentityMetadata(t *testing.T) {
	metadata := JSON(`{"private":"ORIGINAL_SECRET","sourcePurge":{"needsReview":true,"unavailableSourceIds":["aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","ORIGINAL_SECRET","aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"],"unavailableVersionIds":["bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"],"purgeIds":["cccccccccccccccccccccccccccccccc"],"excerpt":"ORIGINAL_SECRET"}}`)
	marker := ParseSourcePurgeReview(metadata)
	if marker == nil || !marker.NeedsReview || len(marker.UnavailableSourceIDs) != 1 || len(marker.UnavailableVersionIDs) != 1 || len(marker.PurgeIDs) != 1 {
		t.Fatalf("known identities not projected: %+v", marker)
	}
	data, err := json.Marshal(marker)
	if err != nil || strings.Contains(string(data), "SECRET") || strings.Contains(string(data), "excerpt") {
		t.Fatal("free metadata escaped review projection")
	}
	for _, invalid := range []JSON{JSON(`{`), JSON(`{}`), JSON(`{"sourcePurge":{"needsReview":false}}`), JSON(`{"sourcePurge":{"needsReview":"true"}}`)} {
		if ParseSourcePurgeReview(invalid) != nil {
			t.Fatal("invalid marker inferred a review fact")
		}
	}
}
