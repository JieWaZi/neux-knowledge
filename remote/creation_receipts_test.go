package remote_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/JieWaZi/neux-knowledge/core"
	"github.com/JieWaZi/neux-knowledge/remote"
)

func TestCustomCreationReceiptValidatesOperationIdentityAndLibrary(t *testing.T) {
	for name, receipt := range map[string]core.Receipt{
		"another-operation": {RequestID: "creation", Operation: "library.update", LibraryID: "library", ResourceID: "library", State: "saved", Version: 1},
		"another-request":   {RequestID: "another", Operation: "library.create", LibraryID: "library", ResourceID: "library", State: "saved", Version: 1},
		"resource-mismatch": {RequestID: "creation", Operation: "library.create", LibraryID: "library", ResourceID: "another", State: "saved", Version: 1},
		"no-library":        {RequestID: "creation", Operation: "library.create", State: "saved", Version: 1},
		"uncommitted":       {RequestID: "creation", Operation: "library.create", LibraryID: "library", ResourceID: "library", State: "accepted", Version: 1},
	} {
		t.Run(name, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/api/knowledge/v1/libraries/creation-receipts" || r.URL.Query().Get("requestId") != "creation" {
					t.Error("creation receipt was not connection scoped", r.URL)
				}
				_ = json.NewEncoder(w).Encode(receipt)
			}))
			defer s.Close()
			c, err := remote.NewCustom(remote.Options{BaseURL: s.URL, Token: "secret"}, remote.ResourceRef{Backend: "custom", ConnectionID: "connection"})
			if err != nil {
				t.Fatal(err)
			}
			var incompatible *remote.Error
			if _, err := c.GetCreationReceipt(context.Background(), "creation"); !errors.As(err, &incompatible) || incompatible.Code != "protocol_incompatible" {
				t.Fatal("invalid creation receipt was accepted", err)
			}
		})
	}
}
