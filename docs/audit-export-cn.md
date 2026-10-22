# 只读核查与 Markdown 导出

SQLite 是知识权威。核查只返回发现，Markdown 导出只生成可移交副本；二者均不创建任务、回执、修订或持久问题记录，也不会调用模型。公开 SDK 入口见 [core/audit_export.go](../core/audit_export.go)，实际 CLI 输入通过 `discover` 查看。

## 核查

`LintLibrary` 在同一个 SQLite 读事务内读取目标库的非归档页面，包括草稿。正文按每批 200 页的主键游标遍历，关系重新从正文解析，代码块和行内代码中的示例链接不成为真实关系。

| 发现 | 判定 | 提示级别 |
| --- | --- | --- |
| `orphan_page` | 没有来自其他非归档页面的入链；自链接不计入 | warning |
| `broken_link` | 内部链接目标在当前库中不存在或已归档 | error |
| `empty_content` | 去除首尾空白后的正文少于 50 个 Unicode 字符 | warning |
| `stale_ref` | Evidence 绑定的同库来源版本不存在、已归档、身份或摘要不符，或已不再是当前来源版本；派生来源引用缺少对应 Evidence，以及清理标记中的不可用版本也会报告 | error |

四项规则改编自固定版本 WeKnora 的只读核查首轮。50 字符沿用上游的短正文提示意图，适配为 Unicode 字符计数；短术语页可能合理命中。核查不判断模型语义是否正确，也不阻止发布。来源被替换或归档后，历史引用仍保留供回溯，发现不会自动删页或删除证据。

来源永久清理后，人工或多来源当前正文可能仍保留。核查从同一页面的安全 `sourcePurge` 标记读取不可用版本，继续返回以该版本 ID 为目标的 `stale_ref`，不需要重读已删除原件或摘录。

报告携带库、结构、策略版本及每个问题的页面身份和版本。数据库读取失败会返回错误，不会伪装成来源失效。不提供 `AutoFix`；CLI 也拒绝 `autoFix` 参数。

```sh
printf '%s' '{"libraryId":"library-example"}' |
  go run ./cmd/neux-knowledge --db ./data/knowledge.db --raw ./data/raw audit
```

## 导出

`ExportMarkdown` 在一个 SQLite 读事务中固定库配置、已发布页面、主题父链和来源身份。默认导出全部 `published` 页面；可传 `pageIds` 选择最多一千个不同页面。指定的页面不存在、属于其他库或尚未发布时，整个导出返回错误，不静默省略。草稿、归档页、历史修订和原件不进入导出。

每个文件包含 JSON 形式的 YAML frontmatter 和原样知识正文。元数据保存页面 ID／版本、slug、标题、类型、语言、当前主题名称链、排序、库／结构／策略版本，以及来源版本 ID、代次、SHA-256 和定位。标题、主题和来源文件名经过 JSON 转义，换行等字符不会注入新的 frontmatter 字段。来源原件、受管 raw 路径和 Evidence 原文摘录不写入导出；已有软件代码证据作为元数据保留。

已清理来源的待核查事实以 `sourcePurgeReview` 导出，只包含明确布尔值、不可用来源／版本和清理身份；不透传任意 `PageMetadata`。身份白名单只接受核心生成的 32 位小写十六进制身份，去重并排序，避免旧自由元数据夹带原文。

输出文件名固定为 `page-<页面ID的SHA-256>.md`，不会把 slug、标题、来源文件名或主题名称作为磁盘路径。正文中的 Wiki 链接语法保留；清单中的页面身份与 slug 可供接收方映射。导出不把文件变成另一套可写权威，也不支持自动回灌。

不传 `--out` 时，CLI 输出含 Markdown 字符串和文件摘要的 JSON bundle。传 `--out` 时，CLI 在父目录下独占创建一个全新目录，写入 `.md` 文件，最后写入不含正文的 `manifest.json`；清单中的 SHA-256 对应完整 Markdown 文件字节。

```sh
mkdir -p ./exports
printf '%s' '{"libraryId":"library-example"}' |
  go run ./cmd/neux-knowledge --db ./data/knowledge.db --raw ./data/raw \
    --out ./exports/snapshot-example export
```

父目录必须已存在。已有输出目录、文件名冲突、目录穿越及数据库／raw 路径重叠均被拒绝。写入使用受限目录句柄；普通错误会清理本次创建的文件。清单最后写入是完成标记，整个目录没有原子重命名或跨崩溃持久化承诺；若进程中断，应移走不完整目录后重新导出。不会覆盖已有成果。

调用方仍须按自己的账号和授权范围选择 `libraryId`。自有 HTTP 提供逐库 `read` 授权的 `GET /api/knowledge/v1/libraries/{libraryId}/audit` 与 `GET /api/knowledge/v1/libraries/{libraryId}/markdown-export`；导出可重复传 `pageId` 查询参数。来源清理使用单独确认与权限入口，见[来源永久清理](source-purge-cn.md)；它不承诺介质物理擦除。
