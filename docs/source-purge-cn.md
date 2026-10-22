# 来源永久清理

来源清理以一个稳定 `SourceID` 为单位，覆盖它的全部版本。它与归档不同：确认后不能读取、恢复或重新使用这些来源身份。库和页面永久删除不在此接口范围。

## 预览、确认与恢复

`core.Engine` 提供四个公开方法：

```go
PreviewSourcePurge(ctx, PurgePreviewInput{LibraryID, SourceID})
ConfirmSourcePurge(ctx, ConfirmSourcePurgeInput{RequestID, LibraryID, SourceID, PreviewDigest, Confirmed})
GetSourcePurge(ctx, libraryID, purgeID)
RecoverSourcePurge(ctx, RecoverSourcePurgeInput{RequestID, LibraryID, PurgeID, ExpectedPolicyVersion})
```

自有服务器客户端 `remote.Custom` 提供同名方法；它已冻结目标库，故查询签名为 `GetSourcePurge(ctx, purgeID)`，其余输入仍显式核对目标库。

预览只读，返回来源版本集合、页面版本与正文保留策略、历史及内部材料数量、共享原件数量、原件根归属和 `blockingJobIds`。公开结果不含文件名、原文、摘录或原件摘要键。`digest` 绑定实际库配置、权限和结构代次，包含已归档页面的引用及版本、历史、任务、全部阶段尝试、Lite收尾增量、问题和未完成导入。确认事务重读同一范围；任一影响发生变化都拒绝旧摘要，调用方必须重新预览。损坏、无法编码的持久快照不能生成有效确认摘要。

确认必须传完整 `previewDigest` 和布尔 `confirmed: true`；字符串、缺省值和 `false` 都不能表示确认。同库有排队、运行或取消未确认任务，以及仍运行的阶段时，确认拒绝，不暗中取消任务。

确认在一个 SQLite 事务中撤销来源、清理内部副本、写入最小墓碑与持久清理清单，返回 `accepted` 回执和清理 `ResourceID`。此时来源已不可访问，文件清理状态为 `pending`。确认本身不会自动执行磁盘清理。

恢复需独立请求身份和当前 `ExpectedPolicyVersion`，按原清单继续。`GetSourcePurge` 才是当前进度事实：

| 状态 | 含义 |
| --- | --- |
| `pending` | 清单已保存，磁盘对象尚待处理，或上次处理因故未提交进度 |
| `completed` | 已处理清单中全部对象；其他来源仍使用的共享原件明确计入保留数量 |
| `blocked` | 保留远端原件归属诊断；本地身份异常直接拒绝操作 |

同请求身份、同输入返回原已持久回执，不同输入拒绝。旧确认回执不会随恢复改成 `completed`；刷新清单状态。失败且未持久回执的恢复可使用原请求身份重试。远端响应不明时先查询同一 `RequestID` 的回执及清单，不创建替换请求或静默重新确认。

## 清理与保留事实

确认删除本来源所有版本与当前来源头，保留不带文件名或原文的身份墓碑。受影响页面移除本来源的证据及摘录，清空摘要、分块引用与自由元数据；安全元数据只保存 `sourcePurge` 待核查标记和不可用来源／版本／清单身份。

`core.ParseSourcePurgeReview` 对该标记作同一白名单投影，供宿主页面 DTO、核查与导出复用。核查为保留页的不可用版本生成 `stale_ref`；导出清单和 frontmatter 的 `sourcePurgeReview` 仅保留待核查事实与合法核心身份，不读取已删除资料。

人工编辑、有保护片段、仍有其他来源或代码证据的当前页面保留正文，并标记待核查。纯生成且只依赖本来源的页面清空正文、别名和出链后归档。受影响页面的全部历史正文、摘要、保护片段、别名和自由元数据被清空，历史版本身份仍在。此策略无法自动判断人工或多来源正文中的每一句是否来自被清理资料，保留正文需要人工核查。

阶段快照可能包含任务显式来源以外的同库阅读材料。因此本次确认会清空同库终态任务的诊断、阶段输入／模型原文／诊断与用量，删除同库Lite收尾增量与持久问题，不只按任务来源列表猜测。活任务已在确认前阻塞；任务重试和历史恢复不能把已撤销来源证据重新发布。

原导入请求有真实成功回执时，重复调用只重放回执，不重新写原件；来源已清理时此旧回执不表示来源仍可读。业务登记中断的导入会在写字节前保存精确意图，固定源身份、对象键和临时文件名。确认也清理本来源的这些对象，并撤销未完成请求，禁止其重放重建原件。全新来源尚未登记成功的中断导入可用原请求恢复；这里不提供扫描无主文件的一般垃圾回收。

## 原件归属、并发与崩溃

新空原件根在跨进程操作系统锁内绑定当前数据库规范路径摘要、原件根规范路径摘要与随机根身份；数据库内保存唯一反向绑定，目录保存格式 v2 声明。两向身份必须一致，同一数据库换到另一根、复制目录声明或把原目录替换为空目录都不能获得删除权限；另一数据库使用已绑定根时也拒绝。首次认领先取得数据库写锁，避免不同目录各持 OS 锁而同时认领。绑定记录不保存个人绝对路径。

当前只接受格式v2的双向归属声明。已有非空且没有有效声明的根、空随机身份或数据库反向绑定缺失都会直接拒绝打开或清理，不认领已有对象，也不提供未知归属的逻辑删除降级。移动数据库或原件根后必须由维护者显式处理绑定；核心不会自动改绑。

导入、读取、预览、确认和恢复共用原件根锁；锁文件释放后保留，避免删除文件造成不同进程锁住不同 inode。删除前在 SQLite 写事务中重新检查所有库的来源引用及尚未完成的导入意图，共享内容对象不能误删。当前权限核验和对象检查到文件删除期间持有写事务，阻止权限与引用变化穿过检查窗口。

持久清单先于文件删除提交。文件删除失败时清单保持待处理；进程退出或删除后数据库提交失败时，重开核心后按同清单恢复，文件已不存在视为已处理。受管临时文件只删除原请求固定的精确名称，不扫描任意路径。

## CLI 与 HTTP

CLI 使用与 SDK 相同的 JSON 输入；`source.purge.list` 以 `libraryId/offset/limit` 分页读取持久清理清单，关闭界面或重开进程后仍能取得原清理 ID。例中身份均为占位值：

```sh
neux-knowledge --db ./data/knowledge.db --raw ./data/raw source.purge.preview <<'JSON'
{"libraryId":"library-demo","sourceId":"source-demo"}
JSON
neux-knowledge --db ./data/knowledge.db --raw ./data/raw source.purge.confirm <<'JSON'
{"requestId":"purge-confirm-demo","libraryId":"library-demo","sourceId":"source-demo","previewDigest":"替换为完整预览摘要","confirmed":true}
JSON
neux-knowledge --db ./data/knowledge.db --raw ./data/raw source.purge.get <<'JSON'
{"libraryId":"library-demo","purgeId":"purge-demo"}
JSON
neux-knowledge --db ./data/knowledge.db --raw ./data/raw source.purge.recover <<'JSON'
{"requestId":"purge-recover-demo","libraryId":"library-demo","purgeId":"purge-demo","expectedPolicyVersion":1}
JSON
```

自有 HTTP 路径基于 `/api/knowledge/v1/libraries/{libraryId}`：

| 方法与路径 | 权限 |
| --- | --- |
| `GET /source-purges?offset=0&limit=50` | 当前库 `read`；重开后恢复原清理入口 |
| `GET /source-purges/preview?sourceId=...` | 当前库 `read` |
| `POST /source-purges/confirm` | 当前库独立 `purge`、写 IP 白名单及当前权限代次 |
| `GET /source-purges/{purgeId}` | 当前库 `read` |
| `POST /source-purges/{purgeId}/recover` | 当前库独立 `purge`、写 IP 白名单及当前权限代次 |

`write`、`organize`、`maintain` 均不包含 `purge`。官方 WeKnora 适配不宣称拥有此自有协议能力。本地 SDK 和 CLI 的账号授权由可信宿主负责，不能用直接 SDK 调用替代远端授权检查。

## 明确边界与验证

这里交付来源身份撤销、内部副本清理和可恢复的受管文件删除，不承诺 SQLite 空闲页、WAL／日志、文件系统介质、系统快照或备份的物理擦除。已有 Markdown 导出、Ally 聊天、宿主执行记录和未带来源身份的用户资料不属于核心清理范围。`completed` 仍可能保留其他来源共享的原件及人工／多来源当前正文，应同时呈现计数与保留边界。

回归覆盖旧摘要／策略变化拒绝、显式确认、同请求重放、共享内容保留、旧导入防复活、未完成导入与临时对象、活任务阻塞、当前模型诊断／收尾增量／历史清理、原件根身份异常拒绝、真实子进程文件锁、文件删除失败和删除后数据库提交失败恢复，并通过真实 SQLite、HTTP 与 CLI 调用核对。这些证据不证明底层介质物理擦除或 Ally 的视觉验收。
