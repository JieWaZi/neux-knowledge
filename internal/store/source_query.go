package store

import (
	"context"
	"encoding/json"
	"strings"

	types "github.com/JieWaZi/neux-knowledge/internal/domain"
	"gorm.io/gorm"
)

// sourceActivity holds a lightweight state overlay for the current attempt without loading source contents.
type sourceActivity struct {
	State      string // Display state for the current attempt.
	Diagnostic string // Current dead-letter diagnostic; other in-flight states clear stale diagnostics.
}

// sourceActivitySnapshot 同时提供展示状态与原件占用，终态展示不能解除未结束任务的占用。
type sourceActivitySnapshot struct {
	Versions map[string]sourceActivity // 当前尝试对来源版本的展示状态。
	Occupied map[string]bool           // 所有在途任务仍然拥有的原件身份。
}

// sourceActivities 汇总全部在途任务及其当前尝试，来源终态仍由原记录保存。
func sourceActivities(tx *gorm.DB, libraryID string) (sourceActivitySnapshot, error) {
	result := sourceActivitySnapshot{Versions: map[string]sourceActivity{}, Occupied: map[string]bool{}}
	var jobs []types.Job
	if err := tx.Select("id,attempt,source_version_ids").Where("library_id = ? AND state IN ?", libraryID, []string{"queued", "processing", "cancel_requested"}).Find(&jobs).Error; err != nil {
		return result, err
	}
	if len(jobs) == 0 {
		return result, nil
	}
	versions := make([]string, 0)
	seen := map[string]bool{}
	for _, j := range jobs {
		for _, id := range j.SourceVersionIDs {
			if !seen[id] {
				seen[id] = true
				versions = append(versions, id)
			}
		}
	}
	for start := 0; start < len(versions); start += 500 {
		end := start + 500
		if end > len(versions) {
			end = len(versions)
		}
		var sources []types.SourceVersion
		if err := tx.Select("source_id").Where("library_id = ? AND id IN ?", libraryID, versions[start:end]).Find(&sources).Error; err != nil {
			return result, err
		}
		for _, source := range sources {
			result.Occupied[source.SourceID] = true
		}
	}
	var work []types.OrganizeSourceWork
	for start := 0; start < len(jobs); start += 100 {
		end := start + 100
		if end > len(jobs) {
			end = len(jobs)
		}
		clauses := make([]string, 0, end-start)
		args := make([]any, 0, (end-start)*2)
		for _, j := range jobs[start:end] {
			clauses = append(clauses, "(job_id = ? AND job_attempt = ?)")
			args = append(args, j.ID, j.Attempt)
		}
		var batch []types.OrganizeSourceWork
		if err := tx.Select("job_id,job_attempt,source_version_id,state,diagnostic,started_at").Where(strings.Join(clauses, " OR "), args...).Find(&batch).Error; err != nil {
			return result, err
		}
		work = append(work, batch...)
	}
	current := make(map[string]map[string]types.OrganizeSourceWork, len(jobs))
	attempts := map[string]int{}
	for _, j := range jobs {
		attempts[j.ID] = j.Attempt
		current[j.ID] = map[string]types.OrganizeSourceWork{}
	}
	for _, w := range work {
		if attempts[w.JobID] == w.JobAttempt {
			current[w.JobID][w.SourceVersionID] = w
		}
	}
	for _, j := range jobs {
		for _, id := range j.SourceVersionIDs {
			w, ok := current[j.ID][id]
			if ok && w.State == "done" {
				continue
			}
			activity := sourceActivity{State: "queued"}
			if ok && w.State == "dead" {
				activity.State, activity.Diagnostic = "failed", w.Diagnostic
			} else if ok && w.StartedAt > 0 {
				activity.State = "processing"
			}
			result.Versions[id] = activity
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
		activity, e := sourceActivities(tx, libraryID)
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
		activity, e := sourceActivities(tx, libraryID)
		if e != nil {
			return e
		}
		q := tx.Model(&types.SourceVersion{}).Where("library_id = ? AND archived = ?", libraryID, false)
		if in.Query != "" {
			q = q.Where("neux_source_contains(filename, ?) = 1", in.Query)
		}
		if state != "all" {
			ids := map[string][]string{}
			for id, a := range activity.Versions {
				ids[a.State] = append(ids[a.State], id)
			}
			// Bind one JSON parameter per identity group to avoid exceeding SQLite's variable limit when many sources are active.
			encoded := make(map[string]string, 3)
			for _, key := range []string{"failed", "processing", "queued"} {
				values, err := json.Marshal(ids[key])
				if err != nil {
					return err
				}
				encoded[key] = string(values)
			}
			q = q.Where("CASE WHEN state IN ('processed','no-change') THEN state WHEN id IN (SELECT value FROM json_each(?)) THEN 'failed' WHEN id IN (SELECT value FROM json_each(?)) THEN 'processing' WHEN id IN (SELECT value FROM json_each(?)) THEN 'queued' ELSE state END = ?", encoded["failed"], encoded["processing"], encoded["queued"], state)
		}
		if e = q.Count(&total).Error; e != nil {
			return e
		}
		if e = q.Order(order).Offset(offset).Limit(limit).Find(&items).Error; e != nil {
			return e
		}
		items = projectSourceActivity(items, activity)
		return nil
	})
	return
}
