# 整库永久删除

`Engine.DeleteLibrary` 接收 `DeleteLibraryInput{RequestID, LibraryID, ExpectedVersion}`。期望版本是用户确认的库配置版本。归档库也可以删除；存在 queued、processing、cancel_requested 任务或 running 阶段时返回版本冲突。原件锁与 SQLite 写事务覆盖引用检查和业务撤销，旧任务在库删除后无法继续发布。

事务永久移除本库页面（包括软删除）、历史、问题、主题、来源、任务、阶段、Lite收尾增量、导入意图与来源清理记录，并原子保存整库删除清单和 `library.delete` 回执。最小回执和删除身份保留以支持幂等与未知结果对账，不保存知识正文。原件按精确内容键清理，其他库或导入中的共享引用保留；来源清理尚未完成的对象清单也被整库删除继承。

删除提交后立即尝试清理原件。回执的 `accepted` 仅表示业务删除和持久清单已提交；调用 `GetLibraryDeletion(ctx, libraryID)` 查看 `pending` 或 `completed` 和实际对象计数。磁盘故障保持 pending；数据库与原件根身份不一致时拒绝删除。公开协议保留远端 `blocked` 诊断，本地不采用未知归属降级。`RecoverLibraryDeletion` 使用新的 requestId、库身份和清单 `ExpectedVersion` 继续原清单，状态与回执原子提交。响应丢失时使用相同输入和 requestId 重试，或读取原回执。

打开核心时在原件锁内自动推进最多一百个 pending 清单。单条磁盘删除失败不阻止核心打开；pending 清单可显式恢复。不会扫描或删除清单外的文件。

自有远端提供以下合同：

- `POST /api/knowledge/v1/libraries/{id}/delete`：永久删除，独立 `purge` 授权。
- `GET /api/knowledge/v1/libraries/{id}/deletion`：读取删除状态。
- `POST /api/knowledge/v1/libraries/{id}/deletion/recover`：按清单版本恢复，独立 `purge` 授权。

能力名为 `libraryDelete`。删除后仍可访问状态、恢复与原回执；每次请求继续检查当前凭据、固定库范围、删除时策略代次和写入 IP 限制。远端客户端只转发固定资源，不能改成本机删除。官方 WeKnora 的整库及原件永久删除合同未经验证，能力保持不支持。

永久删除不承诺 SQLite 空闲页、存储介质、备份、已导出文件或宿主聊天的物理擦除。测试覆盖数据库和受管原件合同，不替代真实远端平台与视觉验收。
