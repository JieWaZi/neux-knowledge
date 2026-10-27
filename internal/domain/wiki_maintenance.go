package types

// ReplacePageTextInput exactly replaces all matching text in current content while preserving other manual protection.
type ReplacePageTextInput struct {
	RequestID             string        // Stable idempotent request identifier.
	LibraryID             string        // Authorized target library.
	PageID                string        // Stable page identifier.
	ExpectedVersion       int           // Current content version.
	StructureVersion      int64         // Current directory generation.
	ExpectedPolicyVersion int64         // Frozen permission generation; zero adds no extra freeze.
	OldText               string        // Exact original text that must actually match.
	NewText               string        // Replacement content; empty explicitly deletes the matches.
	Evidence              *PageEvidence // Optional complete evidence replacement; omission preserves existing evidence.
}

// RenamePageInput changes the readable page identifier while preserving page identity and atomically cascading links.
type RenamePageInput struct {
	RequestID             string // Stable idempotent request identifier.
	LibraryID             string // Authorized target library.
	PageID                string // Stable page identifier.
	ExpectedVersion       int    // Current content version.
	StructureVersion      int64  // Current directory generation.
	ExpectedPolicyVersion int64  // Frozen permission generation; zero adds no extra freeze.
	NewSlug               string // New readable identifier unique within the library.
}

// DeletePageInput 归档页面并保留历史；可选规范页和双端守卫将入链原子改向规范页。
type DeletePageInput struct {
	RequestID                  string // 稳定幂等请求身份。
	LibraryID                  string // 已授权知识库。
	PageID                     string // 待归档或永久删除的页面身份。
	ExpectedVersion            int    // 已读取的页面版本。
	StructureVersion           int64  // 当前目录版本。
	ExpectedPolicyVersion      int64  // 冻结策略版本；零表示不额外冻结。
	ExpectedUpdatedAt          string `json:",omitempty"` // 归并时已读取的旧页 RFC3339 更新时间。
	CanonicalPageID            string `json:",omitempty"` // 归并的规范页；普通归档可省略，永久删除必填。
	CanonicalExpectedVersion   int    `json:",omitempty"` // 已核实的规范页版本。
	CanonicalExpectedUpdatedAt string `json:",omitempty"` // 已核实的规范页 RFC3339 更新时间。
}

// PurgePageInput 永久清理已归档重复页，复用归并两端的完整并发守卫。
type PurgePageInput = DeletePageInput
