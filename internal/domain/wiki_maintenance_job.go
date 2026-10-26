package types

import "context"

// IsMaintenanceJob 区分不重新处理原件的已有 Wiki 维护任务。
func IsMaintenanceJob(job *Job) bool {
	return job != nil && len(job.SourceVersionIDs) == 0 && len(job.Permission.Operations) == 1 && job.Permission.Operations[0] == "maintain"
}

type maintenanceJobKey struct{}

// MaintenanceJobScope 由宿主知识任务驱动冻结，模型不能改变任务身份。
type MaintenanceJobScope struct {
	JobID   string // 当前知识维护任务的稳定身份。
	Attempt int    // 当前任务尝试次数。
}

// WithMaintenanceJob 把独立 ACP 工具调用绑定到原维护任务的当前尝试。
func WithMaintenanceJob(ctx context.Context, jobID string, attempt int) context.Context {
	return context.WithValue(ctx, maintenanceJobKey{}, MaintenanceJobScope{JobID: jobID, Attempt: attempt})
}

// MaintenanceJobFromContext 仅供知识事务读取宿主冻结的任务屏障。
func MaintenanceJobFromContext(ctx context.Context) (MaintenanceJobScope, bool) {
	scope, ok := ctx.Value(maintenanceJobKey{}).(MaintenanceJobScope)
	return scope, ok
}
