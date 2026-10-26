package core

import (
	"context"
	"fmt"
	types "github.com/JieWaZi/neux-knowledge/internal/domain"
	"github.com/JieWaZi/neux-knowledge/internal/pipeline"
	"github.com/JieWaZi/neux-knowledge/internal/wiki"
)

// These aliases expose knowledge reading and direct model ports.
type (
	ReadRequest      = pipeline.ReadRequest
	ReadArtifact     = pipeline.ReadArtifact
	ReadBlock        = pipeline.Chunk
	Chunk            = pipeline.Chunk
	ReaderPort       = pipeline.ReaderPort
	MarkdownReader   = pipeline.MarkdownReader
	ModelMessage     = pipeline.ModelMessage
	ModelUsage       = types.ModelUsage
	ModelDiagnostics = types.ModelDiagnostics
	GenerateRequest  = pipeline.GenerateRequest
	GenerateResult   = pipeline.GenerateResult
	Generator        = pipeline.Generator
	OrganizeInput    = pipeline.Input
	OrganizeResult   = pipeline.Result
)

// RunOrganize executes the fixed Lite pipeline; model calls do not create chats or generic executions.
func (e *Engine) RunOrganize(ctx context.Context, in OrganizeInput) (*OrganizeResult, error) {
	job, err := e.store.GetJob(ctx, in.JobID)
	if err != nil {
		return nil, err
	}
	if types.IsMaintenanceJob(job) {
		return nil, &types.Error{Code: types.ErrInvalid, Operation: "organize", Detail: "Wiki maintenance uses the independent tool-capable model driver"}
	}
	return pipeline.Run(ctx, e.store, in)
}

// CancelOrganize establishes a cancellation barrier only for internal lifecycle events such as deletion or authorization revocation; callers also cancel the running Context, and no user stop API is exposed.
func (e *Engine) CancelOrganize(ctx context.Context, in JobActionInput) (*Receipt, error) {
	rec, err := e.store.CancelJob(ctx, in)
	if err != nil {
		return nil, err
	}
	job, err := e.store.GetJob(ctx, in.JobID)
	if err != nil {
		return nil, err
	}
	release, acquired, err := e.store.LockProcessor(ctx, "lite/library/"+job.LibraryID, 1)
	if err != nil {
		return nil, err
	}
	if acquired {
		defer release()
		if err = e.store.RecoverLiteStages(ctx, job.ID, job.Attempt); err != nil {
			return nil, err
		}
	}
	return rec, nil
}

// OrganizeConfigDigest binds the library configuration, resources, and Lite prompt version.
func OrganizeConfigDigest(lib *Library, rulesSHA256 string) string {
	return types.OrganizeConfigDigest(lib, rulesSHA256, wiki.StagePromptVersion)
}

// FailOrganize terminates an unrecoverable failure while retaining published pages and successful sources; it must not be called for process-exit cancellation.
func (e *Engine) FailOrganize(ctx context.Context, jobID string, attempt int, diagnostic string) (*Receipt, error) {
	job, err := e.GetJob(ctx, jobID)
	if err != nil {
		return nil, err
	}
	if job.Attempt != attempt {
		return nil, &types.Error{Code: types.ErrStaleAttempt, Operation: "lite.fail", Detail: "job attempt changed"}
	}
	sources := []types.SourceConclusion{}
	for _, id := range job.SourceVersionIDs {
		source, err := e.GetSource(ctx, job.LibraryID, id)
		if err != nil {
			return nil, err
		}
		if source.State != "processed" && source.State != "no-change" {
			sources = append(sources, types.SourceConclusion{SourceVersionID: id, State: "failed", Diagnostic: diagnostic})
		}
	}
	return e.store.FailJob(ctx, types.FailJobInput{JobActionInput: types.JobActionInput{RequestID: fmt.Sprintf("lite/%s/%d/fatal", jobID, attempt), JobID: jobID, ExpectedVersion: job.Version}, Diagnostic: diagnostic, Sources: sources})
}
