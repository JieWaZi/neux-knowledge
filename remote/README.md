# 官方 WeKnora 远端适配

本包只发 HTTP 请求，不连接本地知识 SQLite，也没有 Reader、Generator 或 Runtime 依赖。使用已实现的 WeKnora HTTP 合同；这不是远端部署版本自动探测，其他版本的未知结构返回 `protocol_incompatible`。

读取库详情／列表、页面／搜索、逐层目录、关系图、历史正文、原件分页和二进制。库与原件使用 success/data 包装，Wiki 使用直接对象；页面是 page/page_size，历史是 offset/limit。历史列表不包含当前页，content_version 才携带指定历史正文。

原件下载的服务器权限要求高于普通 Wiki 阅读，不根据库可读推断可下载。每次请求仍由服务器认证和授权；Capability.supported 只说明协议，permission 为 unknown。

定制 raw-only、明确手动整理、归档、原子恢复、版本化编辑、软件规则与人工位置保护未开放。固定上游页面 handler 虽然先检查调用方版本，service UpdatePage 又读取当前版本，无法证明从调用方基础版本到提交的连续 CAS；因此不声称端到端并发保护。原子恢复还缺调用方预期版本。禁止靠上传后取消、普通删除或本机模型补齐这些合同。

请求有超时和响应字节上限；禁止重定向，避免凭据发送到另一服务器；错误不包含令牌、地址中的凭据或远端原始响应。分层 slug 保留原值，逐段拒绝路径遍历。不会将断线、认证失败或未知结构报告为空结果。

合同依据为固定源码的 `internal/router/routes_knowledge.go`、`internal/handler/{wiki_page,knowledgebase,knowledge}.go`、`internal/types/wiki_page.go` 与 `internal/application/service/wiki_page.go`。此适配器为新写的 HTTP 客户端，未复制其宿主实现；复用范围与许可见 [第三方说明](../THIRD_PARTY_NOTICES.md)。

`weknora_test.go` 使用按上述源码构造的 HTTP fixtures，验证包装、分页、Unicode 分层句柄、历史正文、二进制、错误和重定向。测试不是对在线官方服务的验收；当前联调环境的官方端口不可达。
