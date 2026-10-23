package store

import (
	"context"

	types "github.com/JieWaZi/neux-knowledge/internal/domain"
	"gorm.io/gorm"
)

// GraphPages 复用已迁入的页面仓库，仅选择图谱所需元数据，避免加载所有正文。
func (s *Store) GraphPages(ctx context.Context, libraryID string) (pages []*types.WikiPage, err error) {
	err = s.transaction(ctx, func(tx *gorm.DB) error {
		if _, e := getLibrary(tx, libraryID, false); e != nil {
			return e
		}
		var e error
		pages, e = NewWikiPageRepository(tx.Select("id", "slug", "title", "page_type", "in_links", "out_links", "source_refs", "updated_at").Limit(50001)).ListAll(ctx, libraryID)
		if e != nil {
			return e
		}
		if len(pages) > 50000 {
			return fail(types.ErrInvalid, "graph", "graph metadata capacity exceeded")
		}
		return nil
	})
	return pages, err
}
