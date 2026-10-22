package remote_test

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/JieWaZi/neux-knowledge/core"
	"github.com/JieWaZi/neux-knowledge/httpapi"
	"github.com/JieWaZi/neux-knowledge/remote"
)

func TestCustomPurgeUsesServerGrantAndPersistentRecovery(t *testing.T) {
	ctx := context.Background()
	e, l, writeOnly, _ := customFixture(t)
	r, err := e.ImportSource(ctx, core.ImportSourceInput{RequestID: "purge-source", LibraryID: l.ID, Filename: "original.md", Bytes: []byte("original")})
	if err != nil {
		t.Fatal(err)
	}
	source, err := e.GetSource(ctx, l.ID, r.ResourceID)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := writeOnly.PreviewSourcePurge(ctx, core.PurgePreviewInput{LibraryID: l.ID, SourceID: source.SourceID})
	if err != nil {
		t.Fatal(err)
	}
	in := core.ConfirmSourcePurgeInput{RequestID: "remote-confirm", LibraryID: l.ID, SourceID: source.SourceID, PreviewDigest: preview.Digest, Confirmed: true}
	_, err = writeOnly.ConfirmSourcePurge(ctx, in)
	var denied *remote.Error
	if !errors.As(err, &denied) || denied.Code != "forbidden" {
		t.Fatalf("write granted remote purge: %v", err)
	}
	policy, err := httpapi.NewPolicyStore(httpapi.SecurityConfig{Tokens: map[string]httpapi.Principal{"purge-token": {Subject: "purge-owner", Libraries: map[string]httpapi.LibraryGrant{l.ID: {Operations: []string{"read", "purge"}, PolicyVersion: l.PolicyVersion}}}}})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(httpapi.New(e, httpapi.Options{Security: policy}))
	defer server.Close()
	client, err := remote.NewCustom(remote.Options{BaseURL: server.URL, Token: "purge-token"}, writeOnly.Resource())
	if err != nil {
		t.Fatal(err)
	}
	confirmed, err := client.ConfirmSourcePurge(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := client.ConfirmSourcePurge(ctx, in)
	if err != nil || replayed.ResourceID != confirmed.ResourceID {
		t.Fatalf("remote replay failed: %+v %v", replayed, err)
	}
	status, err := client.GetSourcePurge(ctx, confirmed.ResourceID)
	if err != nil || status.State != "pending" {
		t.Fatalf("accepted != cleanup completed: %+v %v", status, err)
	}
	listed, err := client.ListSourcePurges(ctx, 0, 10)
	if err != nil || listed.Total != 1 || len(listed.Items) != 1 || listed.Items[0].ID != confirmed.ResourceID {
		t.Fatalf("lost remote cleanup list: %+v %v", listed, err)
	}
	empty, err := client.ListSourcePurges(ctx, 1, 10)
	if err != nil || empty.Total != 1 || len(empty.Items) != 0 {
		t.Fatalf("lost remote paging: %+v %v", empty, err)
	}
	_, err = client.RecoverSourcePurge(ctx, core.RecoverSourcePurgeInput{RequestID: "remote-recover", LibraryID: l.ID, PurgeID: confirmed.ResourceID, ExpectedPolicyVersion: l.PolicyVersion})
	if err != nil {
		t.Fatal(err)
	}
	status, err = client.GetSourcePurge(ctx, confirmed.ResourceID)
	if err != nil || status.State != "completed" {
		t.Fatalf("server recovery status=%+v %v", status, err)
	}
	receipt, err := client.GetReceipt(ctx, in.RequestID)
	if err != nil || receipt.ResourceID != confirmed.ResourceID {
		t.Fatalf("unknown response cannot reconcile purge receipt: %+v %v", receipt, err)
	}
	_, err = e.ReadRaw(ctx, l.ID, source.ID)
	if err == nil {
		t.Fatal("remote purge left source readable")
	}
}
