# 薄知识服务宿主

`cmd/neux-knowledge-server` 只装配独立 `core.Engine`、知识 Handler 和独立认证。它可存取 raw、Wiki、主题、历史与回执；没有模型执行端口，整理、取消、维护准确返回 `unsupported`。需要服务器实际生成时，在 NeuxAlly Web 后端装配同一 Handler 与已有 Runtime 业务端口，见 `remote-http-cn.md`。

启动必须显式提供配置文件：

```sh
go run ./cmd/neux-knowledge-server -config ./server.json
```

以下只展示配置结构，库身份与凭据均为虚构占位值：

```json
{
  "dbPath": "./data/knowledge.db",
  "rawDir": "./data/raw",
  "listen": "127.0.0.1:8787",
  "security": {
    "tokens": {
      "replace-with-dedicated-secret": {
        "subject": "reader-demo",
        "libraries": {
          "library-demo": {
            "operations": ["read", "write"],
            "scope": ["project-demo"],
            "policyVersion": 1
          }
        }
      }
    },
    "writeIPPolicy": {
      "enabled": true,
      "allowed": ["192.0.2.0/24"],
      "trustedProxies": []
    }
  }
}
```

`dbPath/rawDir` 所在父目录必须存在，库身份须是核心真实创建的库。配置可单独授予 `createLibrary: true` 用于新建库，但新库之后仍需显式加入授权映射；不因创建而获得隐式全部库权限。JSON 未知字段会被拒绝，缺少存储路径或凭据时拒绝启动。

监听默认回环地址。对外部署可配置 `tlsCert/tlsKey` 同时启用 TLS，或由可信部署网关终止 TLS。网关配置必须符合知识 Handler 的真实来源链约定，不能把反向代理自身的 IP 当作所有客户端身份。薄宿主启动时读取一次配置；修改配置后重新启动。完整业务宿主可在自己的配置生命周期调用共享 `PolicyStore.Replace` 立即撤销在途发布权限。

程序退出时等待 HTTP 优雅停止并关闭核心。日志只报告实际监听与未装配执行端口，不打印令牌。它不连接或替代任何本机模型，也不代表服务器模型、官方 WeKnora 或 NeuxAlly Web 已经部署验收。
