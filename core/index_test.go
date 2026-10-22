package core_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math"
	"testing"

	"github.com/JieWaZi/neux-knowledge/core"
	"github.com/JieWaZi/neux-knowledge/httpapi"
)

func TestDocumentIndexRequiresRealVectorsAndRejectsChangedModelSpace(t *testing.T) {
	ctx := context.Background()
	e, _ := open(t)
	l := library(t, e, "vectors")
	src := source(t, e, l, "source")
	vector := true
	_ = vector
	l.IndexingStrategy.VectorEnabled = true
	l.EmbeddingModelID = "selected"
	r, err := e.UpdateLibrary(ctx, core.UpdateLibraryInput{RequestID: "vectors-on", LibraryID: l.ID, ExpectedVersion: l.Version, Name: l.Name, PolicyVersion: l.PolicyVersion, IndexingStrategy: &l.IndexingStrategy, EmbeddingModelID: &l.EmbeddingModelID})
	if err != nil {
		t.Fatal(err)
	}
	_ = r
	l, _ = e.GetLibrary(ctx, l.ID)
	j := accept(t, e, l, src)
	artifact, err := (core.MarkdownReader{}).Read(ctx, core.ReadRequest{SourceID: src.SourceID, SourceVersionID: src.ID, SHA256: src.SHA256, Filename: src.Filename, MIME: src.MIME, Raw: []byte("# 原件\nsource"), Scope: "all"})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	emb := core.DocumentEmbedder{Fingerprint: "endpoint-one/model", Embed: func(_ context.Context, texts []string) ([][]float32, error) {
		calls++
		v := make([][]float32, len(texts))
		for i := range v {
			v[i] = []float32{1, 0}
		}
		return v, nil
	}}
	if err = e.IndexDocument(ctx, j.ID, src.ID, artifact, emb); err != nil {
		t.Fatal(err)
	}
	if calls == 0 {
		t.Fatal("enabled vector index never called the embedding service")
	}
	chunks, err := e.ListDocumentChunks(ctx, l.ID, src.ID)
	if err != nil || len(chunks) == 0 {
		t.Fatalf("chunks missing: %v", err)
	}
	hits, err := e.SearchDocumentChunks(ctx, l.ID, "", []float32{1, 0}, emb.Fingerprint, 5)
	if err != nil || len(hits) == 0 || hits[0].Score < 0.99 {
		t.Fatalf("actual vectors not retrievable: %+v %v", hits, err)
	}
	for _, query := range []struct {
		vector      []float32
		fingerprint string
	}{{[]float32{1, 0, 0}, emb.Fingerprint}, {[]float32{1, 0}, "new-endpoint/model"}} {
		_, err = e.SearchDocumentChunks(ctx, l.ID, "", query.vector, query.fingerprint, 5)
		var typed *core.Error
		if !errors.As(err, &typed) || typed.Code != core.ErrVersionConflict {
			t.Fatalf("incompatible query silently returned no result: %v", err)
		}
	}
	if _, parentErr := e.GetDocumentChunkParents(ctx, l.ID, make([]string, 6)); parentErr == nil {
		t.Fatal("unbounded parent lookup accepted")
	}
	original := emb.Embed
	emb.Embed = func(_ context.Context, texts []string) ([][]float32, error) {
		v := make([][]float32, len(texts))
		for i := range v {
			v[i] = []float32{1, 0, 0}
		}
		return v, nil
	}
	if err = e.IndexDocument(ctx, j.ID, src.ID, artifact, emb); err == nil {
		t.Fatal("same endpoint/model changed dimension silently mixed")
	}
	hits, err = e.SearchDocumentChunks(ctx, l.ID, "", []float32{1, 0}, emb.Fingerprint, 5)
	if err != nil || len(hits) == 0 {
		t.Fatalf("rejected new dimensions destroyed existing vectors: %v", err)
	}
	emb.Embed = original
	emb.Fingerprint = "different-endpoint/same-model"
	if err = e.IndexDocument(ctx, j.ID, src.ID, artifact, emb); err == nil {
		t.Fatal("different embedding space silently mixed")
	}
	emb.Fingerprint = "endpoint-one/model"
	emb.Embed = func(context.Context, []string) ([][]float32, error) {
		return [][]float32{{float32(math.NaN()), 0}}, nil
	}
	if err = e.ValidateDocumentEmbedding(ctx, emb, []string{"probe"}); err == nil {
		t.Fatal("nonfinite embedding accepted")
	}
}

func TestFailedDocumentEmbeddingNeverReportsIndexed(t *testing.T) {
	ctx := context.Background()
	e, _ := open(t)
	l := library(t, e, "index-failure")
	src := source(t, e, l, "failure")
	l.IndexingStrategy.VectorEnabled = true
	l.EmbeddingModelID = "selected"
	_, err := e.UpdateLibrary(ctx, core.UpdateLibraryInput{RequestID: "enabled", LibraryID: l.ID, ExpectedVersion: l.Version, Name: l.Name, PolicyVersion: l.PolicyVersion, IndexingStrategy: &l.IndexingStrategy, EmbeddingModelID: &l.EmbeddingModelID})
	if err != nil {
		t.Fatal(err)
	}
	l, _ = e.GetLibrary(ctx, l.ID)
	j := accept(t, e, l, src)
	raw, _ := e.ReadRaw(ctx, l.ID, src.ID)
	artifact, _ := (core.MarkdownReader{}).Read(ctx, core.ReadRequest{SourceID: src.SourceID, SourceVersionID: src.ID, SHA256: src.SHA256, MIME: src.MIME, Filename: src.Filename, Raw: raw, Scope: "all"})
	if err = e.IndexDocument(ctx, j.ID, src.ID, artifact, core.DocumentEmbedder{Fingerprint: "space", Embed: func(context.Context, []string) ([][]float32, error) { return nil, errors.New("provider unavailable") }}); err == nil {
		t.Fatal("embedding failure was swallowed")
	}
	chunks, _ := e.ListDocumentChunks(ctx, l.ID, src.ID)
	for _, ch := range chunks {
		if ch.State == "indexed" {
			t.Fatal("failed embedding was marked indexed")
		}
	}
}

func TestDocumentEmbeddingUsesUpstreamFiveItemBatches(t *testing.T) {
	t.Setenv("BATCH_EMBED_SIZE", "5")
	e, _ := open(t)
	calls := 0
	err := e.ValidateDocumentEmbedding(context.Background(), core.DocumentEmbedder{Fingerprint: "space", Embed: func(_ context.Context, texts []string) ([][]float32, error) {
		calls++
		if len(texts) > 5 {
			t.Fatalf("oversized provider batch: %d", len(texts))
		}
		v := make([][]float32, len(texts))
		for i := range v {
			v[i] = []float32{1, 0}
		}
		return v, nil
	}}, make([]string, 11))
	if err != nil || calls != 3 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

func TestEmptyDocumentIndexDoesNotRejectVectorQuery(t *testing.T) {
	e, _ := open(t)
	l := library(t, e, "empty-index")
	hits, err := e.SearchDocumentChunks(context.Background(), l.ID, "", []float32{1, 0}, "space", 5)
	if err != nil || len(hits) != 0 {
		t.Fatalf("empty index query=%v %v", hits, err)
	}
}

// TestDocumentIndexRechecksRevokedCredential prevents persisting index results whose authorization was revoked during an external call.
func TestDocumentIndexRechecksRevokedCredential(t *testing.T) {
	for _, vector := range []bool{false, true} {
		name := "keyword"
		if vector {
			name = "vector"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			e, _ := open(t)
			l := library(t, e, "index-revocation")
			src := source(t, e, l, "source")
			if vector {
				l.IndexingStrategy.VectorEnabled = true
				l.EmbeddingModelID = "selected"
				_, err := e.UpdateLibrary(ctx, core.UpdateLibraryInput{RequestID: "vector-config", LibraryID: l.ID, ExpectedVersion: l.Version, Name: l.Name, PolicyVersion: l.PolicyVersion, IndexingStrategy: &l.IndexingStrategy, EmbeddingModelID: &l.EmbeddingModelID})
				if err != nil {
					t.Fatal(err)
				}
				l, _ = e.GetLibrary(ctx, l.ID)
			}
			j := accept(t, e, l, src)
			permission := core.PermissionSnapshot{PolicyVersion: l.PolicyVersion, Operations: []string{"organize"}}
			policies, err := httpapi.NewPolicyStore(httpapi.SecurityConfig{Tokens: map[string]httpapi.Principal{"secret": {Subject: "u", Libraries: map[string]httpapi.LibraryGrant{l.ID: {PolicyVersion: l.PolicyVersion, Operations: permission.Operations}}}}})
			if err != nil {
				t.Fatal(err)
			}
			digest := sha256.Sum256([]byte("secret"))
			guard := policies.RestoreGuard(e, httpapi.RecoveryIdentity{Subject: "u", CredentialSHA256: hex.EncodeToString(digest[:]), LibraryID: l.ID, Operation: "organize", Permission: permission})
			// Initial authorization is valid; replacing the policy during the external call must return immediately without waiting for the publication lock.
			release, err := guard(ctx)
			if err != nil {
				t.Fatal(err)
			}
			release()
			revoked := false
			revoke := func() {
				if err := policies.Replace(httpapi.SecurityConfig{}); err != nil {
					t.Fatal(err)
				}
				revoked = true
			}
			emb := core.DocumentEmbedder{Fingerprint: "space", PublishGuard: guard, Embed: func(_ context.Context, texts []string) ([][]float32, error) {
				revoke()
				vectors := make([][]float32, len(texts))
				for i := range vectors {
					vectors[i] = []float32{1, 0}
				}
				return vectors, nil
			}}
			if !vector {
				revoke()
			}
			raw, err := e.ReadRaw(ctx, l.ID, src.ID)
			if err != nil {
				t.Fatal(err)
			}
			artifact, err := (core.MarkdownReader{}).Read(ctx, core.ReadRequest{SourceID: src.SourceID, SourceVersionID: src.ID, SHA256: src.SHA256, Filename: src.Filename, MIME: src.MIME, Raw: raw, Scope: "all"})
			if err != nil {
				t.Fatal(err)
			}
			_, err = e.PrepareIndexedDocument(ctx, j.ID, src.ID, artifact, emb)
			var denied *httpapi.APIError
			if !errors.As(err, &denied) || denied.Code != "policy_changed" {
				t.Errorf("revoked credential index result=%v", err)
			}
			if !revoked {
				t.Fatal("embedding revocation was not exercised")
			}
			chunks, err := e.ListDocumentChunks(ctx, l.ID, src.ID)
			if err != nil || len(chunks) != 0 {
				t.Fatalf("revoked credential persisted chunks=%+v err=%v", chunks, err)
			}
		})
	}
}

// TestDocumentIndexRejectsGuardWithoutRelease rejects publication guards without a valid release handle.
func TestDocumentIndexRejectsGuardWithoutRelease(t *testing.T) {
	ctx := context.Background()
	e, _ := open(t)
	l := library(t, e, "index-invalid-guard")
	src := source(t, e, l, "source")
	j := accept(t, e, l, src)
	raw, err := e.ReadRaw(ctx, l.ID, src.ID)
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := (core.MarkdownReader{}).Read(ctx, core.ReadRequest{SourceID: src.SourceID, SourceVersionID: src.ID, SHA256: src.SHA256, Filename: src.Filename, MIME: src.MIME, Raw: raw, Scope: "all"})
	if err != nil {
		t.Fatal(err)
	}
	err = e.IndexDocument(ctx, j.ID, src.ID, artifact, core.DocumentEmbedder{PublishGuard: func(context.Context) (func(), error) { return nil, nil }})
	var invalid *core.Error
	if !errors.As(err, &invalid) || invalid.Code != core.ErrInvalid {
		t.Fatalf("invalid guard result=%v", err)
	}
	chunks, err := e.ListDocumentChunks(ctx, l.ID, src.ID)
	if err != nil || len(chunks) != 0 {
		t.Fatalf("invalid guard persisted chunks=%+v err=%v", chunks, err)
	}
}
