package httpapi_test

import (
	"encoding/json"
	"net/http"
	"net/netip"
	"testing"

	"github.com/JieWaZi/neux-knowledge/core"
	"github.com/JieWaZi/neux-knowledge/httpapi"
)

func TestCreationReceiptRequiresCurrentCreatorAndOriginalActor(t *testing.T) {
	e, _, policy := fixture(t)
	config := httpapi.SecurityConfig{Tokens: map[string]httpapi.Principal{
		"creator": {Subject: "creator", CreateLibrary: true},
		"other":   {Subject: "another-creator", CreateLibrary: true},
		"reader":  {Subject: "creator"},
	}}
	if err := policy.Replace(config); err != nil {
		t.Fatal(err)
	}
	handler := httpapi.New(e, httpapi.Options{Security: policy})
	input := core.CreateLibraryInput{RequestID: "creation-owned", Name: "自己的新库", Kind: "general", WikiLanguage: "zh-CN"}
	w := request(t, handler, http.MethodPost, "/libraries", "creator", "192.0.2.1:1", "", input)
	if w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body)
	}
	var created core.Receipt
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	w = request(t, handler, http.MethodGet, "/libraries/creation-receipts?requestId=creation-owned", "creator", "192.0.2.1:1", "", nil)
	var recovered core.Receipt
	if err := json.Unmarshal(w.Body.Bytes(), &recovered); err != nil || w.Code != http.StatusOK || recovered.LibraryID != created.LibraryID || recovered.ResourceID != created.LibraryID || recovered.Operation != "library.create" {
		t.Errorf("creator cannot recover own receipt: %d %s %v", w.Code, w.Body, err)
	}
	for _, token := range []string{"other", "reader"} {
		w = request(t, handler, http.MethodGet, "/libraries/creation-receipts?requestId=creation-owned", token, "192.0.2.1:1", "", nil)
		if w.Code != http.StatusForbidden {
			t.Fatalf("%s obtained another creation receipt: %d %s", token, w.Code, w.Body)
		}
	}
	w = request(t, handler, http.MethodPost, "/libraries", "other", "192.0.2.1:1", "", input)
	if w.Code != http.StatusForbidden {
		t.Fatalf("another creator replayed the known request identity: %d %s", w.Code, w.Body)
	}
	w = request(t, handler, http.MethodGet, "/libraries/"+created.LibraryID, "creator", "192.0.2.1:1", "", nil)
	if w.Code != http.StatusForbidden {
		t.Fatal("creation recovery expanded library grants", w.Code, w.Body)
	}
	w = request(t, handler, http.MethodGet, "/libraries/creation-receipts?requestId=missing", "creator", "192.0.2.1:1", "", nil)
	if w.Code != http.StatusNotFound {
		t.Fatal("missing receipt became success", w.Code, w.Body)
	}
	config.Tokens["creator"] = httpapi.Principal{Subject: "creator"}
	if err := policy.Replace(config); err != nil {
		t.Fatal(err)
	}
	w = request(t, handler, http.MethodGet, "/libraries/creation-receipts?requestId=creation-owned", "creator", "192.0.2.1:1", "", nil)
	if w.Code != http.StatusForbidden {
		t.Fatal("revoked creator retained recovery rights", w.Code, w.Body)
	}
	config.Tokens["creator"] = httpapi.Principal{Subject: "creator", CreateLibrary: true}
	config.WriteIPPolicy = httpapi.IPPolicy{Enabled: true, Allowed: []netip.Prefix{netip.MustParsePrefix("203.0.113.0/24")}}
	if err := policy.Replace(config); err != nil {
		t.Fatal(err)
	}
	w = request(t, handler, http.MethodGet, "/libraries/creation-receipts?requestId=creation-owned", "creator", "192.0.2.1:1", "", nil)
	if w.Code != http.StatusForbidden {
		t.Fatal("creation recovery bypassed current IP policy", w.Code, w.Body)
	}
}
