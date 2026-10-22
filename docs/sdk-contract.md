# 知识核心 SDK 合同

Go module 为 `github.com/JieWaZi/neux-knowledge`，公开入口是 `core`。生命周期与生成端口见 [core/organize.go](../core/organize.go)，业务输入和持久模型见 [领域输入](../internal/domain/inputs.go)、[领域对象](../internal/domain/entities.go)。SDK 不暴露 GORM，调用者不需要导入 internal。

## 存储与业务入口

`Open` 需要独立 SQLite 文件和受管 raw 根目录，`Close` 释放资源。原件保留上传原字节，上传不触发模型。库、原件、Wiki、主题、修订、索引、核查、导出与删除分别通过 core 业务方法访问；CLI 当前操作以 `discover` 为准。

写入携带明确请求标识和期望版本。原件由稳定 `SourceID` 与不可变 `SourceVersionID` 共同定位：重新导入产生新版本，重建保留原件并清除所选版本的派生块、向量和摘要。Wiki 的来源证据保留实际版本、摘要和位置。

## 直接模型与阅读端口

`Generator.Generate(ctx, GenerateRequest)` 只接收本次 `Messages` 和模型参数，返回 `GenerateResult.Text` 原文、`FinishReason`、真实用量与原生能力诊断。`JobID`、`SourceVersionID`、`StageID`、`Stage` 只用于知识诊断，不是聊天或通用执行身份。

普通 Wiki 调用温度0.3、Thinking=false、输出预算32768；普通文档档案使用原系统模板与独立用户正文、8192 rune输入采样、1024输出预算，并拒绝length截断。模型端口不得跨请求保留会话，不获得文件提交、CLI或工具权限。只有一次页改写遇length时，由核心在该次改写内部携带已返回片段，最多额外续写3次；仍截断时保留原页。

`ReaderPort` 收到固定原件字节与身份，返回实际覆盖、块、原文rune范围和确认位置。partial、unsupported与实际读取失败不能升级成完整阅读。`PrepareDocument` 负责实际分块和必要的原始文档索引；可调用 `PrepareIndexedDocument`。Wiki 引用使用已持久的实际子块，重叠内容按上游 `MergeTextChunks` 重建。可选 `Embed` 提供冻结的真实嵌入能力，供上游目录相似度筛选使用；不可用时沿上游有界目录回退。

调用方需要动态撤权时，将同一宿主闸门传入 `OrganizeInput.PublishGuard` 与用于索引的 `DocumentEmbedder.PublishGuard`。页面、索引导言和链接维护在提交前复核；原件索引在外部嵌入返回后取得闸门并覆盖分块与向量持久化。外部模型与嵌入请求不持发布锁，撤权无需等待网络返回，迟到结果不得落库。

## 接纳、运行与发布

`AcceptJob` 冻结来源、权限和库配置，不调用模型。`RunOrganize` 读取持久任务，以库级系统文件锁串行批次；锁被旧任务持有时等待实际释放，不丢弃新接纳任务。默认batch=5、Map=10、Reduce=10、cite=4；WikiConfig沿固定上游规则覆盖相应批次参数。

Wiki 文档使用Pass0候选提取，失败时回退旧完整提取器；文档内语义去重后并行生成Wiki摘要与块引用。引用单批失败不取消兄弟批次，未知块句柄被丢弃，无引用知识使用真实文档级Details回退。精确已有身份优先，批次内身份保留和cite后reclaim防止重复页面。

整个批次先统一目录规划，再按slug聚合来源归并。换版通过稳定SourceID读取旧页面与旧摘要，同主题发出retract+addition，消失主题发出retractStale；适配层据此移除旧版本证据，只添加本次真实引用。已有人工位置和保护内容继续由短事务校验。

页面生成失败保持原页；未能创建的新知识链接从该批摘要清除。真实写入失败和表格行保护拒绝使相关来源重排，成功兄弟页面保持发布。调用瞬态重试最多3次，等待2秒、4秒。文档待办初次加最多5次重排，失败次数和NotBefore持久化，第6次失败进入dead；普通重排5秒，限流60秒。确定性阅读能力限制与来源失活直接结算，不消耗模型重试预算。

页面、修订、来源结论和回执在SQLite事务内保存，检查任务尝试、取消、权限、来源存活、目标页面和目录版本。模型文本不能直接写Wiki。每次页面提交同时写持久finalize增量，所有待办收束后统一更新索引和affected页链接；首版索引最多200份近期摘要，增量只发送已有简介与本次变化。索引模型失败沿上游默认或保留旧简介。链接维护先修复可安全解析的坏slug，再对affected页的已有链接与新写入标题做自动链接，不改动无关页面或增加内容修订版本。

显式整理使用上述同一次Run收束全部待办和一次finalize，替代上游Asynq触发器和20秒debounce；同库互斥、持久待办和局部失败规则保留。当前产品在Wiki开启时走Wiki摘要，Wiki关闭时走普通文档档案，不声称复现上游完整post_process调度或单独SummaryEnabled开关。

## 取消、恢复与诊断

内部生命周期使用 `CancelOrganize` 先立持久屏障，再同步取消Run的Context；不提供用户停止整理入口。阅读、模型或索引真实结束后结算屏障，已发布页保留。`FailOrganize` 供模型准备或运行的不可恢复故障结算：未完成来源失败，成功来源与页面保留；宿主关闭导致的Context取消不调用此入口，以便重启恢复。

进程退出后，系统文件锁自动释放。恢复把未返回的纯知识阶段标为中断，继续持久来源待办；未完成来源重新执行Map，不把一次transport成功的坏JSON当作可复用产物。完成来源不重新领取。`RetryJob` 创建新尝试，并重新检查权限与来源，旧尝试不能影响新尝试。

`Stage` 保存当前任务尝试、来源读集、真实输入、结果文本、停止原因、解析或调用错误、用量和原生诊断。`OrganizeSourceWork.StartedAt` 是实际开始Reader前提交的事实。`OnProgress` 只在来源开始与重排事实提交后通知宿主；`OnPublished` 只报告真实提交回执，二者不拥有状态或回滚权。

当前开发结构版本为1，保存在 `knowledge_schema` 的唯一记录中。空数据库一次性建立当前全部表与索引；已有数据库只校验版本、完整表、列与必要索引，不补列、不执行历史迁移，也不识别或处理旧任务和候选表。开发数据由维护者在应用关闭后显式整理。

所有验证依靠稳定ErrorCode与原始技术诊断；不依赖错误文本决定授权。真实模型质量、CLI能力限制和远端部署仍需相应环境验收。

## 模型与检索边界

核心向Generator保留原system/user/assistant消息顺序和正文。NeuxAlly的Codex独立文本适配使用临时原生主目录，只沿用认证与provider连接；原system正文通过model_instructions_file替换内置基础指令，原user正文直接发送。仅user调用因原生CLI要求基础指令非空，使用明确记录的中性适配句，不把它称为上游system。关闭AGENTS读取、内置skills与自动skills指令，清除模型目录的代码助手指令；普通Chat不走此路径。assistant续写历史及其他Runtime仍经ACP文本序列化，其限制明确诊断。原生权限/会话元数据和部分工具定义仍可能存在，Thinking、输出上限与温度是否可控以本次Diagnostics为准，不声称完整Chat API等价或内容质量已经等效。

Wiki-only不要求Embedding。固定上游的vector与keyword都调用检索引擎BatchIndex，二者均要求Embedding；本地vector要求Embedding，keyword使用SQLite关键词分支，因此不要求Embedding。GraphEnabled对应上游独立实体关系抽取，不是Wiki页面出链和反链；本地未接入实体关系抽取。Wiki出链来自正文wikilink及上游Finalize标题匹配，反链由出链投影；生产Finalize不匹配新页别名，也不把不相干全文强行链接。
