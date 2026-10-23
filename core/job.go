package core

import "context"

// AcceptJob performs the corresponding knowledge domain operation under the SDK contract.
func (e *Engine) AcceptJob(ctx context.Context, in AcceptJobInput) (*Receipt, error) {
	return e.store.AcceptJob(ctx, in)
}

// GetJob performs the corresponding knowledge domain operation under the SDK contract.
func (e *Engine) GetJob(ctx context.Context, jobID string) (*Job, error) {
	return e.store.GetJob(ctx, jobID)
}

// ListJobs performs the corresponding knowledge domain operation under the SDK contract.
func (e *Engine) ListJobs(ctx context.Context, libraryID string, offset, limit int) ([]*Job, int64, error) {
	return e.store.ListJobs(ctx, libraryID, offset, limit)
}

// RetryJob performs the corresponding knowledge domain operation under the SDK contract.
func (e *Engine) RetryJob(ctx context.Context, in JobActionInput) (*Receipt, error) {
	return e.store.RetryJob(ctx, in)
}

// GetStage performs the corresponding knowledge domain operation under the SDK contract.
func (e *Engine) GetStage(ctx context.Context, stageID string) (*Stage, error) {
	return e.store.GetStage(ctx, stageID)
}

// ListStages performs the corresponding knowledge domain operation under the SDK contract.
func (e *Engine) ListStages(ctx context.Context, jobID string) ([]*Stage, error) {
	return e.store.ListStages(ctx, jobID)
}

// GetReceipt performs the corresponding knowledge domain operation under the SDK contract.
func (e *Engine) GetReceipt(ctx context.Context, requestID string) (*Receipt, error) {
	return e.store.GetReceipt(ctx, requestID)
}

// FailJob records the actual job failure while retaining published results.
func (e *Engine) FailJob(ctx context.Context, in FailJobInput) (*Receipt, error) {
	return e.store.FailJob(ctx, in)
}

// GetActiveJob 返回库内正在执行或下一位等待执行的任务；队列为空时返回 nil。
func (e *Engine) GetActiveJob(ctx context.Context, libraryID string) (*Job, error) {
	return e.store.GetActiveJob(ctx, libraryID)
}

// ListJobsForActor 在服务端计数和分页前按真实接纳主体过滤，不返回其他 Key 的过程状态。
func (e *Engine) ListJobsForActor(ctx context.Context, libraryID, actorID string, offset, limit int) ([]*Job, int64, error) {
	return e.store.ListJobsForActor(ctx, libraryID, actorID, offset, limit)
}
