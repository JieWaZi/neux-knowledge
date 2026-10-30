package store

import (
	"context"
	"strings"

	types "github.com/JieWaZi/neux-knowledge/internal/domain"
	"gorm.io/gorm"
)

// sourceActivity 仅承载请求范围内来源的活动投影，不加载任务的完整来源集合。
type sourceActivity struct {
	ID         string // 当前请求的来源版本标识。
	SourceID   string // 跨版本稳定的原件身份。
	State      string // 当前尝试的展示状态；空值表示沿用来源持久状态。
	Diagnostic string // 当前死信诊断；其他在途状态清除旧诊断。
	Occupied   bool   // 任一版本仍被在途任务占用。
}

// sourceActivitySnapshot 同时提供展示状态与原件占用，终态展示不能解除未结束任务的占用。
type sourceActivitySnapshot struct {
	Versions map[string]sourceActivity // 当前请求的来源版本展示状态。
	Occupied map[string]bool           // 当前请求的原件占用状态。
}

// sourceActiveWorkSQL 从确切来源版本的反向索引定位工作关系，再按主键核对原任务的当前尝试与在途状态。
func sourceActiveWorkSQL(sourceTable string) string {
	return `FROM knowledge_organize_source_work AS source_work
		WHERE source_work.source_version_id = ` + sourceTable + `.id
		AND source_work.library_id = ` + sourceTable + `.library_id
		AND EXISTS (SELECT 1 FROM knowledge_jobs AS source_job
			WHERE source_job.id = source_work.job_id AND source_job.library_id = source_work.library_id
			AND source_job.attempt = source_work.job_attempt
			AND source_job.state IN ('queued','processing','cancel_requested'))`
}

// sourceActivityValueSQL 利用接纳时同原件只能由一个在途任务占用的约束，读取确切版本的唯一活动归属。
func sourceActivityValueSQL(value string) string {
	return `COALESCE((SELECT ` + value + ` ` + sourceActiveWorkSQL("knowledge_source_versions") + ` LIMIT 1), '')`
}

// sourceActivityStateSQL 与状态筛选共用同一表达式，当前尝试的 done 不能覆盖来源已保存的结论。
func sourceActivityStateSQL() string {
	return sourceActivityValueSQL(`CASE
		WHEN source_work.state = 'done' THEN ''
		WHEN source_work.state = 'dead' THEN 'failed'
		WHEN source_work.started_at > 0 THEN 'processing'
		ELSE 'queued' END`)
}

func sourceOccupiedSQL() string {
	return `EXISTS (SELECT 1 FROM knowledge_source_versions AS occupied_source
		WHERE occupied_source.library_id = knowledge_source_versions.library_id
		AND occupied_source.source_id = knowledge_source_versions.source_id
		AND EXISTS (SELECT 1 ` + sourceActiveWorkSQL("occupied_source") + `))`
}

// sourceActivities 只投影传入来源；其他版本的原件占用由持久事实相关子查询判断。
func sourceActivities(tx *gorm.DB, libraryID string, sources []*types.SourceVersion) (sourceActivitySnapshot, error) {
	result := sourceActivitySnapshot{Versions: map[string]sourceActivity{}, Occupied: map[string]bool{}}
	ids := make([]string, 0, len(sources))
	seen := make(map[string]bool, len(sources))
	for _, source := range sources {
		if source != nil && !seen[source.ID] {
			seen[source.ID] = true
			ids = append(ids, source.ID)
		}
	}
	diagnostic := sourceActivityValueSQL(`CASE WHEN source_work.state = 'dead' THEN source_work.diagnostic ELSE '' END`)
	for start := 0; start < len(ids); start += 500 {
		end := start + 500
		if end > len(ids) {
			end = len(ids)
		}
		var rows []sourceActivity
		if err := tx.Model(&types.SourceVersion{}).
			Select("id, source_id, "+sourceActivityStateSQL()+" AS state, "+diagnostic+" AS diagnostic, "+sourceOccupiedSQL()+" AS occupied").
			Where("library_id = ? AND id IN ?", libraryID, ids[start:end]).Find(&rows).Error; err != nil {
			return result, err
		}
		for _, row := range rows {
			result.Occupied[row.SourceID] = row.Occupied
			if row.State != "" {
				result.Versions[row.ID] = row
			}
		}
	}
	return result, nil
}

func projectSourceActivity(sources []*types.SourceVersion, activity sourceActivitySnapshot) []*types.SourceVersion {
	result := make([]*types.SourceVersion, 0, len(sources))
	for _, s := range sources {
		if s == nil {
			continue
		}
		v := *s
		v.OrganizeBlocked = activity.Occupied[s.SourceID]
		if a, ok := activity.Versions[s.ID]; ok && !s.Archived && s.State != "processed" && s.State != "no-change" {
			v.State = a.State
			v.Diagnostic = a.Diagnostic
		}
		result = append(result, &v)
	}
	return result
}

// SourceActivity shares list-state rules; source terminal and archive states always take precedence.
func (s *Store) SourceActivity(ctx context.Context, libraryID string, sources []*types.SourceVersion) (items []*types.SourceVersion, err error) {
	err = s.transaction(ctx, func(tx *gorm.DB) error {
		if _, err := getLibrary(tx, libraryID, false); err != nil {
			return err
		}
		for _, v := range sources {
			if v != nil && v.LibraryID != libraryID {
				return fail(types.ErrScopeViolation, "source.activity", "source belongs to another library")
			}
		}
		activity, e := sourceActivities(tx, libraryID, sources)
		if e != nil {
			return e
		}
		items = projectSourceActivity(sources, activity)
		return nil
	})
	return
}

// ListSourcesQuery filters display state using single-table conditions; counts and pagination share a snapshot.
func (s *Store) ListSourcesQuery(ctx context.Context, libraryID string, in types.SourceQuery) (items []*types.SourceVersion, total int64, err error) {
	state := strings.TrimSpace(in.State)
	if state == "" {
		state = "all"
	}
	if state == "unchanged" {
		state = "no-change"
	}
	switch state {
	case "all", "pending", "queued", "processing", "processed", "no-change", "failed", "cancelled":
	default:
		return nil, 0, fail(types.ErrInvalid, "source.list", "invalid source state")
	}
	order := "created_at DESC, id DESC"
	switch in.Sort {
	case "", "newest":
	case "oldest":
		order = "created_at ASC, id ASC"
	default:
		return nil, 0, fail(types.ErrInvalid, "source.list", "invalid source sort")
	}
	offset, limit := pagination(in.Offset, in.Limit)
	err = s.transaction(ctx, func(tx *gorm.DB) error {
		if _, e := getLibrary(tx, libraryID, false); e != nil {
			return e
		}
		q := tx.Model(&types.SourceVersion{}).Where("library_id = ? AND archived = ?", libraryID, false)
		if in.Query != "" {
			q = q.Where("neux_source_contains(filename, ?) = 1", in.Query)
		}
		if state != "all" {
			q = q.Where("CASE WHEN state IN ('processed','no-change') THEN state ELSE COALESCE(NULLIF("+sourceActivityStateSQL()+", ''), state) END = ?", state)
		}
		if e := q.Count(&total).Error; e != nil {
			return e
		}
		if e := q.Order(order).Offset(offset).Limit(limit).Find(&items).Error; e != nil {
			return e
		}
		activity, e := sourceActivities(tx, libraryID, items)
		if e != nil {
			return e
		}
		items = projectSourceActivity(items, activity)
		return nil
	})
	return
}
