package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/JieWaZi/neux-knowledge/core"
)

// Handler assembles only knowledge routes and owns no models or host administration routes.
type Handler struct {
	engine  *core.Engine // Sole authoritative core instance with a host-managed lifecycle.
	options Options      // Independent authentication and optional execution use cases.
}

func New(e *core.Engine, o Options) http.Handler {
	o.Profiles = append([]Profile(nil), o.Profiles...)
	return &Handler{engine: e, options: o}
}
func respond(w http.ResponseWriter, v any, err error) {
	if err != nil {
		status := 500
		code := "internal_error"
		var api *APIError
		var ce *core.Error
		if errors.As(err, &api) {
			status = api.Status
			code = api.Code
		} else if errors.As(err, &ce) {
			code = string(ce.Code)
			switch ce.Code {
			case core.ErrInvalid:
				status = 400
			case core.ErrNotFound:
				status = 404
			case core.ErrScopeViolation:
				status = 403
			case core.ErrVersionConflict, core.ErrIdempotencyConflict, core.ErrCancelled, core.ErrStaleAttempt, core.ErrManualProtected, core.ErrSourceUnavailable:
				status = 409
			case core.ErrCandidateInvalid, core.ErrExecutionNotSuccessful:
				status = 422
			case core.ErrDatabaseBusy:
				status = 503
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(APIError{Code: code})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}
func invalid() error { return &APIError{Code: "invalid", Status: 400} }
func decode(w http.ResponseWriter, r *http.Request, out any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 48<<20)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if d.Decode(out) != nil {
		return invalid()
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return invalid()
	}
	return nil
}
func pagination(r *http.Request) (int, int, error) {
	return paginationLimit(r, 100, 50)
}

func paginationLimit(r *http.Request, maximum, defaultLimit int) (int, int, error) {
	o, l := 0, defaultLimit
	var err error
	if s := r.URL.Query().Get("offset"); s != "" {
		o, err = strconv.Atoi(s)
		if err != nil {
			return 0, 0, invalid()
		}
	}
	if s := r.URL.Query().Get("limit"); s != "" {
		l, err = strconv.Atoi(s)
		if err != nil {
			return 0, 0, invalid()
		}
	}
	if o < 0 || l < 1 || l > maximum {
		return 0, 0, invalid()
	}
	return o, l, nil
}
func same(got, want string) error {
	if got != want {
		return &APIError{Code: "scope_violation", Status: 403}
	}
	return nil
}
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.engine == nil {
		respond(w, nil, &APIError{Code: "service_unavailable", Status: 503})
		return
	}
	if !strings.HasPrefix(r.URL.Path, BasePath+"/") {
		http.NotFound(w, r)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, BasePath+"/")
	parts := strings.Split(path, "/")
	if len(parts) == 0 || parts[0] != "libraries" {
		http.NotFound(w, r)
		return
	}
	token := ""
	forwarded := strings.Join(r.Header.Values("X-Forwarded-For"), ",")
	if header := r.Header.Get("Authorization"); strings.HasPrefix(header, "Bearer ") {
		token = strings.TrimPrefix(header, "Bearer ")
	}
	lib, op := "", "list"
	if len(parts) > 1 {
		lib = parts[1]
		op = "read"
	}
	creationRecovery := len(parts) == 2 && parts[1] == "creation-receipts" && r.Method == http.MethodGet
	if creationRecovery {
		// Creation receipts have no pre-existing library scope and still require current creation permission and write-origin policy checks.
		lib, op = "", "create"
	}
	if r.Method != http.MethodGet {
		op = "write"
		if len(parts) == 1 {
			op = "create"
		}
		if len(parts) > 2 {
			switch parts[2] {
			case "organize":
				op = "organize"
			case "maintenance":
				op = "maintain"
			case "jobs":
				op = "organize"
			case "source-purges", "delete", "deletion":
				op = "purge"
			}
		}
	}
	// Activity history requires configuration-edit authorization; ordinary read-only sharing does not expose actor identities or settings history.
	if len(parts) == 3 && parts[2] == "activity" && r.Method == http.MethodGet {
		op = "write"
	}
	p, g, release, err := h.options.Security.acquire(r.Context(), token, lib, op, r.RemoteAddr, forwarded)
	if err != nil {
		respond(w, nil, err)
		return
	}
	actor := p.ActivityActor
	if actor.UserID == "" && actor.Trigger != "system" {
		actor.UserID = p.Subject
	}
	r = r.WithContext(core.WithActivityActor(r.Context(), actor))
	locked := true
	defer func() {
		if locked {
			release()
		}
	}()
	if r.Method != "GET" && r.Method != "POST" {
		respond(w, nil, &APIError{Code: "method_not_allowed", Status: 405})
		return
	}
	currentPolicy := int64(0)
	if lib != "" {
		l, e := h.engine.GetLibrary(r.Context(), lib)
		deletionAccess := len(parts) > 2 && (parts[2] == "delete" || parts[2] == "deletion" || parts[2] == "receipts")
		var ce *core.Error
		if e != nil && deletionAccess && errors.As(e, &ce) && ce.Code == core.ErrNotFound {
			status, deletionErr := h.engine.GetLibraryDeletion(r.Context(), lib)
			if deletionErr == nil {
				l = &core.Library{ID: lib, PolicyVersion: status.PolicyVersion}
				e = nil
			}
		}
		if e != nil {
			respond(w, nil, e)
			return
		}
		if op != "read" && l.PolicyVersion != g.PolicyVersion {
			respond(w, nil, &APIError{Code: "policy_changed", Status: 403})
			return
		}
		currentPolicy = l.PolicyVersion
	}
	auth := Authorization{Subject: p.Subject, Permission: core.PermissionSnapshot{PolicyVersion: g.PolicyVersion, Scope: append([]string(nil), g.Scope...), Operations: append([]string(nil), g.Operations...)}}
	// Recovery identities are server-generated only; restarting a job still requires verification of the original credential and actual origin.
	auth.Recovery = RecoveryIdentity{Subject: auth.Subject, CredentialSHA256: credentialHash(token), LibraryID: lib, Operation: op, RemoteAddr: r.RemoteAddr, ForwardedFor: forwarded, Permission: auth.Permission}
	auth.PublishGuard = h.options.Security.RestoreGuard(h.engine, auth.Recovery)
	if creationRecovery {
		v, e := h.creationReceipt(r.Context(), r.URL.Query().Get("requestId"), p.Subject)
		respond(w, v, e)
		return
	}
	if len(parts) == 1 {
		if r.Method == "GET" {
			h.libraries(w, r, p)
			return
		}
		var in core.CreateLibraryInput
		if err = decode(w, r, &in); err == nil {
			var v *core.Receipt
			// Creation activity must record the authenticated principal as the persistent owner for authorized recovery after a lost response.
			actor.UserID = p.Subject
			creationContext := core.WithActivityActor(r.Context(), actor)
			v, err = h.engine.CreateLibrary(creationContext, in)
			if err == nil {
				v, err = h.creationReceipt(creationContext, in.RequestID, p.Subject)
			}
			respond(w, v, err)
		} else {
			respond(w, nil, err)
		}
		return
	}
	if len(parts) == 2 {
		if r.Method == "GET" {
			v, e := h.engine.GetLibrary(r.Context(), lib)
			respond(w, v, e)
			return
		}
		var in core.UpdateLibraryInput
		if err = decode(w, r, &in); err == nil {
			err = same(in.LibraryID, lib)
		}
		if err == nil && in.PolicyVersion != g.PolicyVersion {
			err = invalid()
		}
		if err == nil {
			v, e := h.engine.UpdateLibrary(r.Context(), in)
			respond(w, v, e)
		} else {
			respond(w, nil, err)
		}
		return
	}
	if h.activityRoute(w, r, parts, lib) {
		return
	}
	if parts[2] == "capabilities" && len(parts) == 3 && r.Method == "GET" {
		policy := h.options.Security.config.WriteIPPolicy
		ipOK := true
		if policy.Enabled {
			ip, valid := clientIP(r.RemoteAddr, forwarded, policy)
			ipOK = valid && inPrefixes(ip, policy.Allowed)
		}
		operations := map[string]bool{}
		details := map[string]CapabilityStatus{}
		for _, name := range []string{"read", "write", "rawOnly", "organize", "rebuild", "maintain", "history", "themes", "themeWrite", "sourceRead", "sourceDownload", "sourceContent", "activity", "atomicRestore", "sourceArchive", "sourcePurge", "libraryDelete", "keywordWithoutEmbedding", "writeIpPolicy"} {
			status := CapabilityStatus{Supported: true, Permission: "allowed", Condition: "ready"}
			op := "read"
			switch name {
			case "write", "rawOnly", "themeWrite", "activity", "atomicRestore", "sourceArchive":
				op = "write"
			case "organize", "maintain":
				op = name
			case "rebuild":
				op = "organize"
			case "sourcePurge", "libraryDelete":
				op = "purge"
			}
			if !contains(g.Operations, op) {
				status.Permission = "denied"
			}
			if op != "read" {
				if !ipOK {
					status.Condition = "write_ip_forbidden"
				}
				if currentPolicy != g.PolicyVersion {
					status.Condition = "policy_changed"
				}
			}
			if name == "organize" || name == "rebuild" || name == "maintain" {
				if h.options.Execution == nil {
					status.Supported = false
					status.Condition = "execution_unavailable"
				} else if len(h.options.Profiles) == 0 {
					status.Condition = "profile_unavailable"
				}
			}
			details[name] = status
			operations[name] = status.Supported && status.Permission == "allowed" && status.Condition == "ready"
		}
		respond(w, Capabilities{Profile: "neux-knowledge-v1", Profiles: h.options.Profiles, Details: details, Operations: operations}, nil)
		return
	}
	if h.wikiMaintenanceRoute(w, r, parts, lib, currentPolicy, p.Subject) {
		return
	}
	if h.libraryDeletionRoute(w, r, parts, lib) {
		return
	}
	if h.auditExportRoute(w, r, parts, lib) {
		return
	}
	if h.sourcePurgeRoute(w, r, parts, lib) {
		return
	}
	if r.Method == "POST" && (parts[2] == "organize" || parts[2] == "maintenance") {
		if h.options.Execution == nil {
			respond(w, nil, &APIError{Code: "unsupported", Status: 501})
			return
		}
		// Release the lock after entry validation; asynchronous host execution reacquires PublishGuard before each publication.
		release()
		locked = false
		h.execute(w, r, parts, lib, auth)
		return
	}
	if r.Method == "GET" {
		h.read(w, r, parts, lib)
		return
	}
	h.write(w, r, parts, lib)
}
func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
func (h *Handler) libraries(w http.ResponseWriter, r *http.Request, p Principal) {
	o, l, err := pagination(r)
	if err != nil {
		respond(w, nil, err)
		return
	}
	items := []*core.Library{}
	ids := []string{}
	for id, g := range p.Libraries {
		if contains(g.Operations, "read") {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		v, e := h.engine.GetLibrary(r.Context(), id)
		if e == nil {
			items = append(items, v)
		} else {
			var ce *core.Error
			if !errors.As(e, &ce) || ce.Code != core.ErrNotFound {
				respond(w, nil, e)
				return
			}
		}
	}
	total := len(items)
	start := min(o, total)
	end := min(start+l, total)
	respond(w, List[*core.Library]{Items: items[start:end], Total: int64(total), Offset: o, Limit: l}, nil)
}
func (h *Handler) read(w http.ResponseWriter, r *http.Request, p []string, lib string) {
	ctx := r.Context()
	if len(p) == 3 && p[2] == "page-directory" {
		o, l, err := paginationLimit(r, core.PageDirectoryMaxLimit, core.PageDirectoryMaxLimit)
		if err != nil {
			respond(w, nil, err)
			return
		}
		items, total, err := h.engine.ListPageDirectory(ctx, lib, o, l)
		respond(w, List[*core.PageDirectoryEntry]{Items: items, Total: total, Offset: o, Limit: l}, err)
		return
	}
	o, l, err := pagination(r)
	if err != nil {
		respond(w, nil, err)
		return
	}
	switch p[2] {
	case "pages":
		if len(p) == 3 {
			q := core.PageQuery{LibraryID: lib, Query: r.URL.Query().Get("query"), PageType: r.URL.Query().Get("pageType"), Status: r.URL.Query().Get("status"), Offset: o, Limit: l}
			if r.URL.Query().Has("themeId") {
				v := r.URL.Query().Get("themeId")
				q.ThemeID = &v
			}
			v, n, e := h.engine.ListPages(ctx, q)
			respond(w, List[*core.Page]{v, n, o, l}, e)
			return
		}
		if len(p) == 4 {
			v, e := h.engine.GetPage(ctx, lib, p[3])
			respond(w, v, e)
			return
		}
		if len(p) == 5 && p[4] == "revisions" {
			v, n, e := h.engine.ListRevisions(ctx, lib, p[3], o, l)
			respond(w, List[*core.Revision]{v, n, o, l}, e)
			return
		}
		if len(p) == 6 && p[4] == "revisions" {
			version, e := strconv.Atoi(p[5])
			if e != nil {
				respond(w, nil, invalid())
				return
			}
			v, e := h.engine.GetRevision(ctx, lib, p[3], version)
			respond(w, v, e)
			return
		}
	case "page-by-slug":
		if len(p) == 3 {
			v, e := h.engine.GetPageBySlug(ctx, lib, r.URL.Query().Get("slug"))
			respond(w, v, e)
			return
		}
	case "themes":
		if len(p) == 3 {
			v, e := h.engine.ListThemes(ctx, lib)
			respond(w, v, e)
			return
		}
	case "sources":
		if len(p) == 3 {
			v, n, e := h.engine.ListSourcesQuery(ctx, lib, core.SourceQuery{Offset: o, Limit: l, Query: r.URL.Query().Get("query"), State: r.URL.Query().Get("state"), Sort: r.URL.Query().Get("sort")})
			for _, s := range v {
				s.RawKey = ""
			}
			respond(w, List[*core.SourceVersion]{v, n, o, l}, e)
			return
		}
		if len(p) == 4 {
			v, e := h.engine.GetSource(ctx, lib, p[3])
			if e == nil && v != nil {
				projected, projectionErr := h.engine.SourceActivity(ctx, lib, []*core.SourceVersion{v})
				e = projectionErr
				if e == nil {
					v = projected[0]
				}
			}
			if v != nil {
				v.RawKey = ""
			}
			respond(w, v, e)
			return
		}
		if len(p) == 5 && p[4] == "chunks" {
			v, e := h.engine.ListDocumentChunks(ctx, lib, p[3])
			respond(w, v, e)
			return
		}
		if len(p) == 5 && p[4] == "raw" {
			s, e := h.engine.GetSource(ctx, lib, p[3])
			if e != nil {
				respond(w, nil, e)
				return
			}
			b, e := h.engine.ReadRaw(ctx, lib, p[3])
			if e != nil {
				respond(w, nil, e)
				return
			}
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": s.Filename}))
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			_, _ = w.Write(b)
			return
		}
	case "jobs":
		if len(p) == 3 {
			v, n, e := h.engine.ListJobs(ctx, lib, o, l)
			respond(w, List[*core.Job]{v, n, o, l}, e)
			return
		}
		if len(p) == 4 {
			v, e := h.engine.GetJob(ctx, p[3])
			if e == nil {
				e = same(v.LibraryID, lib)
			}
			if e != nil {
				v = nil
			}
			respond(w, v, e)
			return
		}
	case "receipts":
		if len(p) == 3 && r.URL.Query().Has("requestId") {
			v, e := h.engine.GetReceipt(ctx, r.URL.Query().Get("requestId"))
			if e == nil {
				e = same(v.LibraryID, lib)
			}
			if e != nil {
				v = nil
			}
			respond(w, v, e)
			return
		}
		if len(p) == 4 {
			v, e := h.engine.GetReceipt(ctx, p[3])
			if e == nil {
				e = same(v.LibraryID, lib)
			}
			if e != nil {
				v = nil
			}
			respond(w, v, e)
			return
		}
	}
	respond(w, nil, &APIError{Code: "not_found", Status: 404})
}
func (h *Handler) write(w http.ResponseWriter, r *http.Request, p []string, lib string) {
	ctx := r.Context()
	var v *core.Receipt
	var err error
	switch {
	case p[2] == "sources" && len(p) == 3:
		var in core.ImportSourceInput
		err = decode(w, r, &in)
		if err == nil {
			err = same(in.LibraryID, lib)
		}
		if err == nil {
			v, err = h.engine.ImportSource(ctx, in)
		}
	case p[2] == "sources" && len(p) == 5 && p[4] == "archive":
		var in core.ArchiveSourceInput
		err = decode(w, r, &in)
		if err == nil {
			err = same(in.LibraryID, lib)
		}
		if err == nil {
			err = same(in.SourceVersionID, p[3])
		}
		if err == nil {
			v, err = h.engine.ArchiveSource(ctx, in)
		}
	case p[2] == "pages" && len(p) == 3:
		var in core.WritePageInput
		err = decode(w, r, &in)
		if err == nil {
			err = same(in.LibraryID, lib)
		}
		if err == nil {
			v, err = h.engine.WritePage(ctx, in)
		}
	case p[2] == "pages" && len(p) == 5 && p[4] == "restore":
		var in core.RestorePageInput
		err = decode(w, r, &in)
		if err == nil {
			err = same(in.LibraryID, lib)
		}
		if err == nil {
			err = same(in.PageID, p[3])
		}
		if err == nil {
			v, err = h.engine.RestorePage(ctx, in)
		}
	case p[2] == "themes" && len(p) == 3:
		var in core.WriteThemeInput
		err = decode(w, r, &in)
		if err == nil {
			err = same(in.LibraryID, lib)
		}
		if err == nil {
			v, err = h.engine.WriteTheme(ctx, in)
		}
	default:
		err = &APIError{Code: "not_found", Status: 404}
	}
	respond(w, v, err)
}
func (h *Handler) execute(w http.ResponseWriter, r *http.Request, p []string, lib string, auth Authorization) {
	var v *core.Receipt
	var err error
	requestID := ""
	switch {
	case p[2] == "organize" && len(p) == 3:
		var in OrganizeRequest
		err = decode(w, r, &in)
		if err == nil {
			err = same(in.LibraryID, lib)
		}
		if err == nil && (in.RequestID == "" || in.ProfileID == "" || len(in.Instruction) > 65536) {
			err = invalid()
		}
		if err == nil {
			err = h.profile(in.ProfileID)
		}
		if err == nil {
			requestID = in.RequestID
			v, err = h.options.Execution.Organize(r.Context(), in, auth)
		}
	case p[2] == "maintenance" && len(p) == 3:
		var in MaintenanceRequest
		err = decode(w, r, &in)
		if err == nil {
			err = same(in.LibraryID, lib)
		}
		if err == nil && (in.RequestID == "" || in.ProfileID == "" || in.PageID == "" || in.ExpectedPageVersion < 1 || strings.TrimSpace(in.Instruction) == "" || len(in.Instruction) > 65536) {
			err = invalid()
		}
		if err == nil {
			err = h.profile(in.ProfileID)
		}
		if err == nil {
			var page *core.Page
			page, err = h.engine.GetPage(r.Context(), lib, in.PageID)
			if err == nil && page.Version != in.ExpectedPageVersion {
				err = &APIError{Code: "version_conflict", Status: 409}
			}
		}
		if err == nil {
			requestID = in.RequestID
			v, err = h.options.Execution.Maintain(r.Context(), in, auth)
		}

	default:
		err = &APIError{Code: "not_found", Status: 404}
	}
	if err == nil && (v == nil || v.LibraryID != lib || v.RequestID != requestID) {
		v = nil
		err = &APIError{Code: "execution_contract_invalid", Status: 502}
	}
	if err == nil {
		// Success reads only receipts already persisted by the core; execution callbacks cannot fabricate a successful outcome.
		persisted, e := h.engine.GetReceipt(r.Context(), requestID)
		if e != nil || persisted.LibraryID != lib {
			v = nil
			err = &APIError{Code: "execution_contract_invalid", Status: 502}
		} else {
			v = persisted
		}
	}
	respond(w, v, err)
}

func (h *Handler) profile(id string) error {
	for _, p := range h.options.Profiles {
		if p.ID == id && id != "" {
			return nil
		}
	}
	return &APIError{Code: "unsupported_profile", Status: 501}
}
