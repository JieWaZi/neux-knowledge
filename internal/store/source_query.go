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

// sourceActivities derives overlays only from the task occupying the slot and work in its current attempt; done retains the source's terminal state.
func sourceActivities(tx *gorm.DB, libraryID string) (map[string]sourceActivity, error) {
	var jobs []types.Job
	if err := tx.Select("id,attempt,source_version_ids").Where("library_id = ? AND state IN ?", libraryID, []string{"queued", "processing", "cancel_requested"}).Limit(1).Find(&jobs).Error; err != nil {
		return nil, err
	}
	result := map[string]sourceActivity{}
	if len(jobs) == 0 {
		return result, nil
	}
	j := jobs[0]
	var work []types.OrganizeSourceWork
	if err := tx.Select("source_version_id,state,diagnostic,started_at").Where("job_id = ? AND job_attempt = ?", j.ID, j.Attempt).Find(&work).Error; err != nil {
		return nil, err
	}
	current := make(map[string]types.OrganizeSourceWork, len(work))
	for _, w := range work {
		current[w.SourceVersionID] = w
	}
	for _, id := range j.SourceVersionIDs {
		w, ok := current[id]
		if ok && w.State == "done" {
			continue
		}
		activity := sourceActivity{State: "queued"}
		if ok && w.State == "dead" {
			activity.State = "failed"
			activity.Diagnostic = w.Diagnostic
		} else if ok && w.StartedAt > 0 {
			activity.State = "processing"
		}
		result[id] = activity
	}
	return result, nil
}

func projectSourceActivity(sources []*types.SourceVersion, activity map[string]sourceActivity) []*types.SourceVersion {
	result := make([]*types.SourceVersion, 0, len(sources))
	for _, s := range sources {
		if s == nil {
			continue
		}
		v := *s
		if a, ok := activity[s.ID]; ok && !s.Archived && s.State != "processed" && s.State != "no-change" {
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
			for id, a := range activity {
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
