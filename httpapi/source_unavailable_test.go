package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/JieWaZi/neux-knowledge/core"
)

func TestUnavailableSourceIsConflictWithoutPermissionEscalation(t *testing.T) {
	w := httptest.NewRecorder()
	respond(w, nil, &core.Error{Code: core.ErrSourceUnavailable, Operation: "job.source", Detail: "source was superseded"})
	var result APIError
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusConflict || result.Code != string(core.ErrSourceUnavailable) {
		t.Fatalf("source lifecycle conflict became a permission/storage failure: status=%d code=%s", w.Code, result.Code)
	}
}
