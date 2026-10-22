package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JieWaZi/neux-knowledge/core"
	"github.com/JieWaZi/neux-knowledge/httpapi"
)

func fixture(t *testing.T) (*core.Engine, *core.Library, *httpapi.PolicyStore) {
	t.Helper()
	d := t.TempDir()
	e, err := core.Open(context.Background(), core.Options{DBPath: filepath.Join(d, "k.db"), RawDir: filepath.Join(d, "raw")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	r, err := e.CreateLibrary(context.Background(), core.CreateLibraryInput{RequestID: "create", Name: "服务", Kind: "general", WikiLanguage: "zh-CN"})
	if err != nil {
		t.Fatal(err)
	}
	l, _ := e.GetLibrary(context.Background(), r.ResourceID)
	s, err := httpapi.NewPolicyStore(httpapi.SecurityConfig{Tokens: map[string]httpapi.Principal{"secret": {Subject: "u", Libraries: map[string]httpapi.LibraryGrant{l.ID: {Operations: []string{"read", "write", "organize", "maintain"}, Scope: []string{"project-a"}, PolicyVersion: l.PolicyVersion}}}}})
	if err != nil {
		t.Fatal(err)
	}
	return e, l, s
}
func request(t *testing.T, h http.Handler, method, path, token, ip, xff string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var b io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		b = bytes.NewReader(data)
	}
	r := httptest.NewRequest(method, httpapi.BasePath+path, b)
	r.RemoteAddr = ip
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if xff != "" {
		r.Header.Set("X-Forwarded-For", xff)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func TestAuthenticationScopeAndRawOnly(t *testing.T) {
	e, l, s := fixture(t)
	h := httpapi.New(e, httpapi.Options{Security: s})
	for _, tc := range []struct {
		path  string // Request path.
		token string // Current test credential.
		want  int    // Expected actual HTTP status.
	}{{"/libraries", "", 401}, {"/libraries", "bad", 401}, {"/libraries/other", "secret", 403}} {
		w := request(t, h, "GET", tc.path, tc.token, "192.0.2.1:1", "", nil)
		if w.Code != tc.want {
			t.Fatalf("%s: %d %s", tc.path, w.Code, w.Body)
		}
	}
	raw := []byte{0xef, 0xbb, 0xbf, 0, 0xff}
	in := core.ImportSourceInput{RequestID: "upload", LibraryID: l.ID, Filename: "中文.bin", Bytes: raw}
	w := request(t, h, "POST", "/libraries/"+l.ID+"/sources", "secret", "192.0.2.1:1", "", in)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	var r core.Receipt
	_ = json.Unmarshal(w.Body.Bytes(), &r)
	w = request(t, h, "GET", "/libraries/"+l.ID+"/sources/"+r.ResourceID+"/raw", "secret", "192.0.2.1:1", "", nil)
	if w.Code != 200 || !bytes.Equal(w.Body.Bytes(), raw) {
		t.Fatal(w.Code, w.Body)
	}
	_, n, _ := e.ListJobs(context.Background(), l.ID, 0, 50)
	if n != 0 {
		t.Fatal("upload started job")
	}
	w = request(t, h, "POST", "/libraries/"+l.ID+"/organize", "secret", "192.0.2.1:1", "", httpapi.OrganizeRequest{RequestID: "org", LibraryID: l.ID, ProfileID: "server-profile"})
	if w.Code != 501 {
		t.Fatal(w.Code, w.Body)
	}
}
func TestAllWritesDenyEmptyAndForgedForwarding(t *testing.T) {
	e, l, s := fixture(t)
	cfg := httpapi.SecurityConfig{Tokens: map[string]httpapi.Principal{"secret": {Subject: "u", Libraries: map[string]httpapi.LibraryGrant{l.ID: {Operations: []string{"read", "write", "organize", "maintain"}, PolicyVersion: l.PolicyVersion}}}}, WriteIPPolicy: httpapi.IPPolicy{Enabled: true}}
	if err := s.Replace(cfg); err != nil {
		t.Fatal(err)
	}
	h := httpapi.New(e, httpapi.Options{Security: s})
	for _, suffix := range []string{"", "/sources", "/sources/x/archive", "/pages", "/pages/x/restore", "/themes", "/organize", "/jobs/x/cancel", "/maintenance"} {
		w := request(t, h, "POST", "/libraries/"+l.ID+suffix, "secret", "198.51.100.1:20", "192.0.2.1", map[string]string{})
		if w.Code != 403 {
			t.Fatalf("%s: %d %s", suffix, w.Code, w.Body)
		}
	}
	cfg.WriteIPPolicy.Allowed = []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}
	_ = s.Replace(cfg)
	w := request(t, h, "POST", "/libraries/"+l.ID+"/sources", "secret", "198.51.100.1:20", "192.0.2.1", nil)
	if w.Code != 403 {
		t.Fatal("forged XFF admitted", w.Code)
	}
	cfg.WriteIPPolicy.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("198.51.100.0/24")}
	_ = s.Replace(cfg)
	w = request(t, h, "POST", "/libraries/"+l.ID+"/sources", "secret", "198.51.100.1:20", "192.0.2.1, 198.51.100.2", core.ImportSourceInput{RequestID: "trusted", LibraryID: l.ID, Filename: "a", Bytes: []byte("ok")})
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	for _, xff := range []string{"", "192.0.2.1, unknown", "192.0.2.1, 203.0.113.1"} {
		w = request(t, h, "POST", "/libraries/"+l.ID+"/sources", "secret", "198.51.100.1:20", xff, nil)
		if w.Code != 403 {
			t.Fatalf("bad chain %q: %d", xff, w.Code)
		}
	}
}

func TestMaintenanceRequiresExactPageAndConfiguredProfile(t *testing.T) {
	e, l, s := fixture(t)
	h := httpapi.New(e, httpapi.Options{Security: s, Execution: &executionSpy{}, Profiles: []httpapi.Profile{{ID: "server", Name: "服务器"}}})
	w := request(t, h, "POST", "/libraries/"+l.ID+"/maintenance", "secret", "192.0.2.1:1", "", httpapi.MaintenanceRequest{RequestID: "m", LibraryID: l.ID, ProfileID: "server", Instruction: "改正文"})
	if w.Code != 400 {
		t.Fatal(w.Code, w.Body)
	}
	w = request(t, h, "POST", "/libraries/"+l.ID+"/organize", "secret", "192.0.2.1:1", "", httpapi.OrganizeRequest{RequestID: "o", LibraryID: l.ID, ProfileID: "local-cli"})
	if w.Code != 501 {
		t.Fatal(w.Code, w.Body)
	}
}

type executionSpy struct{}

func (*executionSpy) Organize(context.Context, httpapi.OrganizeRequest, httpapi.Authorization) (*core.Receipt, error) {
	return nil, nil
}
func (*executionSpy) Maintain(context.Context, httpapi.MaintenanceRequest, httpapi.Authorization) (*core.Receipt, error) {
	return nil, nil
}

type recordingExecution struct {
	auth httpapi.Authorization   // Actual authorization frozen at server acceptance.
	e    *core.Engine            // Actual SQLite core with domain transactions intact.
	in   httpapi.OrganizeRequest // Organization contract actually decoded by HTTP.
}

func (x *recordingExecution) Organize(ctx context.Context, in httpapi.OrganizeRequest, a httpapi.Authorization) (*core.Receipt, error) {
	x.auth = a
	x.in = in
	return x.e.AcceptJob(ctx, core.AcceptJobInput{RequestID: in.RequestID, LibraryID: in.LibraryID, ExpectedVersion: in.ExpectedVersion, StructureVersion: in.StructureVersion, SourceVersionIDs: in.SourceVersionIDs, Rebuild: in.Rebuild, Permission: a.Permission, RulesVersion: "rules", ConfigDigest: "profile"})
}
func (x *recordingExecution) Maintain(context.Context, httpapi.MaintenanceRequest, httpapi.Authorization) (*core.Receipt, error) {
	return nil, nil
}
func TestRecoveryGuardRevalidatesCredentialAndSerializesRevocation(t *testing.T) {
	e, l, s := fixture(t)
	sr, err := e.ImportSource(context.Background(), core.ImportSourceInput{RequestID: "src", LibraryID: l.ID, Filename: "a", Bytes: []byte("source")})
	if err != nil {
		t.Fatal(err)
	}
	x := &recordingExecution{e: e}
	h := httpapi.New(e, httpapi.Options{Security: s, Execution: x, Profiles: []httpapi.Profile{{ID: "server"}}})
	w := request(t, h, "POST", "/libraries/"+l.ID+"/organize", "secret", "192.0.2.1:1", "", httpapi.OrganizeRequest{RequestID: "org", LibraryID: l.ID, ExpectedVersion: l.Version, StructureVersion: l.StructureVersion, SourceVersionIDs: []string{sr.ResourceID}, ProfileID: "server"})
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	guard := s.RestoreGuard(e, x.auth.Recovery)
	unlock, err := guard(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _ = s.Replace(httpapi.SecurityConfig{}); close(done) }()
	select {
	case <-done:
		t.Fatal("revocation passed active publication lock")
	case <-time.After(30 * time.Millisecond):
	}
	unlock()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("revocation blocked after publish release")
	}
	if release, err := guard(context.Background()); err == nil {
		release()
		t.Fatal("revoked original credential recovered")
	}
	data, _ := json.Marshal(x.auth.Recovery)
	if bytes.Contains(data, []byte("secret")) {
		t.Fatal("recovery persisted token")
	}
}

func TestTrustedProxyCombinesAllForwardedHeaderLines(t *testing.T) {
	e, l, s := fixture(t)
	_ = s.Replace(httpapi.SecurityConfig{Tokens: map[string]httpapi.Principal{"secret": {Subject: "u", Libraries: map[string]httpapi.LibraryGrant{l.ID: {Operations: []string{"read", "write"}, PolicyVersion: l.PolicyVersion}}}}, WriteIPPolicy: httpapi.IPPolicy{Enabled: true, Allowed: []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}, TrustedProxies: []netip.Prefix{netip.MustParsePrefix("198.51.100.0/24")}}})
	h := httpapi.New(e, httpapi.Options{Security: s})
	r := httptest.NewRequest("POST", httpapi.BasePath+"/libraries/"+l.ID+"/sources", nil)
	r.RemoteAddr = "198.51.100.1:1"
	r.Header.Set("Authorization", "Bearer secret")
	r.Header.Add("X-Forwarded-For", "192.0.2.1")
	r.Header.Add("X-Forwarded-For", "203.0.113.1")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatalf("separate forged forwarding header admitted: %d %s", w.Code, w.Body)
	}
}
func TestCapabilitiesReflectWriteIPAndEmptyLists(t *testing.T) {
	e, l, s := fixture(t)
	_ = s.Replace(httpapi.SecurityConfig{Tokens: map[string]httpapi.Principal{"secret": {Subject: "u", Libraries: map[string]httpapi.LibraryGrant{l.ID: {Operations: []string{"read", "write", "organize"}, PolicyVersion: l.PolicyVersion}}}}, WriteIPPolicy: httpapi.IPPolicy{Enabled: true}})
	h := httpapi.New(e, httpapi.Options{Security: s})
	w := request(t, h, "GET", "/libraries/"+l.ID+"/capabilities", "secret", "192.0.2.1:1", "", nil)
	var c httpapi.Capabilities
	_ = json.Unmarshal(w.Body.Bytes(), &c)
	if w.Code != 200 || c.Operations["write"] {
		t.Fatal(w.Code, w.Body)
	}
	for _, suffix := range []string{"/pages", "/sources", "/jobs"} {
		w = request(t, h, "GET", "/libraries/"+l.ID+suffix, "secret", "192.0.2.1:1", "", nil)
		var page struct {
			Items json.RawMessage `json:"items"` // Check the actual serialization of an empty list.
		}
		_ = json.Unmarshal(w.Body.Bytes(), &page)
		if w.Code != 200 || string(page.Items) != "[]" {
			t.Fatal(suffix, w.Code, w.Body)
		}
	}
}

func TestReadOnlyCredentialsUnknownLibraryAndClientAuthorizationCannotEscalate(t *testing.T) {
	e, l, s := fixture(t)
	_ = s.Replace(httpapi.SecurityConfig{Tokens: map[string]httpapi.Principal{"reader": {Subject: "r", Libraries: map[string]httpapi.LibraryGrant{l.ID: {Operations: []string{"read"}, PolicyVersion: l.PolicyVersion}, "missing": {Operations: []string{"read"}, PolicyVersion: 1}}}, "writer": {Subject: "w", Libraries: map[string]httpapi.LibraryGrant{l.ID: {Operations: []string{"read", "organize"}, Scope: []string{"fixed"}, PolicyVersion: l.PolicyVersion}}}}})
	h := httpapi.New(e, httpapi.Options{Security: s, Execution: &executionSpy{}, Profiles: []httpapi.Profile{{ID: "server"}}})
	w := request(t, h, "POST", "/libraries/"+l.ID+"/sources", "reader", "192.0.2.1:1", "", core.ImportSourceInput{RequestID: "no", LibraryID: l.ID, Filename: "a", Bytes: []byte("x")})
	if w.Code != 403 {
		t.Fatal(w.Code, w.Body)
	}
	w = request(t, h, "GET", "/libraries/missing", "reader", "192.0.2.1:1", "", nil)
	if w.Code != 404 {
		t.Fatal(w.Code, w.Body)
	}
	w = request(t, h, "POST", "/libraries/"+l.ID+"/organize", "writer", "192.0.2.1:1", "", map[string]any{"requestId": "escalate", "libraryId": l.ID, "profileId": "server", "permission": map[string]any{"scope": []string{"ungranted"}}})
	if w.Code != 400 {
		t.Fatal(w.Code, w.Body)
	}
	w = request(t, h, "POST", "/libraries/"+l.ID+"/organize", "writer", "192.0.2.1:1", "", map[string]any{"requestId": "local", "libraryId": l.ID, "profileId": "server", "cliPath": "/private/bin"})
	if w.Code != 400 {
		t.Fatal(w.Code, w.Body)
	}
}

type forgedExecution struct {
	library string // Library identity in the forged receipt.
}

func (x *forgedExecution) Organize(context.Context, httpapi.OrganizeRequest, httpapi.Authorization) (*core.Receipt, error) {
	return &core.Receipt{RequestID: "forged", LibraryID: x.library, ResourceID: "not-persisted", Operation: "job.accept", State: "accepted", Version: 1}, nil
}
func (x *forgedExecution) Maintain(context.Context, httpapi.MaintenanceRequest, httpapi.Authorization) (*core.Receipt, error) {
	return nil, nil
}
func TestExecutionCannotClaimAnUnpersistedReceipt(t *testing.T) {
	e, l, s := fixture(t)
	h := httpapi.New(e, httpapi.Options{Security: s, Execution: &forgedExecution{library: l.ID}, Profiles: []httpapi.Profile{{ID: "server"}}})
	w := request(t, h, "POST", "/libraries/"+l.ID+"/organize", "secret", "192.0.2.1:1", "", httpapi.OrganizeRequest{RequestID: "forged", LibraryID: l.ID, ProfileID: "server"})
	if w.Code != 502 {
		t.Fatal(w.Code, w.Body)
	}
}

func TestCapabilitiesSeparateSupportPermissionAndConditions(t *testing.T) {
	e, l, s := fixture(t)
	_ = s.Replace(httpapi.SecurityConfig{Tokens: map[string]httpapi.Principal{"reader": {Subject: "r", Libraries: map[string]httpapi.LibraryGrant{l.ID: {Operations: []string{"read"}, PolicyVersion: l.PolicyVersion}}}}})
	w := request(t, httpapi.New(e, httpapi.Options{Security: s}), "GET", "/libraries/"+l.ID+"/capabilities", "reader", "192.0.2.1:1", "", nil)
	var c httpapi.Capabilities
	_ = json.Unmarshal(w.Body.Bytes(), &c)
	if w.Code != 200 || !c.Details["write"].Supported || c.Details["write"].Permission != "denied" || c.Details["organize"].Condition != "execution_unavailable" {
		t.Fatal(w.Code, w.Body)
	}
}

func TestCapabilitiesExposeThemeSourceAndRebuildFacts(t *testing.T) {
	e, l, s := fixture(t)
	_ = s.Replace(httpapi.SecurityConfig{Tokens: map[string]httpapi.Principal{"reader": {Subject: "r", Libraries: map[string]httpapi.LibraryGrant{l.ID: {Operations: []string{"read"}, PolicyVersion: l.PolicyVersion}}}}})
	w := request(t, httpapi.New(e, httpapi.Options{Security: s}), "GET", "/libraries/"+l.ID+"/capabilities", "reader", "192.0.2.1:1", "", nil)
	var c httpapi.Capabilities
	if err := json.Unmarshal(w.Body.Bytes(), &c); err != nil || w.Code != 200 {
		t.Fatal(w.Code, w.Body, err)
	}
	for _, name := range []string{"themes", "sourceRead", "sourceDownload", "sourceContent"} {
		if !c.Operations[name] || !c.Details[name].Supported || c.Details[name].Permission != "allowed" || c.Details[name].Condition != "ready" {
			t.Errorf("read capability %s missing: %+v", name, c)
		}
	}
	if !c.Details["themeWrite"].Supported || c.Details["themeWrite"].Permission != "denied" || c.Operations["themeWrite"] {
		t.Fatalf("theme write bypassed write permission: %+v", c)
	}
	if !c.Details["activity"].Supported || c.Details["activity"].Permission != "denied" || c.Operations["activity"] {
		t.Fatalf("activity bypassed write permission: %+v", c)
	}
	if c.Details["rebuild"].Supported || c.Details["rebuild"].Permission != "denied" || c.Details["rebuild"].Condition != "execution_unavailable" {
		t.Fatalf("rebuild bypassed execution or permission facts: %+v", c)
	}
}

func TestOrganizeHTTPPreservesExplicitRebuild(t *testing.T) {
	e, l, s := fixture(t)
	source, err := e.ImportSource(context.Background(), core.ImportSourceInput{RequestID: "raw-rebuild", LibraryID: l.ID, Filename: "source.md", Bytes: []byte("original")})
	if err != nil {
		t.Fatal(err)
	}
	x := &recordingExecution{e: e}
	h := httpapi.New(e, httpapi.Options{Security: s, Execution: x, Profiles: []httpapi.Profile{{ID: "server"}}})
	w := request(t, h, "POST", "/libraries/"+l.ID+"/organize", "secret", "192.0.2.1:1", "", map[string]any{"requestId": "rebuild-http", "libraryId": l.ID, "expectedVersion": l.Version, "structureVersion": l.StructureVersion, "sourceVersionIds": []string{source.ResourceID}, "profileId": "server", "rebuild": true, "instruction": "保留边界与来源"})
	raw, _ := json.Marshal(x.in)
	var accepted map[string]any
	_ = json.Unmarshal(raw, &accepted)
	if w.Code != 200 || accepted["rebuild"] != true || accepted["profileId"] != "server" || accepted["instruction"] != "保留边界与来源" {
		t.Fatalf("HTTP lost rebuild intent: %d %s, input=%s", w.Code, w.Body, raw)
	}
	after, err := e.GetSource(context.Background(), l.ID, source.ResourceID)
	if err != nil || after.RowVersion != 2 {
		t.Fatal("HTTP rebuild became ordinary organize", after, err)
	}
	w = request(t, h, "POST", "/libraries/"+l.ID+"/organize", "secret", "192.0.2.1:1", "", map[string]any{"requestId": "oversized", "libraryId": l.ID, "profileId": "server", "instruction": strings.Repeat("a", 65537)})
	if w.Code != 400 {
		t.Fatal("oversized instruction accepted", w.Code, w.Body)
	}
}

func TestHTTPDoesNotExposeJobCancellation(t *testing.T) {
	e, l, s := fixture(t)
	imported, err := e.ImportSource(context.Background(), core.ImportSourceInput{RequestID: "cancel-source", LibraryID: l.ID, Filename: "source.md", Bytes: []byte("document")})
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := e.AcceptJob(context.Background(), core.AcceptJobInput{RequestID: "cancel-job", LibraryID: l.ID, ExpectedVersion: l.Version, StructureVersion: l.StructureVersion, SourceVersionIDs: []string{imported.ResourceID}, Permission: core.PermissionSnapshot{PolicyVersion: l.PolicyVersion, Operations: []string{"organize"}}, RulesVersion: "test", ConfigDigest: "test"})
	if err != nil {
		t.Fatal(err)
	}
	job, err := e.GetJob(context.Background(), accepted.JobID)
	if err != nil {
		t.Fatal(err)
	}
	h := httpapi.New(e, httpapi.Options{Security: s, Execution: &recordingExecution{e: e}})
	w := request(t, h, "POST", "/libraries/"+l.ID+"/jobs/"+job.ID+"/cancel", "secret", "192.0.2.1:1", "", core.JobActionInput{RequestID: "cancel", JobID: job.ID, ExpectedVersion: job.Version})
	if w.Code != 404 {
		t.Fatalf("job cancellation is exposed: %d %s", w.Code, w.Body)
	}
	current, err := e.GetJob(context.Background(), job.ID)
	if err != nil || current.State != "queued" || current.CancelRequested {
		t.Fatalf("removed route changed job: %+v %v", current, err)
	}
}

// TestPolicyReplacementPreservesActivityActor verifies that policy deep copies retain actual user or automatic-trigger identities.
func TestPolicyReplacementPreservesActivityActor(t *testing.T) {
	for _, actor := range []core.ActivityActor{{UserID: "actual-user", Role: "admin", Trigger: "user"}, {Trigger: "system"}} {
		t.Run(actor.Trigger, func(t *testing.T) {
			e, l, s := fixture(t)
			err := s.Replace(httpapi.SecurityConfig{Tokens: map[string]httpapi.Principal{"secret": {Subject: "service-account", ActivityActor: actor, Libraries: map[string]httpapi.LibraryGrant{l.ID: {Operations: []string{"write"}, PolicyVersion: l.PolicyVersion}}}}})
			if err != nil {
				t.Fatal(err)
			}
			h := httpapi.New(e, httpapi.Options{Security: s})
			w := request(t, h, "POST", "/libraries/"+l.ID+"/sources", "secret", "192.0.2.1:1", "", core.ImportSourceInput{RequestID: "actor-source", LibraryID: l.ID, Filename: "source.md", Bytes: []byte("source")})
			if w.Code != 200 {
				t.Fatal(w.Code, w.Body)
			}
			rows, _, err := e.ListLibraryActivity(context.Background(), l.ID, core.ActivityQuery{Action: core.AuditActionKnowledgeCreated})
			if err != nil || len(rows) != 1 {
				t.Fatalf("activity=%+v err=%v", rows, err)
			}
			var details map[string]any
			if err := json.Unmarshal(rows[0].Details, &details); err != nil {
				t.Fatal(err)
			}
			if rows[0].ActorUserID != actor.UserID || rows[0].ActorRole != actor.Role || details["trigger"] != actor.Trigger {
				t.Fatalf("configured activity actor lost: %+v details=%v", rows[0], details)
			}
		})
	}
}
