# 自有知识 HTTP 边界

`httpapi` 是可嵌入的 `net/http.Handler`，复用 `core.Engine` 的真实 SQLite、原件与领域事务。它只公开 `/api/knowledge/v1/` 下的知识操作，不公开宿主管理、任意文件访问、阶段候选或发布控制面。官方 WeKnora 使用独立的固定版本协议，见 `remote/README.md`；本文不扩充官方服务器能力。

## 装配与执行

宿主用 `httpapi.New(engine, httpapi.Options{Security: policies, Execution: execution, Profiles: profiles})` 建立 Handler，再挂载到专用知识路径。`engine` 生命周期由宿主管理，不能把整个 NeuxAlly 管理 API 作为远端知识入口。

能力返回 `details` 分开描述服务器支持、当前凭据权限和资源条件；`operations` 是由这些事实派生的当前可用布尔值，不是独立授权源。

公开 Go 类型与路由分别以 `httpapi/types.go`、`httpapi/handler.go` 为权威。读取包括库、页面、slug、关键词分页、主题、原件、二进制下载、实际来源分块、活动、历史版本、任务与原请求回执；写入包括库配置、新库、raw-only 上传、原件归档、页面版本写入／恢复和主题维护。页面写入与恢复同时携带当前页面版本和结构代次，主题以结构代次保护。来源全部版本的永久清理有独立预览、确认、进度与恢复入口，见[来源永久清理](source-purge-cn.md)；整库永久删除见[库删除](library-delete-cn.md)。页面永久删除与 SDK 尚未提供的其他动作没有 HTTP 能力。

自有协议使用 JSON；核心输入采用公开 SDK 字段名，JSON 解码也接受大小写对应的 lowerCamelCase。领域对象保持 SDK 既有 JSON 标签，包括 Wiki 的上游 snake_case 字段。上传 `Bytes` 是 JSON base64，解码后原字节直接交给 `core.ImportSource`。单请求正文上限 48 MiB，原件不会被当成 JSON 文本转码；下载为 `application/octet-stream` 与文件名附件头。客户端上传前应控制原件大小，base64 后须满足请求正文上限。分页列表默认 50、最大 100，返回 `items/total/offset/limit`；主题是 SDK 当前完整主题集合。来源分块只返回目标版本已经保存的解析产物，不重新解析或调用模型；未解析版本返回空数组，不存在或越界的版本返回错误。活动保留服务端实际持久事件、筛选与 `next_cursor`，不能从任务列表合成历史。

整理和页面续改通过 `ExecutionPort` 注入既有服务端业务。`OrganizeRequest` 接受服务端配置 `profileId`、明确原件版本集合和库／目录版本；可选 `rebuild` 表示真实重建所选来源，执行宿主必须传入核心同名接纳参数，不能以普通整理代替。可选 `instruction` 保存本次用户要求，最多 65536 字节，并随原请求冻结；两者省略时保留原整理语义。`MaintenanceRequest` 还冻结精确 `pageId/expectedPageVersion`。未知字段被拒绝，客户端不能传本机账号、CLI 路径或本机项目目录。`Options.Profiles` 是服务器预配的非秘密配置身份清单；清单外的配置为 `unsupported_profile`。缺少执行端口时上述动作返回 HTTP 501 `unsupported`，不会启动本机模型或另外建立 CLI 宿主。

回调必须将 `Authorization.Permission` 用于接纳，且在实际接纳／人工写入前取得 `Authorization.PublishGuard`。执行期间每次发布把同一闸门传入 `core.OrganizeInput.PublishGuard`，由管线持锁包围页面、索引导言与链接维护事务；原件索引的闸门注入遵守 [SDK 发布合同](sdk-contract.md#直接模型与阅读端口)。回调的成功结果必须是核心已经持久的 `Receipt`，不是从聊天文本推测的“完成”。内部权限撤销和生命周期取消仍以持久屏障阻止迟到发布；不提供用户停止整理入口。

## 认证、范围与撤销

默认没有独立认证配置即拒绝请求。Bearer 令牌映射到 `Principal`，逐库配置 `LibraryGrant` 的操作、真实项目范围和 `PolicyVersion`；没有通配库。`read`、`write`、`organize`、`maintain`、`purge` 分别授权；创建库还需单独 `CreateLibrary`。来源分块沿 `read` 授权，活动沿配置编辑的 `write` 授权；重建沿 `organize` 授权并检查执行端口与服务器配置。清理预览与进度使用 `read`，确认与恢复必须显式授予 `purge`，其他写权限不隐含此权。库列表只返回明确获准读取的库；新建库不会自动扩大原凭据范围。主体授权代次必须与核心当前库一致才能写入。HTTP 库配置更新不能修改权限代次；受信宿主通过 SDK 配合配置管理完成权限更新。

所有远端写入口都检查 `WriteIPPolicy`。`Enabled=true` 且 `Allowed` 为空时全部写入被拒绝。默认只看真实 TCP 来源；未配置可信代理时忽略客户端自报转发头。可信 TCP 代理必须提供合法完整的 `X-Forwarded-For` 地址链，所有同名头行合并后验证，最多 32 跳；从右侧跳过显式可信代理，以最近的不可信跳作为实际来源。缺失、非法或全可信且无实际来源的链被拒绝。代理必须可靠覆盖或追加真实来源，不能只转发用户给出的同名头。

`PolicyStore.Replace(SecurityConfig)` 对配置做深拷贝并持写锁原子替换；入口写入与发布闸门持同一读锁。已取得发布锁的事务先完成，策略替换返回后，旧凭据／范围／IP 已被撤销的后续发布无法通过。不要绕过 `Replace` 在其他地方维护另一份发布授权事实。

在途任务可保存 `Authorization.Recovery`，其中只有主体、原凭据 SHA-256、目标库／操作、原真实来源链及权限快照，不保存令牌正文。重启后用同一 `PolicyStore.RestoreGuard(engine, identity)` 重建闸门，重新核对原凭据仍存在、主体、全部操作、范围、IP 和当前核心策略代次。该身份只从宿主持久会话恢复，不接受 HTTP 客户端提交；无法恢复真实身份应阻止任务发布。核心 `PolicyVersion` 还在发布事务内独立防止旧库策略快照通过。

## HTTP 客户端与不确定结果

`remote.NewCustom(options, ResourceRef{Backend: "custom", ConnectionID: "connection-demo", KnowledgeBaseID: "library-demo"})` 只持有 HTTP 连接及完整资源引用。连接发现允许库身份为空，但仅能列获准库或明确新建库；读取、上传、整理和维护必须绑定真实库。写入输入的库身份必须匹配冻结引用。没有本地 Reader／Generator、数据库、影子索引或离线回传；后续刷新仍读权威服务器。

HTTP 默认 30 秒超时，禁止自动跟随重定向，凭据不发往重定向站点。请求身份与 CAS 原样发送，不生成替换 requestId。写请求断线、响应不完整、无法验证回执或可能已写入的服务错误返回 `OutcomeUnknownError{RequestID}`，不自动重试。调用方应查询同一库的 `GetReceipt`，并核对真实任务／资源；结果未明时保留“待确认”。不存在、权限不足、协议不兼容和网络失败分别返回稳定错误，不转成空列表或创建本地同名库。

新库创建丢失响应时，调用方尚不知道库身份，使用连接级 `GetCreationReceipt` 查询原 requestId。服务器仍检查当前 `CreateLibrary` 权限与写来源策略，并用原子保存的 `kb.created` 活动核对原请求、库身份和真实认证主体；仅知道请求身份不能获得其他主体的创建结果。创建 POST 的同键重放同样核对原创建主体。恢复不会自动为凭据添加新库 grant；已恢复的创建操作继续使用连接级回执查询，查无回执或无法确认归属时不重新 POST。

测试覆盖真实 SQLite 双客户端读取、同旧版本 CAS 冲突、同键回放／冲突、历史恢复、人工主题、归档、原件原字节、认证范围、写 IP、转发头链、动态撤销锁、恢复认证、未知任务和取消未确认。HTTP 丢响应测试只证明不盲目重试；结构测试只证明客户端不拥有本地执行／存储依赖。这些证据不代替 NeuxAlly Web 的真实服务装配、官方在线服务或实际服务器模型验收。
