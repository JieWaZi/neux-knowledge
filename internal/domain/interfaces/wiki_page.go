package interfaces

import (
	"context"
	"github.com/JieWaZi/neux-knowledge/internal/domain"
)

type WikiPageRepository interface {
	// Create inserts a new wiki page record.
	Create(ctx context.Context, page *types.WikiPage) error

	// Update rewrites a wiki page record with optimistic locking and
	// unconditionally increments `version`. Callers are responsible for
	// deciding whether the edit is user-visible — the service layer uses
	// UpdateMeta for bookkeeping-only writes instead.
	Update(ctx context.Context, page *types.WikiPage) error

	// UpdateMeta updates bookkeeping / provenance fields (in/out links,
	// status, source_refs, chunk_refs, page_metadata, updated_at) without
	// touching `version`. Safe for link maintenance, re-ingest with an
	// unchanged body, and status-only transitions.
	UpdateMeta(ctx context.Context, page *types.WikiPage) error

	// UpdateAutoLinkedContent rewrites `content`, `out_links` and
	// `updated_at` in place while leaving `version` untouched. Intended for
	// machine-only link markup changes (cross-link injection / dead-link
	// cleanup) so the first-ingest page doesn't jump to v2 just because the
	// post-processor added a `[[...]]` wrapper.
	UpdateAutoLinkedContent(ctx context.Context, page *types.WikiPage) error

	// GetByID retrieves a wiki page by its unique ID.
	GetByID(ctx context.Context, id string) (*types.WikiPage, error)

	// GetBySlug retrieves a wiki page by slug within a knowledge base.
	GetBySlug(ctx context.Context, kbID string, slug string) (*types.WikiPage, error)

	// List retrieves wiki pages with filtering and pagination.
	List(ctx context.Context, req *types.WikiPageListRequest) ([]*types.WikiPage, int64, error)

	// ListByType retrieves all wiki pages of a given type within a knowledge base.
	ListByType(ctx context.Context, kbID string, pageType string) ([]*types.WikiPage, error)

	// ListByTypeLight returns a paginated window of lightweight entries
	// (slug/title/summary only) for the given page_type plus the total
	// non-archived count. Used by the structured index API so reads do not
	// have to materialize TEXT content for every wiki_pages row.
	ListByTypeLight(ctx context.Context, kbID string, pageType string, limit int, offset int) ([]types.WikiIndexEntry, int64, error)

	// ListBySourceRef retrieves all wiki pages that reference a given source knowledge ID.
	ListBySourceRef(ctx context.Context, kbID string, sourceKnowledgeID string) ([]*types.WikiPage, error)

	// ListSlugsBySourceRef returns just the slugs of pages that reference
	// the given knowledge id. Same predicate as ListBySourceRef but
	// projected to a single column for the wiki ingest pipeline's
	// "before" snapshot path.
	ListSlugsBySourceRef(ctx context.Context, kbID string, sourceKnowledgeID string) ([]string, error)

	// ListBySlugs returns lightweight projections (slug/title/page_type/
	// status/aliases/out_links) for the given slugs in one IN query.
	// Used by the lazy fetcher path in wiki ingest's Map/Reduce phases.
	ListBySlugs(ctx context.Context, kbID string, slugs []string) (map[string]*types.WikiPageLite, error)

	// ListSummariesByKnowledgeIDs returns summary-page content keyed by
	// the knowledge id that authored it. Used by the retract branch of
	// reduceSlugUpdates for "what was this doc's contribution?" framing.
	ListSummariesByKnowledgeIDs(ctx context.Context, kbID string, kids []string) (map[string]string, error)

	// ExistsSlugs reports which of the given slugs are live (non-
	// archived). Used by cleanDeadLinks to validate out-link targets
	// without loading the referenced pages' content.
	ExistsSlugs(ctx context.Context, kbID string, slugs []string) (map[string]bool, error)

	// ListAllSlugs returns every non-archived slug in the KB. Used by
	// lint to compute the live-slug set without paying for ListAll's
	// full row materialization.
	ListAllSlugs(ctx context.Context, kbID string) ([]string, error)

	// ListPagesCursor returns up to `limit` pages ordered by id ASC,
	// paginated by an opaque numeric cursor. Used by lint to stream
	// the KB without loading every page at once.
	ListPagesCursor(ctx context.Context, kbID string, cursor string, limit int) ([]*types.WikiPage, string, error)

	// ListByTypeRecent returns the most-recently-updated pages of the
	// given type, projected to slug/title/summary, capped at `limit`.
	// Used by rebuildIndexPage's first-time generation path so the
	// LLM prompt size is bounded on large KBs.
	ListByTypeRecent(ctx context.Context, kbID string, pageType string, limit int) ([]types.WikiIndexEntry, error)

	// FindSimilarPages returns the top-k pages whose lowercase title
	// is most similar to the query under pg_trgm. `pageTypes` empty
	// defaults to entity+concept. Used by the dedup pre-filter to
	// surface candidate merge targets server-side.
	FindSimilarPages(ctx context.Context, kbID string, query string, pageTypes []string, limit int) ([]*types.WikiPageLite, error)

	// FindPagesByNormalizedTitle returns non-archived pages of pageType whose
	// whitespace-stripped, lowercased title equals identity.
	FindPagesByNormalizedTitle(ctx context.Context, kbID, pageType, identity string) ([]*types.WikiPageLite, error)

	// FindPagesByNormalizedTitles is the batched form of
	// FindPagesByNormalizedTitle.
	FindPagesByNormalizedTitles(ctx context.Context, kbID, pageType string, identities []string) ([]*types.WikiPageLite, error)

	// ListDistinctCategoryPaths returns the materialized paths of existing
	// wiki folders (split into segments), capped at maxPaths. Used by the
	// wiki ingest taxonomy planner as the pool of folders to reuse.
	ListDistinctCategoryPaths(ctx context.Context, kbID string, maxPaths int) ([][]string, error)

	// --- Folder tree (wiki_folders) ---

	// CreateFolder inserts a new directory node.
	CreateFolder(ctx context.Context, folder *types.WikiFolder) error
	// GetFolderByID retrieves a folder by id within a knowledge base.
	GetFolderByID(ctx context.Context, kbID string, id string) (*types.WikiFolder, error)
	// GetChildFolderByName returns the live child folder of parentID with the
	// given name, or ErrWikiFolderNotFound. Used by find-or-create.
	GetChildFolderByName(ctx context.Context, kbID string, parentID string, name string) (*types.WikiFolder, error)
	// ListChildFolders returns the direct child folders of parentID, ordered
	// by sort_order then name.
	ListChildFolders(ctx context.Context, kbID string, parentID string) ([]*types.WikiFolder, error)
	// ListAllFolders returns every folder in the knowledge base.
	ListAllFolders(ctx context.Context, kbID string) ([]*types.WikiFolder, error)
	// UpdateFolder rewrites a folder's mutable fields (name, parent_id, path,
	// depth, sort_order, updated_at).
	UpdateFolder(ctx context.Context, folder *types.WikiFolder) error
	// DeleteFolder soft-deletes a folder by id.
	DeleteFolder(ctx context.Context, kbID string, id string) error
	// CountPagesInFolder returns the number of live (non-archived) pages
	// directly in the folder (folder_id = id).
	CountPagesInFolder(ctx context.Context, kbID string, folderID string) (int64, error)
	// CountPagesByFolder returns live (non-archived) page counts grouped by
	// folder_id. When pageTypes is non-empty the count is restricted to those
	// page types; otherwise all types are counted. Pages at the wiki root carry
	// the empty-string key.
	CountPagesByFolder(ctx context.Context, kbID string, pageTypes []string) (map[string]int64, error)
	// ListPagesByFolderIDs returns all pages whose folder_id is in the set.
	// Used to recompute cached paths when a folder subtree is moved/renamed.
	ListPagesByFolderIDs(ctx context.Context, kbID string, folderIDs []string) ([]*types.WikiPage, error)

	// ListAll retrieves all non-archived wiki pages in a knowledge base (for link rebuilding, graph generation).
	ListAll(ctx context.Context, kbID string) ([]*types.WikiPage, error)

	// ListRecentForSuggestions returns recent user-visible wiki pages under the given
	// knowledge bases, used to produce fallback suggested questions for Wiki-only KBs
	// that do not have AI-generated document questions or recommended FAQ entries.
	// Excludes the index page and archived pages. Returns up to `limit` rows sorted
	// by updated_at descending.
	ListRecentForSuggestions(ctx context.Context, tenantID uint64, kbIDs []string, limit int) ([]*types.WikiPage, error)

	// Delete soft-deletes a wiki page by knowledge base ID and slug.
	Delete(ctx context.Context, kbID string, slug string) error

	// DeleteByID soft-deletes a wiki page by ID.
	DeleteByID(ctx context.Context, id string) error

	// Search performs full-text search on wiki pages within a knowledge base.
	Search(ctx context.Context, kbID string, query string, limit int) ([]*types.WikiPage, error)

	// SearchAcross performs the same full-text search across multiple
	// knowledge bases, then returns a global top-N by match_rank.
	SearchAcross(ctx context.Context, kbIDs []string, query string, limit int) ([]*types.WikiPage, error)

	// CountByType returns page counts grouped by type for a knowledge base.
	CountByType(ctx context.Context, kbID string) (map[string]int64, error)

	// CountOrphans returns the number of pages with no inbound links.
	CountOrphans(ctx context.Context, kbID string) (int64, error)

	// UpdateWithRevision snapshots the version being superseded and applies
	// the page update in a single transaction, so history can never contain
	// a snapshot of a version that is still current. Inserting an already-
	// present (page_id, version) pair is a silent no-op.
	UpdateWithRevision(ctx context.Context, page *types.WikiPage, rev *types.WikiPageRevision) error

	// ListRevisions returns snapshots for a page newest-first (content
	// column omitted) plus the total snapshot count.
	ListRevisions(ctx context.Context, kbID string, pageID string, limit int, offset int) ([]*types.WikiPageRevision, int64, error)

	// GetRevision returns one snapshot with content.
	GetRevision(ctx context.Context, kbID string, pageID string, version int) (*types.WikiPageRevision, error)

	// PruneRevisions bounds per-page history storage using the two-tier
	// retention described by the request.
	PruneRevisions(ctx context.Context, req types.WikiRevisionPruneRequest) error

	// DeleteRevisionsByPage hard-deletes a page's entire snapshot history.
	DeleteRevisionsByPage(ctx context.Context, pageID string) error

	// DeleteByKnowledgeBaseID soft-deletes all wiki pages for tenantID+kbID.
	// Used by KB delete cleanup to batch-soft-delete every page without walking
	// the folder tree. Bypasses the per-page chunk/link reconciliation that
	// DeletePage does — the whole KB is going away, so cross-link cleanup is
	// the KB delete flow's responsibility. tenantID is required so a tampered
	// delete payload cannot wipe another tenant's wiki by kbID alone.
	DeleteByKnowledgeBaseID(ctx context.Context, tenantID uint64, kbID string) error

	// DeleteFoldersByKnowledgeBaseID soft-deletes all wiki folders for
	// tenantID+kbID. Unlike DeleteFolder this does NOT enforce the emptiness
	// guard — the KB is being deleted, so non-empty folders must go too.
	DeleteFoldersByKnowledgeBaseID(ctx context.Context, tenantID uint64, kbID string) error

	// DeleteRevisionsByKnowledgeBaseID hard-deletes all wiki page revisions
	// for tenantID+kbID. Revisions have no deleted_at column (they are
	// immutable snapshots, not soft-deletable rows), so this is a physical
	// DELETE — same semantics as DeleteRevisionsByPage but scoped to the
	// whole KB.
	DeleteRevisionsByKnowledgeBaseID(ctx context.Context, tenantID uint64, kbID string) error
}
