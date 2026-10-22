package httpapi_test

import (
	"context"
	"encoding/json"
	"net/netip"
	"strings"
	"testing"

	"github.com/JieWaZi/neux-knowledge/core"
	"github.com/JieWaZi/neux-knowledge/httpapi"
)

func TestSourcePurgeRequiresDedicatedGrantExplicitConfirmationAndIP(t *testing.T) {
	ctx := context.Background()
	e, l, security := fixture(t)
	h := httpapi.New(e, httpapi.Options{Security: security})
	r, err := e.ImportSource(ctx, core.ImportSourceInput{RequestID: "purge-raw", LibraryID: l.ID, Filename: "private.md", Bytes: []byte("PURGE_HTTP_SECRET")})
	if err != nil {
		t.Fatal(err)
	}
	source, err := e.GetSource(ctx, l.ID, r.ResourceID)
	if err != nil {
		t.Fatal(err)
	}
	base := "/libraries/" + l.ID + "/source-purges"
	response := request(t, h, "GET", base+"/preview?sourceId="+source.SourceID, "secret", "192.0.2.1:1", "", nil)
	if response.Code != 200 {
		t.Fatalf("preview=%d %s", response.Code, response.Body)
	}
	if strings.Contains(response.Body.String(), "PURGE_HTTP_SECRET") || strings.Contains(response.Body.String(), "private.md") {
		t.Fatal("preview leaked source")
	}
	var preview core.SourcePurgePreview
	if err = json.Unmarshal(response.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	in := core.ConfirmSourcePurgeInput{RequestID: "http-purge-confirm", LibraryID: l.ID, SourceID: source.SourceID, PreviewDigest: preview.Digest, Confirmed: true}
	response = request(t, h, "POST", base+"/confirm", "secret", "192.0.2.1:1", "", in)
	if response.Code != 403 {
		t.Fatalf("write/organize/maintain silently granted purge: %d %s", response.Code, response.Body)
	}
	config := httpapi.SecurityConfig{Tokens: map[string]httpapi.Principal{"secret": {Subject: "owner", Libraries: map[string]httpapi.LibraryGrant{l.ID: {Operations: []string{"read", "purge"}, PolicyVersion: l.PolicyVersion}}}}, WriteIPPolicy: httpapi.IPPolicy{Enabled: true}}
	if err = security.Replace(config); err != nil {
		t.Fatal(err)
	}
	response = request(t, h, "POST", base+"/confirm", "secret", "192.0.2.1:1", "", in)
	if response.Code != 403 {
		t.Fatalf("purge bypassed write IP restrictions: %d", response.Code)
	}
	config.WriteIPPolicy.Allowed = []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}
	if err = security.Replace(config); err != nil {
		t.Fatal(err)
	}
	bad := map[string]any{"requestId": "bad-confirm", "libraryId": l.ID, "sourceId": source.SourceID, "previewDigest": preview.Digest, "confirmed": "true"}
	response = request(t, h, "POST", base+"/confirm", "secret", "192.0.2.1:1", "", bad)
	if response.Code != 400 {
		t.Fatalf("truthy confirmation accepted: %d", response.Code)
	}
	response = request(t, h, "POST", base+"/confirm", "secret", "192.0.2.1:1", "", in)
	if response.Code != 200 {
		t.Fatalf("confirm=%d %s", response.Code, response.Body)
	}
	var rec core.Receipt
	if err = json.Unmarshal(response.Body.Bytes(), &rec); err != nil {
		t.Fatal(err)
	}
	response = request(t, h, "POST", base+"/"+rec.ResourceID+"/recover", "secret", "192.0.2.1:1", "", core.RecoverSourcePurgeInput{RequestID: "http-purge-recover", LibraryID: l.ID, PurgeID: rec.ResourceID, ExpectedPolicyVersion: l.PolicyVersion})
	if response.Code != 200 {
		t.Fatalf("recover=%d %s", response.Code, response.Body)
	}
	response = request(t, h, "GET", base+"/"+rec.ResourceID, "secret", "192.0.2.1:1", "", nil)
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"state":"completed"`) {
		t.Fatalf("status=%d %s", response.Code, response.Body)
	}
}
