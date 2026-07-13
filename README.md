# WatchBell

WatchBell 是一个自托管的监控和通知小工具。

我最开始写它是为了少手动刷新两类东西：

- TestFlight 链接是否从“测试员已满”变成可加入
- RSS 里是否出现自己关心的关键词

## 当前功能

- TestFlight 公开邀请链接检查
- GitHub Release 发布检查，支持私有仓库和预发布版本
- RSS / Atom / JSON Feed 拉取和关键词匹配
- 网页文本变化检查，支持简单的 `#id`、`.class` 和标签选择器
- Bark 推送
- SMTP 邮件通知
- 通知模板，支持 `${rss.title}` 这类变量
- SQLite 持久化
- 单用户登录，使用 HttpOnly 签名 cookie
- React + Ant Design 管理界面
- Docker 部署
- 内置插件清单 API，前端从后端读取插件及默认配置

## 适合的部署方式

WatchBell 现在按单机自托管设计。

推荐放在 VPS、NAS、家里小主机或者内网服务器上跑。默认使用 SQLite，不需要 Redis、PostgreSQL 或额外队列服务。512MB 内存机器也能跑，前提是不要把检查频率设得太激进。

## 快速开始

### Docker Compose

复制环境变量示例并修改登录密码和会话密钥：

```bash
cp .env.example .env
```

启动：

```bash
docker compose up --build
```

打开：

```text
http://127.0.0.1:8080
```

默认用户名是：

```text
admin
```

如果要改用户名：

```bash
export WATCHBELL_ADMIN_USERNAME='your-name'
```

### 使用发布镜像部署

不需要在服务器上检出源码。下载 `compose.deploy.yml` 和 `deploy.env.example` 后，复制并修改部署环境变量：

```bash
cp deploy.env.example .env.deploy
chmod 600 .env.deploy
```

至少需要修改 `WATCHBELL_ADMIN_PASSWORD` 和 `WATCHBELL_SESSION_SECRET`。会话密钥可以这样生成：

```bash
openssl rand -hex 32
```

拉取镜像并启动：

```bash
docker compose --env-file .env.deploy -f compose.deploy.yml pull
docker compose --env-file .env.deploy -f compose.deploy.yml up -d
```

查看状态和日志：

```bash
docker compose --env-file .env.deploy -f compose.deploy.yml ps
docker compose --env-file .env.deploy -f compose.deploy.yml logs -f watchbell
```

更新到最新镜像：

```bash
docker compose --env-file .env.deploy -f compose.deploy.yml pull
docker compose --env-file .env.deploy -f compose.deploy.yml up -d
```

默认监听宿主机 `8080` 端口，并使用名为 `watchbell-data` 的 volume 持久化 SQLite 数据库。使用反向代理时，可把 `WATCHBELL_BIND_IP` 改成 `127.0.0.1`。

### 本地开发

后端：

```bash
WATCHBELL_ADMIN_PASSWORD=dev-password \
WATCHBELL_SESSION_SECRET=dev-session-secret-change-me-32-bytes \
WATCHBELL_ADDR=:8080 \
go run ./cmd/watchbell
```

前端：

```bash
cd web
npm install
npm run dev
```

Vite 开发服务器会把 `/api` 代理到 `http://127.0.0.1:8080`。

### 生产构建

```bash
cd web
npm run build
cd ..
go build -buildvcs=false -o watchbell ./cmd/watchbell
```

运行：

```bash
WATCHBELL_ADMIN_PASSWORD='换成你的密码' \
WATCHBELL_SESSION_SECRET='换成至少 32 字节的随机字符串' \
WATCHBELL_WEB_DIR=web/dist \
./watchbell
```

## 登录和安全

认证默认开启。启动时至少要设置下面两项之一：

- `WATCHBELL_ADMIN_PASSWORD`
- `WATCHBELL_ADMIN_PASSWORD_HASH`

如果不想在环境变量里放明文密码，可以先生成 hash：

```bash
go run ./cmd/watchbell hash-password 'your-password'
```

然后用 hash 启动：

```bash
WATCHBELL_ADMIN_PASSWORD_HASH='pbkdf2-sha256$...' \
WATCHBELL_SESSION_SECRET='replace-with-at-least-32-random-bytes' \
go run ./cmd/watchbell
```

`WATCHBELL_SESSION_SECRET` 用来签名登录 cookie。生产环境一定要固定它；如果不设置，程序会临时生成一个，重启后旧登录会全部失效。

只有本地临时调试时才建议关闭认证：

```bash
WATCHBELL_AUTH_DISABLED=true go run ./cmd/watchbell
```

不要把关闭认证的实例暴露到公网。

## 配置项

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `WATCHBELL_ADDR` | `:8080` | HTTP 监听地址 |
| `WATCHBELL_DB` | `data/watchbell.db` | SQLite 数据库路径 |
| `WATCHBELL_WEB_DIR` | `web/dist` | 前端构建目录 |
| `WATCHBELL_WORKERS` | `4` | 并发检查任务数 |
| `WATCHBELL_SCHEDULER_TICK` | `10s` | 调度器扫描间隔 |
| `WATCHBELL_LOG_LEVEL` | `info` | `debug`、`info`、`warn`、`error` |
| `WATCHBELL_AUTH_DISABLED` | `false` | 是否关闭登录认证 |
| `WATCHBELL_ADMIN_USERNAME` | `admin` | 登录用户名 |
| `WATCHBELL_ADMIN_PASSWORD` | 空 | 登录密码，启动时读取 |
| `WATCHBELL_ADMIN_PASSWORD_HASH` | 空 | 用 `hash-password` 生成的密码 hash |
| `WATCHBELL_SESSION_SECRET` | 空 | cookie 签名密钥，建议至少 32 字节 |
| `WATCHBELL_SESSION_TTL` | `168h` | 登录有效期 |
| `WATCHBELL_SESSION_COOKIE` | `watchbell_session` | cookie 名称 |

## 监控器配置示例

### RSS

```json
{
  "url": "https://example.com/feed.xml",
  "timeoutSeconds": 15,
  "notifyExisting": false,
  "includeFullText": false
}
```

说明：

- `notifyExisting=false` 时，第一次拉取只记录已有条目，不通知。
- `includeFullText=true` 时，规则会尽量使用 feed 里的完整正文。

### TestFlight

```json
{
  "url": "https://testflight.apple.com/join/example",
  "timeoutSeconds": 15
}
```

TestFlight 检查目前基于公开页面里的文字判断状态。默认识别常见的“已满”和“可加入”文案；如果 Apple 页面文案变化，后续需要调整匹配规则。

### GitHub Releases

```json
{
  "repository": "owner/repository",
  "token": "",
  "apiUrl": "https://api.github.com",
  "apiVersion": "2026-03-10",
  "timeoutSeconds": 15,
  "maxReleases": 20,
  "includePrereleases": false,
  "notifyExisting": false
}
```

- 公开仓库不需要 `token`；私有仓库建议使用仅有仓库 Contents 读取权限的 fine-grained token。
- 默认首次检查只建立基线，不发送历史版本通知；`notifyExisting=true` 时会通知当前最新版本。
- 检查器使用 ETag，仓库没有变化时不会重复下载 Release 列表。
- 事件类型是 `github.release`。创建一个条件为 `{}` 的规则即可对所有新 Release 发送通知。

### 网页变化

```json
{
  "url": "https://example.com",
  "selector": ".content",
  "timeoutSeconds": 15,
  "ignorePatterns": []
}
```

现在的网页检查是轻量版：抓 HTML，取文本，算 hash。它不执行 JavaScript。需要浏览器渲染的页面，以后可以单独加 Playwright worker，不建议一开始就放进主进程。

## 规则配置

规则使用 JSON。最常用的是 `contains` 和 `regex`。

```json
{
  "match": "any",
  "conditions": [
    {
      "field": "rss.title",
      "operator": "contains",
      "value": "TestFlight"
    },
    {
      "field": "rss.content",
      "operator": "regex",
      "value": "空位|名额|测试"
    }
  ]
}
```

支持的操作符：

- `contains`
- `not_contains`
- `equals`
- `regex`
- `exists`

TestFlight 有空位时本身就会产生事件。如果只想有事件就通知，可以把规则条件写成空对象：

```json
{}
```

## 通知渠道

### Bark

```json
{
  "serverUrl": "https://api.day.app",
  "deviceKey": "YOUR_DEVICE_KEY",
  "group": "WatchBell"
}
```

如果你自己部署了 Bark Server，把 `serverUrl` 换成自己的地址即可。

### 邮件

```json
{
  "host": "smtp.example.com",
  "port": 587,
  "username": "user@example.com",
  "password": "password",
  "from": "user@example.com",
  "to": ["you@example.com"],
  "startTls": true,
  "implicitTls": false
}
```

常见端口：

- `587`：通常配 `startTls=true`
- `465`：通常配 `implicitTls=true`

## 通知模板变量

模板变量使用 `${...}`。

通用变量：

```text
${monitor.name}
${monitor.type}
${rule.name}
${rule.matched}
${event.type}
${event.time}
```

RSS：

```text
${rss.title}
${rss.link}
${rss.author}
${rss.summary}
${rss.content}
${rss.publishedAt}
```

TestFlight：

```text
${testflight.url}
${testflight.status}
${testflight.message}
```

网页：

```text
${webpage.url}
${webpage.oldHash}
${webpage.newHash}
${webpage.summary}
```

GitHub Release：

```text
${github.repository}
${github.release.tagName}
${github.release.name}
${github.release.body}
${github.release.url}
${github.release.prerelease}
${github.release.publishedAt}
${github.release.author}
${github.release.assetCount}
```

## CI/CD 与镜像发布

`.github/workflows/ci.yml` 负责测试、构建和镜像发布：

- Pull Request：运行 Go 测试、`go vet`、前端生产构建和原生 `linux/amd64`、`linux/arm64` Docker 构建，不推送镜像。
- Push 到 `main`：测试通过后使用对应架构的原生 Runner 并行构建 `linux/amd64`、`linux/arm64` 镜像，并自动推送 `main`、`sha-*`、`latest` 标签到 `ghcr.io/<owner>/<repo>`。
- Push `v1.2.3` tag：自动推送 `1.2.3`、`1.2`、`1`、`sha-*` 和 `latest` 标签；预发布 tag 不覆盖 `latest`。
- `workflow_dispatch`：允许在 GitHub Actions 页面手动执行构建和发布。

发布镜像包含 OCI 标签、提交号、构建时间和 GitHub artifact attestation。仓库的 Actions 设置需要允许 `GITHUB_TOKEN` 写入 Packages；首次发布后可在 Packages 页面调整镜像可见性。

拉取已发布镜像后，可以这样覆盖 Compose 的本地构建镜像名：

```bash
WATCHBELL_IMAGE=ghcr.io/<owner>/<repo>:latest docker compose up -d --no-build
```

容器以 UID `10001` 的非 root 用户运行，并使用 `watchbell healthcheck` 执行健康检查。绑定宿主机数据目录时，需要保证该 UID 对目录有写权限。

## 数据目录

默认数据库在：

```text
data/watchbell.db
```

Docker Compose 使用 volume 持久化 `/data/watchbell.db`。升级镜像前，建议先备份这个文件或 volume。

## 当前限制

- 目前是单用户系统。
- 网页检查不执行 JavaScript。
- TestFlight 状态识别依赖页面文案。
- 前端还没有做很细的表单组件，复杂配置现在主要编辑 JSON。
- 还没有内置备份、导入导出和多通知重试策略配置界面。

这些限制不是架构障碍，只是还没做到那一步。

## 许可证

暂未指定许可证。
