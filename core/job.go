package core

import (
	"context"

	types "github.com/JieWaZi/neux-knowledge/internal/domain"
)

// IsMaintenanceJob 判断任务是否维护已有 Wiki，而非重新处理原件。
func IsMaintenanceJob(job *Job) bool { return types.IsMaintenanceJob(job) }

// WithMaintenanceJob 将本次 ACP 工具写入约束到原维护任务与尝试。
func WithMaintenanceJob(ctx context.Context, jobID string, attempt int) context.Context {
	return types.WithMaintenanceJob(ctx, jobID, attempt)
}

// ActivateQueuedJob 只启动当前库的队首任务，维护与原件整理共用顺序。
func (e *Engine) ActivateQueuedJob(ctx context.Context, jobID string) (*Job, bool, error) {
	return e.store.ActivateQueuedJob(ctx, jobID)
}

// CompleteMaintenanceJob 保存独立 ACP 的真实报告并结算维护任务，不修改来源状态。
func (e *Engine) CompleteMaintenanceJob(ctx context.Context, in JobActionInput, report string) (*Receipt, error) {
	return e.store.CompleteMaintenanceJob(ctx, in, report)
}

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
