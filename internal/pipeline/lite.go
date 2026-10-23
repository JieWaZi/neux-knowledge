package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	types "github.com/JieWaZi/neux-knowledge/internal/domain"
	"github.com/JieWaZi/neux-knowledge/internal/store"
	"github.com/JieWaZi/neux-knowledge/internal/wiki"
	"github.com/google/uuid"
)

const (
	wikiMaxDocsPerBatch  = 5
	wikiMapParallel      = 10
	wikiReduceParallel   = 10
	wikiLLMMaxAttempts   = 3
	wikiLLMMaxTokens     = 32768
	wikiLLMBackoffBase   = 2 * time.Second
	wikiFollowUpDelay    = 5 * time.Second
	wikiRateLimitBackoff = 60 * time.Second
	maxContentForWiki    = 32768
)

// liteService holds the current organization scope for one library; separate model calls do not share conversations.
type liteService struct {
	store          *store.Store   // Independent SQLite repository.
	input          Input          // Capability ports for this run.
	job            *types.Job     // Accepted scope and version.
	library        *types.Library // Frozen library configuration.
	commitMu       sync.Mutex     // Serializes only short transactions; model calls still run concurrently.
	identityClaims sync.Map
	identityPages  sync.Map
}

// docIngestResult holds pure document Map results without writing Wiki pages before reduction.
type docIngestResult struct {
	source      *types.SourceVersion    // Immutable source.
	artifact    ReadArtifact            // Completed source reading and indexed chunks.
	updates     map[string][]SlugUpdate // Knowledge updates for each page.
	summary     types.PageMutation      // Document summary page.
	description string                  // One-sentence document summary.
	profile     *types.KnowledgeProfile // Standard RAG document profile.
}

// Run executes a Lite work batch under a library lock; partial failures do not roll back successful pages.
func Run(ctx context.Context, s *store.Store, in Input) (*Result, error) {
	j, err := s.GetJob(ctx, in.JobID)
	if err != nil {
		return nil, err
	}
	var release func()
	for {
		var acquired bool
		release, acquired, err = s.LockProcessor(ctx, "lite/library/"+j.LibraryID, 1)
		if err != nil {
			return nil, err
		}
		if acquired {
			// 库锁与持久队首先后核对，重启时即使后续任务先获得调度也不能插队。
			current, readErr := s.GetJob(ctx, j.ID)
			if readErr != nil {
				release()
				return nil, readErr
			}
			if current.State == "completed" || current.State == "partial_failed" || current.State == "failed" || current.State == "cancelled" {
				release()
				return &Result{JobID: current.ID, State: current.State}, nil
			}
			var ready bool
			j, ready, err = s.ActivateQueuedJob(ctx, j.ID)
			if err != nil {
				release()
				return nil, err
			}
			if ready {
				break
			}
			release()
		}
		// A Lite batch lock conflict must not lose accepted tasks; wait for the holder to exit before reading the authoritative final state.
		if err = waitContext(ctx, 50*time.Millisecond); err != nil {
			return nil, err
		}
	}
	defer release()
	j, err = s.GetJob(ctx, j.ID)
	if err != nil {
		return nil, err
	}
	if j.State == "completed" || j.State == "partial_failed" || j.State == "failed" || j.State == "cancelled" {
		return &Result{JobID: j.ID, State: j.State}, nil
	}
	if err = s.RecoverLiteStages(ctx, j.ID, j.Attempt); err != nil {
		return nil, err
	}
	j, err = s.GetJob(ctx, j.ID)
	if err != nil {
		return nil, err
	}
	if j.CancelRequested {
		return nil, &types.Error{Code: types.ErrCancelled, Operation: "lite.run", Detail: "job cancelled"}
	}
	lib := j.LibrarySnapshot
	if lib == nil {
		lib, err = s.GetLibrary(ctx, j.LibraryID)
		if err != nil {
			return nil, err
		}
	}
	if in.Reader == nil {
		in.Reader = MarkdownReader{}
	}
	if in.Generator == nil {
		return nil, invalid("direct model generator is required")
	}
	svc := &liteService{store: s, input: in, job: j, library: lib}
	if err = s.EnsureOrganizeSourceWork(ctx, j.ID); err != nil {
		return nil, err
	}
	for {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		work, e := s.ListOrganizeSourceWork(ctx, j.ID)
		if e != nil {
			return nil, e
		}
		pending := []*types.OrganizeSourceWork{}
		var earliest int64
		for _, w := range work {
			if w.State != "pending" {
				continue
			}
			if w.NotBefore <= time.Now().UnixMilli() {
				pending = append(pending, w)
			} else if earliest == 0 || w.NotBefore < earliest {
				earliest = w.NotBefore
			}
		}
		if len(pending) == 0 {
			if earliest > 0 {
				if err = waitContext(ctx, time.Until(time.UnixMilli(earliest))); err != nil {
					return nil, err
				}
				continue
			}
			return svc.finalize(ctx, work)
		}
		batchSize := lib.WikiConfig.IngestBatchSizeOrDefault(wikiMaxDocsPerBatch)
		if len(pending) > batchSize {
			pending = pending[:batchSize]
		}
		svc.identityClaims = sync.Map{}
		svc.identityPages = sync.Map{}
		if err = svc.processBatch(ctx, pending); err != nil {
			return nil, err
		}
	}
}

func (s *liteService) key(name string) string {
	return fmt.Sprintf("lite/%s/%d/%s", s.job.ID, s.job.Attempt, name)
}
func (s *liteService) pages(ctx context.Context) ([]*types.WikiPage, error) {
	var result []*types.WikiPage
	for offset := 0; ; offset += 100 {
		items, total, err := s.store.ListPages(ctx, types.PageQuery{LibraryID: s.job.LibraryID, Offset: offset, Limit: 100})
		if err != nil {
			return nil, err
		}
		result = append(result, items...)
		if int64(offset+len(items)) >= total {
			return result, nil
		}
	}
}

// lockPublication places content publication and link maintenance behind one authorization gate, holding the lock through the transaction.
func (s *liteService) lockPublication(ctx context.Context) (func(), error) {
	s.commitMu.Lock()
	if s.input.PublishGuard != nil {
		release, err := s.input.PublishGuard(ctx)
		if err != nil {
			s.commitMu.Unlock()
			return nil, err
		}
		if release == nil {
			s.commitMu.Unlock()
			return nil, invalid("publication guard has no release function")
		}
		return func() { release(); s.commitMu.Unlock() }, nil
	}
	return s.commitMu.Unlock, nil
}
func (s *liteService) publish(ctx context.Context, name string, p *types.Publication, contributionKeys ...string) (*types.Receipt, error) {
	release, err := s.lockPublication(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	rec, err := s.store.PublishLite(ctx, s.key(name), s.job.ID, s.job.Attempt, p, contributionKeys...)
	if err == nil && s.input.OnPublished != nil {
		s.input.OnPublished(ctx, rec)
	}
	return rec, err
}
func (s *liteService) processBatch(ctx context.Context, work []*types.OrganizeSourceWork) error {
	pages, err := s.pages(ctx)
	if err != nil {
		return err
	}
	results := make([]*docIngestResult, len(work))
	failures := make([]error, len(work))
	parallel(ctx, len(work), s.library.WikiConfig.IngestMapParallelOrDefault(wikiMapParallel), func(i int) {
		defer func() {
			if recovered := recover(); recovered != nil {
				failures[i] = invalid(fmt.Sprintf("wiki map panicked: %v", recovered))
			}
		}()
		results[i], failures[i] = s.mapDocument(ctx, work[i], pages)
	})
	if err = ctx.Err(); err != nil {
		return err
	}
	updates := map[string][]SlugUpdate{}
	artifacts := map[string]ReadArtifact{}
	sources := map[string]*types.SourceVersion{}
	summaries := map[string]string{}
	for _, r := range results {
		if r == nil {
			continue
		}
		sources[r.source.ID] = r.source
		artifacts[r.source.ID] = r.artifact
		summaries[r.source.ID] = r.summary.Page.Content
		for slug, list := range r.updates {
			updates[slug] = append(updates[slug], list...)
		}
	}
	updates = s.remapSlugUpdatesByIdentity(ctx, s.job.LibraryID, updates, s.newBatchContext(pages))
	// 仅缺失的来源贡献进入目录规划与 Reduce，已提交页面不重新调用模型。
	for slug, list := range updates {
		pending := []SlugUpdate{}
		for _, u := range list {
			applied, e := s.store.HasLiteContribution(ctx, s.job.ID, hash(marshal(u)))
			if e != nil {
				return e
			}
			if applied {
				continue
			}
			pending = append(pending, u)
		}
		if len(pending) == 0 {
			delete(updates, slug)
		} else {
			updates[slug] = pending
		}
	}
	folders, themes := s.planTaxonomy(ctx, updates)
	if len(themes) > 0 {
		if _, err = s.publish(ctx, "folders/"+hash(marshal(themes)), &types.Publication{Themes: themes}); err != nil {
			return err
		}
	}
	slugs := sortedKeys(updates)
	reduceErrors := make([]error, len(slugs))
	parallel(ctx, len(slugs), s.library.WikiConfig.IngestReduceParallelOrDefault(wikiReduceParallel), func(i int) {
		slug := slugs[i]
		defer func() {
			if recovered := recover(); recovered != nil {
				reduceErrors[i] = invalid(fmt.Sprintf("wiki reduce panicked: %v", recovered))
			}
		}()
		reducer := &upstreamReducer{liteService: s, pages: pages, artifacts: artifacts, sources: sources, updates: updates[slug]}
		batchCtx := s.newBatchContext(pages)
		batchCtx.PlannedFolderID = folders
		_, _, additionFailed, deferred, e := reducer.reduceSlugUpdates(ctx, s.job.LibraryID, slug, updates[slug], batchCtx)
		if e == nil && (deferred || reducer.deferred) {
			e = &types.Error{Code: types.ErrManualProtected, Operation: "lite.page.rows", Detail: slug + ": wiki rewrite dropped table rows"}
		}
		if e == nil && additionFailed {
			e = invalid(slug + ": page generation failed")
		}
		if e != nil {
			e = fmt.Errorf("page %s: %w", slug, e)
		}

		reduceErrors[i] = e
	})
	if err = ctx.Err(); err != nil {
		return err
	}
	failedSources := map[string]error{}
	for i, e := range reduceErrors {
		if e != nil {
			for _, u := range updates[slugs[i]] {
				failedSources[u.SourceVersionID] = e
			}
		}
	}
	// 用已完成计划恢复原显式链接，不再次生成摘要；人工正文或保护片段保持原样。
	if s.library.IndexingStrategy.WikiEnabled {
		currentPages, e := s.pages(ctx)
		if e != nil {
			return e
		}
		for _, result := range results {
			if result == nil {
				continue
			}
			current, e := s.store.GetPageBySlug(ctx, s.job.LibraryID, result.summary.Page.Slug)
			if e != nil {
				var missing *types.Error
				if errors.As(e, &missing) && missing.Code == types.ErrNotFound {
					continue
				}
				return e
			}
			if current.LastEditSource != types.WikiEditSourcePipeline || len(current.ProtectedContent) > 0 {
				continue
			}
			body := current.Content
			if plainSummaryLinks(body) == plainSummaryLinks(result.summary.Page.Content) {
				body = result.summary.Page.Content
			}
			candidates := map[string]bool{}
			for _, slug := range wiki.ParseOutLinks(body) {
				candidates[slug] = true
			}
			cleaned := stripFailedSummaryLinks(body, candidates, currentPages)
			if cleaned != current.Content {
				current.Content = cleaned
				release, e := s.lockPublication(ctx)
				if e != nil {
					return e
				}
				err = s.store.UpdateWikiMaintenance(ctx, s.job.ID, s.job.Attempt, current, current.Version)
				release()
				if err != nil {
					return err
				}
			}
		}
	}

	for i, r := range results {
		if r == nil {
			continue
		}

		if e := failedSources[r.source.ID]; e != nil {
			failures[i] = e
			continue
		}
		if err = s.store.ValidateOrganizeSource(ctx, s.job.ID, s.job.Attempt, r.source.ID); err != nil {
			failures[i] = err
			continue
		}
		c := types.SourceConclusion{SourceVersionID: r.source.ID, State: "processed", Complete: true, Coverage: r.artifact.Coverage, Description: r.description, Profile: r.profile}
		if _, err = s.publish(ctx, "source/"+r.source.ID, &types.Publication{Sources: []types.SourceConclusion{c}}); err != nil {
			return err
		}
	}
	for i, e := range failures {
		if e == nil {
			continue
		}
		if isCancellation(e) {
			return e
		}

		var unavailable *types.Error
		if errors.As(e, &unavailable) && unavailable.Code == types.ErrSourceUnavailable {
			if _, err = s.publish(ctx, "unavailable/"+work[i].SourceVersionID, &types.Publication{Sources: []types.SourceConclusion{{SourceVersionID: work[i].SourceVersionID, State: "failed", Diagnostic: e.Error()}}}); err != nil {
				return err
			}
			continue
		}
		var refused *types.Error
		var modelFailure *modelCallError
		permanentModel := errors.As(e, &modelFailure) && !isTransientLLMError(ctx, modelFailure.cause)
		if permanentModel || (errors.As(e, &refused) && (refused.Code == types.ErrManualProtected || refused.Code == types.ErrCandidateInvalid)) {
			if _, err = s.publish(ctx, "refused/"+work[i].SourceVersionID, &types.Publication{Sources: []types.SourceConclusion{{SourceVersionID: work[i].SourceVersionID, State: "failed", Diagnostic: e.Error()}}}); err != nil {
				return err
			}
			continue
		}
		var reading *sourceReadFailure
		if errors.As(e, &reading) {
			if _, err = s.publish(ctx, "reading-failure/"+work[i].SourceVersionID, &types.Publication{Sources: []types.SourceConclusion{{SourceVersionID: work[i].SourceVersionID, State: "failed", Coverage: reading.coverage, Diagnostic: reading.diagnostic}}}); err != nil {
				return err
			}
			continue
		}
		delay := wikiFollowUpDelay
		if isRateLimitedGeneration(e.Error()) {
			delay = wikiRateLimitBackoff
		}
		w := work[i]
		_, err = s.store.DeferOrganizeSourceWork(ctx, types.SourceWorkActionInput{RequestID: s.key(fmt.Sprintf("retry/%s/%d", w.SourceVersionID, w.Version)), JobID: s.job.ID, JobAttempt: s.job.Attempt, SourceVersionID: w.SourceVersionID, ExpectedVersion: w.Version, Diagnostic: e.Error(), NotBefore: time.Now().Add(delay).UnixMilli()})
		if err != nil {
			return err
		}
		if s.input.OnProgress != nil {
			s.input.OnProgress(ctx)
		}
	}
	return nil
}

func (s *liteService) mapDocument(ctx context.Context, w *types.OrganizeSourceWork, pages []*types.WikiPage) (*docIngestResult, error) {
	if err := s.store.ValidateOrganizeSource(ctx, s.job.ID, s.job.Attempt, w.SourceVersionID); err != nil {
		return nil, err
	}
	started, err := s.store.StartOrganizeSourceWork(ctx, s.job.ID, s.job.Attempt, w.SourceVersionID)
	if err != nil {
		return nil, err
	}
	if started && s.input.OnProgress != nil {
		s.input.OnProgress(ctx)
	}
	source, err := s.store.GetSource(ctx, s.job.LibraryID, w.SourceVersionID)
	if err != nil {
		return nil, err
	}
	if payload, e := s.store.LoadLitePlan(ctx, s.job.ID, s.job.Attempt, source.ID); e != nil {
		return nil, e
	} else if payload != "" {
		var plan documentPlan
		if e = json.Unmarshal([]byte(payload), &plan); e != nil {
			return nil, invalid("stored document plan is invalid")
		}
		if plan.Artifact.SourceVersionID != source.ID || plan.Artifact.SHA256 != source.SHA256 {
			return nil, invalid("stored document plan source changed")
		}
		return &docIngestResult{source: source, artifact: plan.Artifact, updates: plan.Updates, summary: plan.Summary, description: plan.Description, profile: plan.Profile}, nil
	}
	raw, err := s.store.ReadRaw(ctx, s.job.LibraryID, source.ID)
	if err != nil {
		return nil, err
	}
	request := ReadRequest{SourceID: source.SourceID, SourceVersionID: source.ID, SHA256: source.SHA256, MIME: source.MIME, Filename: source.Filename, Raw: raw, Scope: "all", MaxRunes: s.input.MaxReadRunes}
	readInput := marshal(map[string]any{"sourceVersionId": source.ID, "sha256": source.SHA256, "mime": source.MIME, "scope": "all", "maxRunes": s.input.MaxReadRunes})
	reading, err := s.store.BeginModelStage(ctx, s.job.ID, s.job.Attempt, "read/"+source.ID, "reading", []string{source.ID}, readInput)
	if err != nil {
		return nil, err
	}
	artifact, readErr := s.input.Reader.Read(ctx, request)
	if readErr == nil {
		readErr = validateRead(request, artifact)
	}
	diagnostic := ""
	if readErr != nil {
		diagnostic = readErr.Error()
	} else if artifact.Status != "complete" {
		diagnostic = artifact.Diagnostic
	}
	cleanup, closeCleanup := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	finishErr := s.store.FinishModelStage(cleanup, reading.ID, string(marshal(map[string]any{"status": artifact.Status, "coverage": artifact.Coverage, "diagnostic": artifact.Diagnostic})), "completed", diagnostic, nil, nil)
	closeCleanup()
	if readErr != nil {
		return nil, readErr
	}
	if finishErr != nil {
		return nil, finishErr
	}
	if artifact.Status != "complete" {
		return nil, &sourceReadFailure{diagnostic: "source reading incomplete: " + artifact.Diagnostic, coverage: artifact.Coverage}
	}
	content := reconstructContent(artifact.Blocks)
	if !hasSufficientTextContent(content) {
		return nil, &sourceReadFailure{diagnostic: "source has insufficient text content", coverage: artifact.Coverage}
	}
	if s.input.PrepareDocument != nil {
		artifact, err = s.input.PrepareDocument(ctx, source.ID, artifact)
		if err != nil {
			return nil, err
		}
		if artifact.SourceVersionID != source.ID || artifact.SHA256 != source.SHA256 || artifact.Status != "complete" {
			return nil, invalid("indexed source identity or completeness changed")
		}
	} else if s.library.IndexingStrategy.VectorEnabled || !s.library.IndexingStrategy.WikiEnabled {
		return nil, invalid("enabled RAG indexing requires a real indexing port")
	}
	result := &docIngestResult{source: source, artifact: artifact, updates: map[string][]SlugUpdate{}}
	if !s.library.IndexingStrategy.WikiEnabled {
		prompt, e := wiki.RenderDocumentSummaryPrompt(language(s.library.WikiLanguage), sampleLongContent(content, 8192))
		if e != nil {
			return nil, e
		}
		prompt.System = wiki.AppendCustomPromptInstructions(prompt.System, s.input.Instruction, "document_summary")
		output, e := s.generatePrompt(ctx, "document_summary", source.ID, []string{source.ID}, prompt)
		if e != nil {
			return nil, e
		}
		profile, e := parseDocumentSummaryOutput(output)
		if e != nil {
			return nil, e
		}
		result.description = profile.Summary
		result.profile = profile.Profile
		return result, nil
	}
	mapped, updates, err := s.mapOneDocument(ctx, source, artifact, pages)
	if err != nil {
		return nil, err
	}
	if mapped == nil {
		return nil, invalid("document map produced no content")
	}
	for _, update := range updates {
		update.SourceVersionID = source.ID
		if update.Type == "summary" {
			page, expected := newPage(s.job.LibraryID, update.Slug, "summary", pages)
			page.Title = update.DocTitle + " - Summary"
			page.Content = update.SummaryBody
			page.Summary = update.SummaryLine
			page.WikiLanguage = s.library.WikiLanguage
			page.Evidence.Sources = []types.SourceRef{documentRef(source)}
			page.ChunkRefs = nil
			result.summary = types.PageMutation{Page: page, ExpectedVersion: expected}
			result.description = update.SummaryLine
			result.updates[update.Slug] = append(result.updates[update.Slug], update)
		} else {
			result.updates[update.Slug] = append(result.updates[update.Slug], update)
		}
	}
	plan := documentPlan{Artifact: result.artifact, Updates: result.updates, Summary: result.summary, Description: result.description, Profile: result.profile}
	if err = s.store.SaveLitePlan(ctx, s.job.ID, s.job.Attempt, source.ID, string(marshal(plan))); err != nil {
		return nil, err
	}
	return result, nil
}

// documentPlan 是可信管线的恢复结果，不是额外模型调用或新的知识事实。
type documentPlan struct {
	Artifact    ReadArtifact            // 已完成的原件阅读与分块。
	Updates     map[string][]SlugUpdate // 按页面整理的不可变来源贡献。
	Summary     types.PageMutation      // 原件摘要的初始发布内容。
	Description string                  // 来源的一句话说明。
	Profile     *types.KnowledgeProfile // 非 Wiki 文档的知识档案。
}

func (s *liteService) contributionKey(update SlugUpdate) string {
	return "lite/" + s.job.ID + "/applied/" + hash(marshal(update))
}

// combinedExtraction preserves the upstream JSON contract without adding a host result wrapper.
type combinedExtraction struct {
	Entities []extractedItem `json:"entities"` // Entity candidates.
	Concepts []extractedItem `json:"concepts"` // Concept candidates.
}

func (s *liteService) classifyUpstream(ctx context.Context, sourceID, candidateXML string, chunks []*Chunk, lang string) (map[string][]string, []newSlugFromCitation, int) {
	batches := splitChunksIntoCitationBatches(chunks)
	results := make([]citationBatchResult, len(batches))
	if strings.TrimSpace(candidateXML) == "" {
		return nil, nil, 0
	}
	parallel(ctx, len(batches), maxCitationBatchConcurrency, func(i int) {
		raw, err := s.generate(ctx, "cite", sourceID, []string{sourceID}, wiki.WikiChunkCitationPrompt, map[string]string{"CandidateSlugs": candidateXML, "ChunksXML": renderChunksXML(batches[i]), "Language": lang})
		if err == nil {
			_ = json.Unmarshal([]byte(cleanLLMJSON(raw)), &results[i])
		}
	})
	set := map[string]map[string]bool{}
	var discovered []newSlugFromCitation
	for i, result := range results {
		for slug, handles := range result.Citations {
			if slug == "" {
				continue
			}
			if set[slug] == nil {
				set[slug] = map[string]bool{}
			}
			for _, h := range handles {
				if id, ok := batches[i].handles.Resolve(h); ok {
					set[slug][id] = true
				}
			}
		}
		for _, item := range result.NewSlugs {
			if item.Slug == "" || item.Name == "" {
				continue
			}
			real := []string{}
			for _, h := range item.SourceChunks {
				if id, ok := batches[i].handles.Resolve(h); ok {
					real = append(real, id)
				}
			}
			item.SourceChunks = real
			discovered = append(discovered, item)
		}
	}
	order := map[string]int{}
	for _, c := range chunks {
		order[c.ID] = c.ChunkIndex
	}
	out := map[string][]string{}
	for slug, ids := range set {
		for id := range ids {
			out[slug] = append(out[slug], id)
		}
		sort.SliceStable(out[slug], func(i, j int) bool { return order[out[slug][i]] < order[out[slug][j]] })
	}
	return out, discovered, len(batches)
}

// citationBatchResult preserves the upstream citation response contract.
type citationBatchResult struct {
	Citations map[string][]string   `json:"citations"` // Knowledge slugs mapped to short chunk handles.
	NewSlugs  []newSlugFromCitation `json:"new_slugs"` // Additional knowledge discovered while traversing citations.
}

func (s *liteService) contentInstructions() string {
	base := ""
	if s.library.WikiConfig != nil {
		base = s.library.WikiConfig.ContentInstructions
	}
	return combineInstructions(base, s.input.Instruction)
}
func (s *liteService) extractionInstructions() string {
	base := ""
	if s.library.WikiConfig != nil {
		base = s.library.WikiConfig.ExtractionInstructions
	}
	return combineInstructions(base, s.input.Instruction)
}
func combineInstructions(library, task string) string {
	library = strings.TrimSpace(library)
	task = strings.TrimSpace(task)
	if library == "" {
		return task
	}
	if task == "" {
		return library
	}
	return library + "\n\n" + task
}
func language(code string) string {
	if strings.HasPrefix(strings.ToLower(code), "en") {
		return "English"
	}
	return "Chinese"
}
func reconstructContent(chunks []*Chunk) string {
	filtered := make([]*Chunk, 0, len(chunks))
	for _, c := range chunks {
		if c != nil && (c.ChunkType == "text" || c.ChunkType == "") {
			filtered = append(filtered, c)
		}
	}
	return MergeTextChunks(filtered, "\n")
}
func truncateString(s string, max int) string {
	r := []rune(s)
	if len(r) > max {
		return string(r[:max])
	}
	return s
}
func sortedKeys[T any](m map[string]T) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
func sourceIDsFor(updates []SlugUpdate) []string {
	out := []string{}
	for _, u := range updates {
		out = appendUniqueString(out, u.SourceVersionID)
	}
	sort.Strings(out)
	return out
}
func documentRef(source *types.SourceVersion) types.SourceRef {
	return types.SourceRef{SourceID: source.SourceID, SourceVersionID: source.ID, SHA256: source.SHA256, ClaimKind: "document_context"}
}
func newPage(lib, slug, kind string, pages []*types.WikiPage) (types.WikiPage, int) {
	for _, p := range pages {
		if p.Slug == slug {
			return clonePage(*p), p.Version
		}
	}
	return types.WikiPage{ID: uuid.NewSHA1(uuid.NameSpaceURL, []byte(lib+"/"+slug)).String(), KnowledgeBaseID: lib, Slug: slug, PageType: kind, Status: "published"}, 0
}
func clonePage(p types.WikiPage) types.WikiPage {
	var result types.WikiPage
	_ = json.Unmarshal(marshal(p), &result)
	return result
}
func parallel(ctx context.Context, count, limit int, fn func(int)) {
	if limit < 1 {
		limit = 1
	}
	slots := make(chan struct{}, limit)
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			wg.Wait()
			return
		}
		wg.Add(1)
		go func(index int) { defer wg.Done(); defer func() { <-slots }(); fn(index) }(i)
	}
	wg.Wait()
}
func waitContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
func isCancellation(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var e *types.Error
	return errors.As(err, &e) && e.Code == types.ErrCancelled
}

func (s *liteService) granularity() types.WikiExtractionGranularity {
	if s.library.WikiConfig == nil {
		return types.WikiExtractionStandard
	}
	return s.library.WikiConfig.ExtractionGranularity.Normalize()
}

// sourceReadFailure is a confirmed reader capability limitation that repeated model calls cannot resolve.
type sourceReadFailure struct {
	diagnostic string          // Deterministic failure description.
	coverage   json.RawMessage // Actual reader coverage.
}

func (e *sourceReadFailure) Error() string { return e.diagnostic }
