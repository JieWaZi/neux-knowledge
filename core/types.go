// Package core provides a standalone knowledge SDK without requiring models, HTTP, or a NeuxAlly host.
package core

import "github.com/JieWaZi/neux-knowledge/internal/domain"

// These aliases directly reuse core domain models without requiring SDK callers to import internal packages.
type (
	JSON                 = types.JSON
	StringArray          = types.StringArray
	SourceRef            = types.SourceRef
	SourceLocator        = types.SourceLocator
	CodeRef              = types.CodeRef
	PageEvidence         = types.PageEvidence
	Library              = types.Library
	SourceVersion        = types.SourceVersion
	KnowledgeProfile     = types.KnowledgeProfile
	PermissionSnapshot   = types.PermissionSnapshot
	Page                 = types.WikiPage
	Theme                = types.WikiFolder
	Revision             = types.WikiPageRevision
	Job                  = types.Job
	Stage                = types.Stage
	Receipt              = types.Receipt
	Publication          = types.Publication
	PageMutation         = types.PageMutation
	ThemeMutation        = types.ThemeMutation
	SourceConclusion     = types.SourceConclusion
	WikiConfig           = types.WikiConfig
	IndexingStrategy     = types.IndexingStrategy
	CreateLibraryInput   = types.CreateLibraryInput
	UpdateLibraryInput   = types.UpdateLibraryInput
	ImportSourceInput    = types.ImportSourceInput
	ArchiveSourceInput   = types.ArchiveSourceInput
	WritePageInput       = types.WritePageInput
	ReplacePageTextInput = types.ReplacePageTextInput
	RenamePageInput      = types.RenamePageInput
	DeletePageInput      = types.DeletePageInput
	RestorePageInput     = types.RestorePageInput
	WriteThemeInput      = types.WriteThemeInput
	AcceptJobInput       = types.AcceptJobInput
	JobActionInput       = types.JobActionInput
	PageQuery            = types.PageQuery
)

// Error and ErrorCode are stable standalone core errors independent of the host HTTP framework.
type Error = types.Error
type ErrorCode = types.ErrorCode

const (
	ErrInvalid                = types.ErrInvalid
	ErrNotFound               = types.ErrNotFound
	ErrVersionConflict        = types.ErrVersionConflict
	ErrIdempotencyConflict    = types.ErrIdempotencyConflict
	ErrScopeViolation         = types.ErrScopeViolation
	ErrCancelled              = types.ErrCancelled
	ErrStaleAttempt           = types.ErrStaleAttempt
	ErrExecutionNotSuccessful = types.ErrExecutionNotSuccessful
	ErrCandidateInvalid       = types.ErrCandidateInvalid
	ErrManualProtected        = types.ErrManualProtected
	ErrDatabaseBusy           = types.ErrDatabaseBusy
	ErrDatabase               = types.ErrDatabase
	ErrStorage                = types.ErrStorage
)

// FailJobInput records an actual failure without page writes.
type FailJobInput = types.FailJobInput
