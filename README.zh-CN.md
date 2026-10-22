# neux-knowledge

[English](README.md)

neux-knowledge 基于 [Tencent WeKnora](https://github.com/Tencent/WeKnora) 进行精简和微调，直接复用其 Wiki 相关实现，包括页面存储与修订、文档分块、知识提取与归并、引用、链接和索引，并适配为使用 SQLite 存储、由宿主提供模型能力的独立 Go 模块。

**WeKnora 官方网站：** [weknora.weixin.qq.com](https://weknora.weixin.qq.com/)。

## 通过 Go 模块使用

需要 Go 1.25.8 或更高版本：

```sh
go get github.com/JieWaZi/neux-knowledge@latest
```

导入 `github.com/JieWaZi/neux-knowledge/core`，接入方式见 [SDK 与生命周期合同](docs/sdk-contract.md)。宿主提供文档读取器、直接文本模型和可选嵌入能力；原件字节、Wiki 页面、修订、原始文档索引和任务诊断由核心保存。

Wiki 管线同库批次串行，每次模型请求使用独立上下文。核心解析原始回复，处理有界重试与续写，按页发布成功结果，待文档待办收束后维护索引和受影响链接，不创建聊天或通用执行。

## 文档

- [只读核查与 Markdown 导出](docs/audit-export-cn.md)
- [来源永久清理](docs/source-purge-cn.md)和[整库永久删除](docs/library-delete-cn.md)
- [远端服务](docs/remote-server-cn.md)与 [WeKnora HTTP 适配](remote/README.md)
- [内容原则](rules/weknora/principles.md)

CLI 当前操作通过 `go run ./cmd/neux-knowledge discover` 发现。生成必须显式注入宿主模型；独立 HTTP 服务不自行配置模型。

## 开发

```sh
go test ./...
go vet ./...
go test -race ./...
```

自动化覆盖可控模型、真实 SQLite 和本地 HTTP 合同；真实模型质量、服务商兼容性和目标平台需要另行验收。

## 许可与来源

本项目新增部分采用 [MIT 许可](LICENSE)。WeKnora 的版权与许可原文集中保存在 [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md)。
