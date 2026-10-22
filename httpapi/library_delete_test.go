package httpapi_test

import (
	"context"
	"encoding/json"
	"github.com/JieWaZi/neux-knowledge/core"
	"github.com/JieWaZi/neux-knowledge/httpapi"
	"testing"
)

func TestLibraryDeletionRequiresPurgeAndAllowsAuthorizedReconciliation(t *testing.T) {
	ctx := context.Background()
	e, l, security := fixture(t)
	h := httpapi.New(e, httpapi.Options{Security: security})
	base := "/libraries/" + l.ID
	in := core.DeleteLibraryInput{RequestID: "delete-library", LibraryID: l.ID, ExpectedVersion: l.Version}
	if r := request(t, h, "POST", base+"/delete", "secret", "192.0.2.1:1", "", in); r.Code != 403 {
		t.Fatalf("delete lacked dedicated grant: %d %s", r.Code, r.Body)
	}
	if err := security.Replace(httpapi.SecurityConfig{Tokens: map[string]httpapi.Principal{"secret": {Subject: "owner", Libraries: map[string]httpapi.LibraryGrant{l.ID: {Operations: []string{"read", "purge"}, PolicyVersion: l.PolicyVersion}}}}}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		r := request(t, h, "POST", base+"/delete", "secret", "192.0.2.1:1", "", in)
		if r.Code != 200 {
			t.Fatalf("delete/retry: %d %s", r.Code, r.Body)
		}
	}
	r := request(t, h, "GET", base+"/deletion", "secret", "192.0.2.1:1", "", nil)
	if r.Code != 200 {
		t.Fatalf("status unavailable: %d %s", r.Code, r.Body)
	}
	var status core.LibraryDeletionStatus
	if err := json.Unmarshal(r.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	recovery := core.RecoverLibraryDeletionInput{RequestID: "recover-library", LibraryID: l.ID, ExpectedVersion: status.Version}
	for i := 0; i < 2; i++ {
		r = request(t, h, "POST", base+"/deletion/recover", "secret", "192.0.2.1:1", "", recovery)
		if r.Code != 200 {
			t.Fatalf("recovery: %d %s", r.Code, r.Body)
		}
	}
	if _, err := e.GetLibrary(ctx, l.ID); err == nil {
		t.Fatal("library survives")
	}
	if r = request(t, h, "GET", base+"/receipts?requestId=delete-library", "secret", "192.0.2.1:1", "", nil); r.Code != 200 {
		t.Fatalf("receipt inaccessible: %d %s", r.Code, r.Body)
	}
	if r = request(t, h, "GET", base+"/pages", "secret", "192.0.2.1:1", "", nil); r.Code != 404 {
		t.Fatal("deleted library remains readable", r.Code)
	}
}
