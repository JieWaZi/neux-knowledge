// Package httpapi provides an embeddable HTTP boundary containing only knowledge use cases and exposes no host administration APIs.
package httpapi

import (
	"context"
	"github.com/JieWaZi/neux-knowledge/core"
)

const BasePath = "/api/knowledge/v1"

// Authorization freezes the accepted identity and provides a reacquirable revocation guard for actual publication.
type Authorization struct {
	Subject      string                                // Authenticated principal, not a client-reported identity.
	Permission   core.PermissionSnapshot               // Actual library scope and permission generation verified by the server.
	PublishGuard func(context.Context) (func(), error) // Recheck the policy under a read lock; release it after the publication transaction ends.
	Recovery     RecoveryIdentity                      // Persistable original authorization identity without the token content.
}

// RecoveryIdentity is stored only by the host; recovery jobs do not accept this identity from clients.
type RecoveryIdentity struct {
	Subject          string                  `json:"subject"`          // Authenticated principal at acceptance.
	CredentialSHA256 string                  `json:"credentialSHA256"` // Original credential digest used only to verify that the same credential remains valid.
	LibraryID        string                  `json:"libraryId"`        // Frozen library target.
	Operation        string                  `json:"operation"`        // Originally authorized operation.
	RemoteAddr       string                  `json:"remoteAddr"`       // Original actual TCP peer.
	ForwardedFor     string                  `json:"forwardedFor"`     // Original request forwarding chain, rechecked against current trusted-proxy rules during recovery.
	Permission       core.PermissionSnapshot `json:"permission"`       // Scope and policy generation at acceptance.
}

// OrganizeRequest selects only a server-provisioned profile and accepts no local accounts, CLI paths, or code paths.
type OrganizeRequest struct {
	RequestID        string   `json:"requestId"`             // Original idempotent request identity.
	LibraryID        string   `json:"libraryId"`             // Target knowledge library.
	ExpectedVersion  int64    `json:"expectedVersion"`       // Current library version.
	StructureVersion int64    `json:"structureVersion"`      // Current catalog generation.
	SourceVersionIDs []string `json:"sourceVersionIds"`      // Explicitly frozen source versions.
	ProfileID        string   `json:"profileId"`             // Server-provisioned execution profile identity.
	Rebuild          bool     `json:"rebuild,omitempty"`     // Explicitly rebuild selected sources; when omitted, retain ordinary organization semantics.
	Instruction      string   `json:"instruction,omitempty"` // Requirements for this organization run without changing the server execution identity or authorization scope.
}

// MaintenanceRequest is passed to the deployment's actual session use case; the HTTP layer does not start another model host.
type MaintenanceRequest struct {
	RequestID           string `json:"requestId"`           // Original idempotent request identity.
	LibraryID           string `json:"libraryId"`           // Target knowledge library.
	ExpectedVersion     int64  `json:"expectedVersion"`     // Current library version.
	StructureVersion    int64  `json:"structureVersion"`    // Current catalog generation.
	ProfileID           string `json:"profileId"`           // Server-provisioned profile identity.
	Instruction         string `json:"instruction"`         // User maintenance instructions, never used as authorization or a path.
	PageID              string `json:"pageId"`              // Explicitly frozen maintenance target page.
	ExpectedPageVersion int    `json:"expectedPageVersion"` // Page version already read by the user.
}

// Profile exposes only selectable server profile identities without execution accounts or paths.
type Profile struct {
	ID   string `json:"id"`   // Fixed server profile identity.
	Name string `json:"name"` // User-visible name.
}

// ExecutionPort is assembled by an existing host business service and must pass PublishGuard to the core pipeline.
type ExecutionPort interface {
	Organize(context.Context, OrganizeRequest, Authorization) (*core.Receipt, error)
	Maintain(context.Context, MaintenanceRequest, Authorization) (*core.Receipt, error)
}

// Options declares independent authentication and actual execution ports; missing authentication denies every request.
type Options struct {
	Security  *PolicyStore  // Dynamic policy store shared by all entry points and publication guards.
	Execution ExecutionPort // Organization and maintenance return unsupported when execution is not assembled.
	Profiles  []Profile     // Server-provisioned profiles selectable remotely, without secrets.
}

// APIError returns only stable business codes to callers without exposing underlying file paths or credentials.
type APIError struct {
	Code   string `json:"code"` // Stable machine error code.
	Status int    `json:"-"`    // HTTP status.
}

func (e *APIError) Error() string { return e.Code }

// List returns explicit actual pagination facts.
type List[T any] struct {
	Items  []T   `json:"items"`  // Actual records on this page.
	Total  int64 `json:"total"`  // Actual total within the authorized scope.
	Offset int   `json:"offset"` // Starting offset.
	Limit  int   `json:"limit"`  // Bounded page limit.
}

// Capabilities distinguishes protocol support, current authorization, and execution availability.
type Capabilities struct {
	Profile    string                      `json:"profile"`    // Fixed protocol contract identity.
	Operations map[string]bool             `json:"operations"` // Actions actually available for the current request.
	Profiles   []Profile                   `json:"profiles"`   // Currently selectable server profiles.
	Details    map[string]CapabilityStatus `json:"details"`    // Separate protocol support, credential permissions, and current resource conditions.
}

// CapabilityStatus prevents treating protocol support as current credential or resource authorization.
type CapabilityStatus struct {
	Supported  bool   `json:"supported"`  // Whether the current service actually implements this use case.
	Permission string `json:"permission"` // allowed or denied, computed per request.
	Condition  string `json:"condition"`  // ready or an explicit policy or execution-profile restriction.
}
