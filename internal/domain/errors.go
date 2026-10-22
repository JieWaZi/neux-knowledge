package types

import "fmt"

// ErrorCode is the standalone SDK's stable error classification.
type ErrorCode string

const (
	ErrInvalid                ErrorCode = "invalid"
	ErrNotFound               ErrorCode = "not_found"
	ErrVersionConflict        ErrorCode = "version_conflict"
	ErrIdempotencyConflict    ErrorCode = "idempotency_conflict"
	ErrScopeViolation         ErrorCode = "scope_violation"
	ErrSourceUnavailable      ErrorCode = "source_unavailable"
	ErrCancelled              ErrorCode = "cancelled"
	ErrStaleAttempt           ErrorCode = "stale_attempt"
	ErrExecutionNotSuccessful ErrorCode = "execution_not_successful"
	ErrCandidateInvalid       ErrorCode = "candidate_invalid"
	ErrManualProtected        ErrorCode = "manual_protected"
	ErrDatabaseBusy           ErrorCode = "database_busy"
	ErrDatabase               ErrorCode = "database_error"
	ErrStorage                ErrorCode = "storage_error"
)

// Error stores a stable category and the original technical cause.
type Error struct {
	Code      ErrorCode // Stable error code that hosts can map.
	Operation string    // Failed domain operation.
	Detail    string    // Technical diagnostics, rather than interface copy.
	Cause     error     // Original underlying cause.
}

func (e *Error) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s: %s: %s: %v", e.Code, e.Operation, e.Detail, e.Cause)
	}
	return fmt.Sprintf("%s: %s: %s", e.Code, e.Operation, e.Detail)
}
func (e *Error) Unwrap() error { return e.Cause }
