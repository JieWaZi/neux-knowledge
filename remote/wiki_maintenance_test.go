package remote_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"testing"

	"github.com/JieWaZi/neux-knowledge/core"
	"github.com/JieWaZi/neux-knowledge/remote"
)

func TestCustomWikiMaintenanceRoundTrip(t *testing.T) {
	ctx := context.Background()
	_, l, c, _ := customFixture(t)
	caps, err := c.Capabilities(ctx)
	if err != nil || !caps.Operations["read"] || !caps.Operations["write"] {
		t.Fatalf("caps %+v %v", caps, err)
	}
	rec, err := c.WriteAgentPage(ctx, core.WritePageInput{RequestID: "agent", LibraryID: l.ID, StructureVersion: l.StructureVersion, Page: core.Page{Slug: "a", Title: "A", PageType: "concept", Content: "target target"}})
	if err != nil {
		t.Fatal(err)
	}
	p, err := c.GetPage(ctx, rec.ResourceID)
	if err != nil || p.LastEditSource != "agent" {
		t.Fatalf("page %+v %v", p, err)
	}
	_, err = c.ReplacePageText(ctx, core.ReplacePageTextInput{RequestID: "replace", LibraryID: l.ID, PageID: p.ID, ExpectedVersion: p.Version, StructureVersion: l.StructureVersion, OldText: "target", NewText: "new"})
	if err != nil {
		t.Fatal(err)
	}
	p, _ = c.GetPage(ctx, p.ID)
	_, err = c.RenamePage(ctx, core.RenamePageInput{RequestID: "rename", LibraryID: l.ID, PageID: p.ID, ExpectedVersion: p.Version, StructureVersion: l.StructureVersion, NewSlug: "b"})
	if err != nil {
		t.Fatal(err)
	}
	p, _ = c.GetPage(ctx, p.ID)
	l, _ = c.GetLibrary(ctx)
	_, err = c.DeletePage(ctx, core.DeletePageInput{RequestID: "delete", LibraryID: l.ID, PageID: p.ID, ExpectedVersion: p.Version, StructureVersion: l.StructureVersion})
	if err != nil {
		t.Fatal(err)
	}
}

func TestOfficialWikiMaintenanceExplicitlyUnsupported(t *testing.T) {
	c, err := remote.NewWeKnora(remote.Options{BaseURL: "https://example.com", Token: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range []string{"write", "agentWrite", "replaceText", "renamePage", "deletePage"} {
		cap, ok := c.Capabilities()[op]
		if !ok || cap.Supported {
			t.Fatalf("official capability %s %+v", op, cap)
		}
	}
}

// maintenanceReceiptTransport returns only test-constructed success responses to verify that the client rejects mismatched receipts.
type maintenanceReceiptTransport func(*http.Request) (*http.Response, error)

func (f maintenanceReceiptTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestCustomWikiMaintenanceValidatesOperationAndKnownResource(t *testing.T) {
	const requestID = "stable/maintenance:request"
	const libraryID = "library"
	cases := []struct {
		name      string                                      // Public maintenance write endpoint.
		operation string                                      // Exact domain operation this endpoint may return.
		target    string                                      // Page identity known before the call; empty during creation.
		invoke    func(*remote.Custom) (*core.Receipt, error) // Public method initiating the same stable request.
	}{
		{"agent-create", "page.agent-write", "", func(c *remote.Custom) (*core.Receipt, error) {
			return c.WriteAgentPage(context.Background(), core.WritePageInput{RequestID: requestID, LibraryID: libraryID, Page: core.Page{Slug: "new-page"}})
		}},
		{"agent-update", "page.agent-write", "page", func(c *remote.Custom) (*core.Receipt, error) {
			return c.WriteAgentPage(context.Background(), core.WritePageInput{RequestID: requestID, LibraryID: libraryID, Page: core.Page{ID: "page"}})
		}},
		{"replace", "page.replace-text", "page", func(c *remote.Custom) (*core.Receipt, error) {
			return c.ReplacePageText(context.Background(), core.ReplacePageTextInput{RequestID: requestID, LibraryID: libraryID, PageID: "page"})
		}},
		{"rename", "page.rename", "page", func(c *remote.Custom) (*core.Receipt, error) {
			return c.RenamePage(context.Background(), core.RenamePageInput{RequestID: requestID, LibraryID: libraryID, PageID: "page", NewSlug: "new-page"})
		}},
		{"delete", "page.delete", "page", func(c *remote.Custom) (*core.Receipt, error) {
			return c.DeletePage(context.Background(), core.DeletePageInput{RequestID: requestID, LibraryID: libraryID, PageID: "page"})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			variants := []string{"valid", "wrong-operation", "wrong-state", "empty-state", "wrong-version", "zero-version"}
			if tc.target != "" {
				variants = append(variants, "wrong-resource")
			}
			for _, variant := range variants {
				t.Run(variant, func(t *testing.T) {
					receipt := core.Receipt{RequestID: requestID, LibraryID: libraryID, ResourceID: tc.target, Operation: tc.operation, State: "saved", Version: 2}
					if receipt.ResourceID == "" {
						receipt.ResourceID = "server-created-resource"
					}
					if variant == "wrong-operation" {
						receipt.Operation = "page.write"
					}
					if variant == "wrong-resource" {
						receipt.ResourceID = "another-resource"
					}
					if variant == "wrong-state" {
						receipt.State = "accepted"
					}
					if variant == "empty-state" {
						receipt.State = ""
					}
					if variant == "wrong-version" {
						receipt.Version = -1
					}
					if variant == "zero-version" {
						receipt.Version = 0
					}
					calls := 0
					client := &http.Client{Transport: maintenanceReceiptTransport(func(r *http.Request) (*http.Response, error) {
						calls++
						payload, err := json.Marshal(receipt)
						if err != nil {
							return nil, err
						}
						return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(bytes.NewReader(payload)), Request: r}, nil
					})}
					c, err := remote.NewCustom(remote.Options{BaseURL: "https://example.com", Token: "secret", HTTPClient: client}, remote.ResourceRef{Backend: "custom", ConnectionID: "connection", KnowledgeBaseID: libraryID})
					if err != nil {
						t.Fatal(err)
					}
					actual, err := tc.invoke(c)
					if variant == "valid" {
						if err != nil || actual == nil || actual.Operation != tc.operation {
							t.Fatalf("valid receipt=%+v error=%v", actual, err)
						}
					} else {
						var unknown *remote.OutcomeUnknownError
						if actual != nil || !errors.As(err, &unknown) || unknown.RequestID != requestID {
							t.Fatalf("mismatched receipt=%+v error=%v; original request must remain recoverable", actual, err)
						}
					}
					if calls != 1 {
						t.Fatalf("write retried or skipped: requests=%d", calls)
					}
				})
			}
		})
	}
}
