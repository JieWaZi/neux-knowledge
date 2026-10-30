package pipeline

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

type retryMarkedError struct {
	// message 保留原始错误信息供诊断。
	message string
	// retryable 表示调用方是否允许重试。
	retryable bool
}

func (e retryMarkedError) Error() string   { return e.message }
func (e retryMarkedError) Retryable() bool { return e.retryable }

func TestTransientLLMErrorHonorsRetryVeto(t *testing.T) {
	for _, message := range []string{"timeout", "connection reset", "status 503"} {
		for _, wrapped := range []bool{false, true} {
			err := error(retryMarkedError{message: message})
			if wrapped {
				err = fmt.Errorf("model call: %w", err)
			}
			if isTransientLLMError(context.Background(), err) {
				t.Errorf("message %q wrapped=%v: retry veto ignored", message, wrapped)
			}
		}
	}
}

func TestTransientLLMErrorPreservesClassification(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cases := []struct {
		// name 标识测试场景。
		name string
		// ctx 控制父任务是否已取消。
		ctx context.Context
		// err 是待分类错误。
		err error
		// want 是期望的重试判断。
		want bool
	}{
		{"nil", context.Background(), nil, false},
		{"cancelled", ctx, errors.New("timeout"), false},
		{"marker allows validation error", context.Background(), retryMarkedError{message: "invalid result", retryable: true}, false},
		{"marker allows transient error", context.Background(), retryMarkedError{message: "timeout", retryable: true}, true},
		{"legacy transient error", context.Background(), errors.New("connection reset"), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isTransientLLMError(tc.ctx, tc.err); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}
