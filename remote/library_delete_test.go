package remote_test

import (
	"context"
	"errors"
	"github.com/JieWaZi/neux-knowledge/core"
	"github.com/JieWaZi/neux-knowledge/httpapi"
	"github.com/JieWaZi/neux-knowledge/remote"
	"net/http/httptest"
	"testing"
)

func TestCustomLibraryDeletePreservesServerAuthorityAndReconciliation(t *testing.T) {
	ctx := context.Background()
	e, l, writeOnly, _ := customFixture(t)
	in := core.DeleteLibraryInput{RequestID: "remote-delete", LibraryID: l.ID, ExpectedVersion: l.Version}
	if _, err := writeOnly.DeleteLibrary(ctx, in); err == nil {
		t.Fatal("write implies purge")
	}
	policy, err := httpapi.NewPolicyStore(httpapi.SecurityConfig{Tokens: map[string]httpapi.Principal{"purge": {Subject: "owner", Libraries: map[string]httpapi.LibraryGrant{l.ID: {Operations: []string{"read", "purge"}, PolicyVersion: l.PolicyVersion}}}}})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(httpapi.New(e, httpapi.Options{Security: policy}))
	defer server.Close()
	c, err := remote.NewCustom(remote.Options{BaseURL: server.URL, Token: "purge"}, writeOnly.Resource())
	if err != nil {
		t.Fatal(err)
	}
	caps, err := c.Capabilities(ctx)
	if err != nil || !caps.Operations["libraryDelete"] {
		t.Fatal(caps, err)
	}
	for i := 0; i < 2; i++ {
		r, e := c.DeleteLibrary(ctx, in)
		if e != nil || r.Operation != "library.delete" || r.ResourceID != l.ID {
			t.Fatal(r, e)
		}
	}
	status, err := c.GetLibraryDeletion(ctx)
	if err != nil || status.State != "completed" {
		t.Fatal(status, err)
	}
	recovery := core.RecoverLibraryDeletionInput{RequestID: "remote-recover", LibraryID: l.ID, ExpectedVersion: status.Version}
	for i := 0; i < 2; i++ {
		r, e := c.RecoverLibraryDeletion(ctx, recovery)
		if e != nil || r.State != "completed" {
			t.Fatal(r, e)
		}
	}
	if _, err = c.GetReceipt(ctx, in.RequestID); err != nil {
		t.Fatal(err)
	}
	if err = policy.Replace(httpapi.SecurityConfig{}); err != nil {
		t.Fatal(err)
	}
	_, err = c.GetLibraryDeletion(ctx)
	var denied *remote.Error
	if !errors.As(err, &denied) {
		t.Fatal("revoked credential accesses status", err)
	}
}
