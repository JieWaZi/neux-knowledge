package store

import (
	"context"
	"encoding/json"
	"errors"
	"path"
	"strings"
	"time"

	"github.com/JieWaZi/neux-knowledge/internal/domain"
	"github.com/JieWaZi/neux-knowledge/internal/wiki"
	"gorm.io/gorm"
)

func getPage(tx *gorm.DB, lib, id string) (*types.WikiPage, error) {
	var p types.WikiPage
	if err := tx.Where("knowledge_base_id = ? AND id = ?", lib, id).Take(&p).Error; err != nil {
		return nil, err
	}
	return &p, nil
}
func getTheme(tx *gorm.DB, lib, id string) (*types.WikiFolder, error) {
	var t types.WikiFolder
	if err := tx.Where("id = ?", id).Take(&t).Error; err != nil {
		return nil, err
	}
	if t.KnowledgeBaseID != lib {
		return nil, fail(types.ErrScopeViolation, "theme", "theme belongs to another library")
	}
	return &t, nil
}
func validateEvidence(tx *gorm.DB, l *types.Library, p *types.WikiPage, allowed map[string]bool) error {
	ids := make([]string, 0, len(p.Evidence.Sources))
	for _, ref := range p.Evidence.Sources {
		ids = append(ids, ref.SourceVersionID)
	}
	if len(ids) > 1000 {
		return fail(types.ErrInvalid, "page.evidence", "too many source references")
	}
	sources := map[string]types.SourceVersion{}
	if len(ids) > 0 {
		var rows []types.SourceVersion
		if err := tx.Where("id IN ?", ids).Find(&rows).Error; err != nil {
			return err
		}
		for _, v := range rows {
			sources[v.ID] = v
		}
	}
	derived := types.StringArray{}
	seen := map[string]bool{}
	for _, ref := range p.Evidence.Sources {
		v, ok := sources[ref.SourceVersionID]
		if !ok || v.LibraryID != l.ID {
			return fail(types.ErrScopeViolation, "page.evidence", "source belongs to another library or is missing")
		}
		if ref.SourceID != v.SourceID || ref.SHA256 != v.SHA256 {
			return fail(types.ErrCandidateInvalid, "page.evidence", "source identity mismatch")
		}
		switch ref.ClaimKind {
		case "source_statement", "inference", "disagreement":
			if ref.ExcerptHash != hash([]byte(ref.Excerpt)) || ref.Locator.Kind == "" {
				return fail(types.ErrCandidateInvalid, "page.evidence", "source excerpt or locator mismatch")
			}
		case "document_context":
			// The upstream Details fallback establishes only that a document contributed to synthesis; model descriptions must not masquerade as source chunk citations.
			if ref.Excerpt != "" || ref.ExcerptHash != "" || ref.Locator != (types.SourceLocator{}) {
				return fail(types.ErrCandidateInvalid, "page.evidence", "document context cannot contain a quotation or locator")
			}
		default:
			return fail(types.ErrCandidateInvalid, "page.evidence", "unknown claim kind")
		}
		if allowed != nil && !allowed[v.ID] && !allowed["prior:"+digest(ref)] {
			return fail(types.ErrScopeViolation, "page.evidence", "source was not frozen for this job")
		}
		if !seen[v.ID] {
			derived = append(derived, v.ID)
			seen[v.ID] = true
		}
	}
	// Derived references cannot be a second writable evidence source; cross-library attempts still return an accurate scope error.
	for _, sourceRef := range p.SourceRefs {
		vid := types.WikiSourceKnowledgeID(sourceRef)
		if !seen[vid] {
			var v types.SourceVersion
			err := tx.Where("id = ?", vid).Take(&v).Error
			if err == nil && v.LibraryID != l.ID {
				return fail(types.ErrScopeViolation, "page.evidence", "source belongs to another library")
			}
			return fail(types.ErrCandidateInvalid, "page.evidence", "SourceRefs must be derived from Evidence")
		}
	}
	for _, ref := range p.Evidence.Code {
		if l.Kind != "software" || ref.Repository == "" || ref.Commit == "" || ref.Path == "" || strings.HasPrefix(ref.Path, "/") || path.Clean(ref.Path) == ".." || strings.HasPrefix(path.Clean(ref.Path), "../") || ref.VerifiedAt <= 0 || ref.StartLine < 0 || ref.EndLine < ref.StartLine {
			return fail(types.ErrCandidateInvalid, "page.evidence", "invalid code evidence")
		}
	}
	p.SourceRefs = derived
	return nil
}
func preparePage(tx *gorm.DB, l *types.Library, p *types.WikiPage, existing *types.WikiPage, generated bool, allowed map[string]bool, edit pageEdit) error {
	if p.KnowledgeBaseID != "" && p.KnowledgeBaseID != l.ID {
		return fail(types.ErrScopeViolation, "page.write", "page belongs to another library")
	}
	p.KnowledgeBaseID = l.ID
	p.OutLinks = wiki.ParseOutLinks(p.Content)
	p.InLinks = nil
	p.TenantID = 0
	p.Slug = strings.TrimSpace(p.Slug)
	p.Title = strings.TrimSpace(p.Title)
	if p.Slug == "" || p.Title == "" || p.PageType == "" {
		return fail(types.ErrInvalid, "page.write", "slug, title and page type are required")
	}
	if p.Status == "" {
		p.Status = "published"
	}
	if p.Status != "published" && p.Status != "archived" && p.Status != "draft" {
		return fail(types.ErrInvalid, "page.write", "invalid page status")
	}
	if p.WikiLanguage == "" {
		p.WikiLanguage = l.WikiLanguage
	}
	if p.WikiLanguage != l.WikiLanguage {
		return fail(types.ErrCandidateInvalid, "page.write", "wiki language mismatch")
	}
	if existing != nil {
		if p.Slug != existing.Slug && !edit.rename {
			return fail(types.ErrInvalid, "page.write", "stable page slug cannot be changed")
		}
		if generated {
			if existing.ManualPlacement && (p.FolderID != existing.FolderID || p.SortOrder != existing.SortOrder || p.Title != existing.Title) {
				return fail(types.ErrManualProtected, "page.write", "manual page placement is protected")
			}
			protection := append(types.StringArray(nil), existing.ProtectedContent...)
			if edit.rewrite != nil {
				for i, text := range protection {
					protection[i] = edit.rewrite(text)
				}
			}
			for _, text := range protection {
				if !strings.Contains(p.Content, text) {
					return fail(types.ErrManualProtected, "page.write", "manual content is protected")
				}
			}
			p.ManualPlacement = existing.ManualPlacement
			p.ProtectedContent = protection
		}
	}
	if !generated {
		p.LastEditSource = "user"
		if p.Content != "" {
			p.ProtectedContent = types.StringArray{p.Content}
		}
	} else {
		p.LastEditSource = "pipeline"
		if edit.agent {
			p.LastEditSource = types.WikiEditSourceAgent
			if existing == nil {
				p.ProtectedContent = nil
				p.ManualPlacement = false
			}
		}
	}
	if allowed != nil && existing != nil {
		local := map[string]bool{}
		for k, v := range allowed {
			local[k] = v
		}
		for _, ref := range existing.Evidence.Sources {
			local["prior:"+digest(ref)] = true
		}
		allowed = local
	}
	if err := validateEvidence(tx, l, p, allowed); err != nil {
		return err
	}
	if edit.agent {
		if err := validateAgentEvidence(tx, l, p, existing); err != nil {
			return err
		}
		if existing != nil && edit.rewrite == nil && !edit.rename && p.Status != "archived" && len(wiki.MissingRowIdentities(existing.Content, p.Content)) > 0 {
			return fail(types.ErrCandidateInvalid, "page.agent-write", "whole page write dropped stored table rows; use exact replacement for deliberate removal")
		}
	}
	p.CategoryPath = nil
	p.Depth = 0
	if p.FolderID != "" {
		folder, err := getTheme(tx, l.ID, p.FolderID)
		if err != nil {
			return err
		}
		p.CategoryPath = types.StringArray(strings.Split(folder.Path, "/"))
		p.Depth = len(p.CategoryPath)
	}
	p.WikiPath = strings.Join(append(append([]string{}, p.CategoryPath...), p.Title), "/")
	return nil
}
func revisionOf(p *types.WikiPage) *types.WikiPageRevision {
	return &types.WikiPageRevision{ID: newID(), KnowledgeBaseID: p.KnowledgeBaseID, PageID: p.ID, Slug: p.Slug, Version: p.Version, Title: p.Title, PageType: p.PageType, Status: p.Status, Content: p.Content, Summary: p.Summary, Aliases: p.Aliases, EditSource: p.LastEditSource, EditorID: p.LastEditorID, EditedAt: p.UpdatedAt, CreatedAt: time.Now(), WikiLanguage: p.WikiLanguage, Evidence: p.Evidence, SourceRefs: p.SourceRefs, ChunkRefs: p.ChunkRefs, PageMetadata: p.PageMetadata, ProtectedContent: p.ProtectedContent}
}
func writePage(tx *gorm.DB, l *types.Library, p *types.WikiPage, expected int, generated bool, allowed map[string]bool) (bool, error) {
	return writePageWithEdit(tx, l, p, expected, generated, allowed, pageEdit{})
}

func writePageWithEdit(tx *gorm.DB, l *types.Library, p *types.WikiPage, expected int, generated bool, allowed map[string]bool, edit pageEdit) (bool, error) {
	var old *types.WikiPage
	if p.ID != "" {
		var err error
		old, err = getPage(tx, l.ID, p.ID)
		// Only verified generated publication can create pages using preallocated identities; manual PUT requests must still target existing pages.
		if err != nil && !(generated && expected == 0 && errors.Is(err, gorm.ErrRecordNotFound)) {
			return false, err
		}
		if old != nil && old.Version != expected {
			return false, fail(types.ErrVersionConflict, "page.write", "page changed")
		}
	} else if expected != 0 {
		return false, fail(types.ErrVersionConflict, "page.write", "new page requires version zero")
	}
	if err := preparePage(tx, l, p, old, generated, allowed, edit); err != nil {
		return false, err
	}
	p.UpdatedAt = time.Now()
	if old == nil {
		if p.ID == "" {
			p.ID = newID()
		}
		p.CreatedAt = p.UpdatedAt
		p.Version = 1
		return p.FolderID != "", tx.Create(p).Error
	}
	if err := tx.Create(revisionOf(old)).Error; err != nil {
		return false, err
	}
	p.CreatedAt = old.CreatedAt
	p.Version = expected
	if err := updateWikiPageRow(tx, p); err != nil {
		return false, err
	}
	// CAS saves the body and derived references; additional local authoritative evidence belongs to the same transaction and version.
	evidence, _ := json.Marshal(p.Evidence)
	err := tx.Model(&types.WikiPage{}).Where("id = ? AND version = ?", p.ID, p.Version).Updates(map[string]any{"wiki_language": p.WikiLanguage, "evidence": string(evidence), "manual_placement": p.ManualPlacement, "protected_content": p.ProtectedContent}).Error
	if err == nil && edit.rename {
		err = tx.Model(&types.WikiPage{}).Where("knowledge_base_id = ? AND id = ? AND version = ?", l.ID, p.ID, p.Version).Update("slug", p.Slug).Error
	}
	return old.FolderID != p.FolderID || old.SortOrder != p.SortOrder || old.ManualPlacement != p.ManualPlacement, err
}

// WritePage protects content and history with both page and structure versions during manual edits.
func (s *Store) WritePage(ctx context.Context, in types.WritePageInput) (*types.Receipt, error) {
	return s.mutate(ctx, in.RequestID, "page.write", in.LibraryID, in, func(tx *gorm.DB) (*types.Receipt, error) {
		l, err := getLibrary(tx, in.LibraryID, true)
		if err != nil {
			return nil, err
		}
		if in.ExpectedPolicyVersion < 0 {
			return nil, fail(types.ErrInvalid, "page.write", "invalid policy version")
		}
		if in.ExpectedPolicyVersion > 0 && l.PolicyVersion != in.ExpectedPolicyVersion {
			return nil, fail(types.ErrScopeViolation, "page.write", "accepted permission is no longer valid")
		}
		if l.StructureVersion != in.StructureVersion {
			return nil, fail(types.ErrVersionConflict, "page.write", "structure changed")
		}
		p := in.Page
		structure, err := writePage(tx, l, &p, in.ExpectedVersion, false, nil)
		if err != nil {
			return nil, err
		}
		if structure {
			if err = bumpStructure(tx, l); err != nil {
				return nil, err
			}
		}
		if err := refreshBacklinks(tx, l.ID); err != nil {
			return nil, err
		}
		return &types.Receipt{ResourceID: p.ID, Version: int64(p.Version), PageIDs: []string{p.ID}, State: "saved"}, nil
	})
}

// RestorePage restores the body and evidence while explicitly retaining the current manual folder placement.
func (s *Store) RestorePage(ctx context.Context, in types.RestorePageInput) (*types.Receipt, error) {
	return s.mutate(ctx, in.RequestID, "page.restore", in.LibraryID, in, func(tx *gorm.DB) (*types.Receipt, error) {
		l, err := getLibrary(tx, in.LibraryID, true)
		if err != nil {
			return nil, err
		}
		if l.StructureVersion != in.StructureVersion {
			return nil, fail(types.ErrVersionConflict, "page.restore", "structure changed")
		}
		p, err := getPage(tx, l.ID, in.PageID)
		if err != nil {
			return nil, err
		}
		if p.Version != in.ExpectedVersion {
			return nil, fail(types.ErrVersionConflict, "page.restore", "page changed")
		}
		var rev types.WikiPageRevision
		if err = tx.Where("knowledge_base_id = ? AND page_id = ? AND version = ?", l.ID, p.ID, in.RevisionVersion).Take(&rev).Error; err != nil {
			return nil, err
		}
		p.Title = rev.Title
		p.PageType = rev.PageType
		p.Status = rev.Status
		p.Content = rev.Content
		p.Summary = rev.Summary
		p.Aliases = rev.Aliases
		p.Evidence = rev.Evidence
		p.SourceRefs = rev.SourceRefs
		p.ChunkRefs = rev.ChunkRefs
		p.PageMetadata = rev.PageMetadata
		p.WikiLanguage = rev.WikiLanguage
		if _, err = writePage(tx, l, p, in.ExpectedVersion, false, nil); err != nil {
			return nil, err
		}
		// The restore is atomically saved as an independent edit origin and new version; later history retains this origin.
		if err := tx.Model(&types.WikiPage{}).Where("id = ? AND version = ?", p.ID, p.Version).Update("last_edit_source", types.WikiEditSourceRevert).Error; err != nil {
			return nil, err
		}

		if err := refreshBacklinks(tx, l.ID); err != nil {
			return nil, err
		}
		return &types.Receipt{ResourceID: p.ID, Version: int64(p.Version), PageIDs: []string{p.ID}, State: "saved"}, nil
	})
}

// GetPage reads a page within the specified library identity.
func (s *Store) GetPage(ctx context.Context, lib, id string) (result *types.WikiPage, err error) {
	err = s.transaction(ctx, func(tx *gorm.DB) error { var e error; result, e = getPage(tx, lib, id); return e })
	return
}

// GetPageBySlug reads a page within a library by its stable slug.
func (s *Store) GetPageBySlug(ctx context.Context, lib, slug string) (result *types.WikiPage, err error) {
	err = s.transaction(ctx, func(tx *gorm.DB) error {
		result = &types.WikiPage{}
		return tx.Where("knowledge_base_id = ? AND slug = ?", lib, slug).Take(result).Error
	})
	return
}

// ListPages uses single-table LIKE keyword queries, including aliases and escaping wildcards.
func (s *Store) ListPages(ctx context.Context, q types.PageQuery) (items []*types.WikiPage, total int64, err error) {
	q.Offset, q.Limit = pagination(q.Offset, q.Limit)
	err = s.transaction(ctx, func(tx *gorm.DB) error {
		if _, e := getLibrary(tx, q.LibraryID, false); e != nil {
			return e
		}
		query := tx.Model(&types.WikiPage{}).Where("knowledge_base_id = ?", q.LibraryID)
		if q.Status != "" {
			query = query.Where("status = ?", q.Status)
		} else {
			query = query.Where("status <> ?", "archived")
		}
		if q.PageType != "" {
			query = query.Where("page_type = ?", q.PageType)
		}
		if q.ThemeID != nil {
			query = query.Where("folder_id = ?", *q.ThemeID)
		}
		if q.Query != "" {
			p := "%" + escapeLikePattern(q.Query) + "%"
			query = query.Where("(title LIKE ? ESCAPE '\\' OR slug LIKE ? ESCAPE '\\' OR content LIKE ? ESCAPE '\\' OR CAST(aliases AS TEXT) LIKE ? ESCAPE '\\')", p, p, p, p)
		}
		if e := query.Count(&total).Error; e != nil {
			return e
		}
		return query.Order("updated_at DESC, id ASC").Offset(q.Offset).Limit(q.Limit).Find(&items).Error
	})
	return
}

// ListRevisions returns bounded history and the complete total without implicitly trimming history.
func (s *Store) ListRevisions(ctx context.Context, lib, pageID string, offset, limit int) (items []*types.WikiPageRevision, total int64, err error) {
	offset, limit = pagination(offset, limit)
	err = s.transaction(ctx, func(tx *gorm.DB) error {
		if _, e := getPage(tx, lib, pageID); e != nil {
			return e
		}
		q := tx.Model(&types.WikiPageRevision{}).Where("knowledge_base_id = ? AND page_id = ?", lib, pageID)
		if e := q.Count(&total).Error; e != nil {
			return e
		}
		return q.Order("version DESC").Offset(offset).Limit(limit).Find(&items).Error
	})
	return
}

// GetRevision reads the complete historical body and structured evidence.
func (s *Store) GetRevision(ctx context.Context, lib, pageID string, version int) (result *types.WikiPageRevision, err error) {
	err = s.transaction(ctx, func(tx *gorm.DB) error {
		if _, e := getPage(tx, lib, pageID); e != nil {
			return e
		}
		result = &types.WikiPageRevision{}
		return tx.Where("knowledge_base_id = ? AND page_id = ? AND version = ?", lib, pageID, version).Take(result).Error
	})
	return
}

func applyTheme(tx *gorm.DB, l *types.Library, in types.ThemeMutation, generated bool) (string, error) {
	t := in.Theme
	if t.KnowledgeBaseID != "" && t.KnowledgeBaseID != l.ID {
		return "", fail(types.ErrScopeViolation, "theme.write", "theme belongs to another library")
	}
	t.KnowledgeBaseID = l.ID
	t.TenantID = 0
	var old *types.WikiFolder
	if t.ID != "" {
		var err error
		old, err = getTheme(tx, l.ID, t.ID)
		// Parent and child topics and pages refer to fixed identities during publication; creation must preserve these references.
		if err != nil && !(generated && !in.Delete && errors.Is(err, gorm.ErrRecordNotFound)) {
			return "", err
		}
		if generated && old != nil && old.Manual && (in.Delete || t.Name != old.Name || t.ParentID != old.ParentID || t.SortOrder != old.SortOrder) {
			return "", fail(types.ErrManualProtected, "theme.write", "manual theme is protected")
		}
		if generated && old != nil {
			t.Manual = old.Manual
		}
	}
	if old != nil && old.SystemKey != "" {
		return "", fail(types.ErrManualProtected, "theme.write", "system theme is protected")
	}
	if old == nil && t.SystemKey != "" {
		return "", fail(types.ErrInvalid, "theme.write", "systemKey is reserved")
	}
	if in.Delete {
		if old == nil {
			return "", fail(types.ErrNotFound, "theme.delete", "theme is missing")
		}
		var n int64
		if err := tx.Model(&types.WikiFolder{}).Where("knowledge_base_id = ? AND parent_id = ?", l.ID, t.ID).Count(&n).Error; err != nil {
			return "", err
		}
		if n > 0 {
			return "", fail(types.ErrInvalid, "theme.delete", "theme has children")
		}
		if err := tx.Model(&types.WikiPage{}).Where("knowledge_base_id = ? AND folder_id = ?", l.ID, t.ID).Count(&n).Error; err != nil {
			return "", err
		}
		if n > 0 {
			return "", fail(types.ErrInvalid, "theme.delete", "theme contains pages")
		}
		return t.ID, tx.Where("id = ? AND knowledge_base_id = ?", t.ID, l.ID).Delete(&types.WikiFolder{}).Error
	}
	t.Name = strings.TrimSpace(t.Name)
	if t.Name == "" || strings.ContainsAny(t.Name, "/\\") {
		return "", fail(types.ErrInvalid, "theme.write", "theme name must be a segment")
	}
	t.Depth = 1
	t.Path = t.Name
	if t.ParentID != "" {
		if t.ParentID == t.ID {
			return "", fail(types.ErrInvalid, "theme.write", "theme cannot parent itself")
		}
		parent, err := getTheme(tx, l.ID, t.ParentID)
		if err != nil {
			return "", err
		}
		if parent.Depth >= 2 {
			return "", fail(types.ErrInvalid, "theme.write", "only two theme levels are allowed")
		}
		t.Depth = parent.Depth + 1
		t.Path = parent.Path + "/" + t.Name
	}
	if !generated {
		t.Manual = true
	}
	t.UpdatedAt = time.Now()
	// Sibling names must be unique; no additional derived name field is used for duplicate checks.
	var duplicate int64
	if err := tx.Model(&types.WikiFolder{}).Where("knowledge_base_id = ? AND parent_id = ? AND name = ? AND id <> ?", l.ID, t.ParentID, t.Name, t.ID).Count(&duplicate).Error; err != nil {
		return "", err
	}
	if duplicate > 0 {
		return "", fail(types.ErrVersionConflict, "theme.write", "sibling theme name exists")
	}
	if old == nil {
		if t.ID == "" {
			t.ID = newID()
		}
		t.CreatedAt = t.UpdatedAt
		if err := tx.Create(&t).Error; err != nil {
			return "", err
		}
	} else {
		var children []*types.WikiFolder
		if err := tx.Where("knowledge_base_id = ? AND parent_id = ?", l.ID, t.ID).Find(&children).Error; err != nil {
			return "", err
		}
		if len(children) > 0 && t.Depth != 1 {
			return "", fail(types.ErrInvalid, "theme.write", "moving a parent would exceed two levels")
		}
		if err := tx.Model(&types.WikiFolder{}).Where("id = ? AND knowledge_base_id = ?", t.ID, l.ID).Updates(map[string]any{"name": t.Name, "parent_id": t.ParentID, "path": t.Path, "depth": t.Depth, "sort_order": t.SortOrder, "manual": t.Manual, "updated_at": t.UpdatedAt}).Error; err != nil {
			return "", err
		}
		for _, child := range children {
			if err := tx.Model(&types.WikiFolder{}).Where("id = ? AND knowledge_base_id = ?", child.ID, l.ID).Updates(map[string]any{"path": t.Path + "/" + child.Name, "depth": 2, "updated_at": t.UpdatedAt}).Error; err != nil {
				return "", err
			}
		}
		ids := []string{t.ID}
		for _, child := range children {
			ids = append(ids, child.ID)
		}
		var pages []*types.WikiPage
		if err := tx.Where("knowledge_base_id = ? AND folder_id IN ?", l.ID, ids).Find(&pages).Error; err != nil {
			return "", err
		}
		for _, p := range pages {
			if generated && p.ManualPlacement && old.Path != t.Path {
				return "", fail(types.ErrManualProtected, "theme.write", "manual page path is protected")
			}
			if _, err := writePage(tx, l, p, p.Version, generated, nil); err != nil {
				return "", err
			}
		}
	}
	return t.ID, nil
}

// WriteTheme creates, moves, or deletes manual topics up to two levels deep.
func (s *Store) WriteTheme(ctx context.Context, in types.WriteThemeInput) (*types.Receipt, error) {
	return s.mutate(ctx, in.RequestID, "theme.write", in.LibraryID, in, func(tx *gorm.DB) (*types.Receipt, error) {
		l, err := getLibrary(tx, in.LibraryID, true)
		if err != nil {
			return nil, err
		}
		if l.StructureVersion != in.StructureVersion {
			return nil, fail(types.ErrVersionConflict, "theme.write", "structure changed")
		}
		id, err := applyTheme(tx, l, types.ThemeMutation{Theme: in.Theme, Delete: in.Delete}, false)
		if err != nil {
			return nil, err
		}
		if err = bumpStructure(tx, l); err != nil {
			return nil, err
		}
		return &types.Receipt{ResourceID: id, Version: l.StructureVersion, State: "saved"}, nil
	})
}

// ListThemes reads actual topics by hierarchy and manual order without generating empty topics.
func (s *Store) ListThemes(ctx context.Context, lib string) (items []*types.WikiFolder, err error) {
	err = s.transaction(ctx, func(tx *gorm.DB) error {
		if _, e := getLibrary(tx, lib, false); e != nil {
			return e
		}
		return tx.Where("knowledge_base_id = ?", lib).Order("depth ASC, sort_order ASC, name ASC, id ASC").Find(&items).Error
	})
	return
}
