# neux-knowledge

[简体中文](README.zh-CN.md)

neux-knowledge is a slimmed-down and lightly adapted version of [Tencent WeKnora](https://github.com/Tencent/WeKnora). It directly reuses WeKnora's Wiki implementations, including page storage and revisions, document chunking, knowledge extraction and consolidation, citations, links, and indexes. The changes make these capabilities available as a standalone Go module with SQLite storage and host-provided model access.

**WeKnora official website:** [weknora.weixin.qq.com](https://weknora.weixin.qq.com/).

## Use as a Go module

Requires Go 1.25.8 or later:

```sh
go get github.com/JieWaZi/neux-knowledge@latest
```

Import `github.com/JieWaZi/neux-knowledge/core` and follow the [SDK and lifecycle contract](docs/sdk-contract.md). The host supplies document readers, a direct text model, and optional embeddings. Source bytes, Wiki pages, revisions, document indexes, and job diagnostics are stored by the core.

The Wiki pipeline processes one batch per library at a time. Each model request has its own context. The core parses raw replies, handles bounded retries and continuations, publishes successful pages independently, and updates the index and affected links after pending document work settles. It does not create chats or general executions.

## Documentation

- [Audit and Markdown export](docs/audit-export-cn.md)
- [Permanent source cleanup](docs/source-purge-cn.md) and [library deletion](docs/library-delete-cn.md)
- [Remote service](docs/remote-server-cn.md) and [WeKnora HTTP adapter](remote/README.md)
- [Content principles](rules/weknora/principles.md)

Discover CLI operations with `go run ./cmd/neux-knowledge discover`. Model generation requires an explicitly injected host; the standalone HTTP server does not configure a model by itself.

## Development

```sh
go test ./...
go vet ./...
go test -race ./...
```

Automated checks cover controlled models, real SQLite, and local HTTP contracts. Live model quality, provider compatibility, and target platforms require separate acceptance.

## License and attribution

Project additions use the [MIT License](LICENSE). WeKnora and dependency copyrights and license texts are preserved in [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).
