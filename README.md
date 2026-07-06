# WatchBell

WatchBell 是一个自托管的监控和通知小工具。

我最开始写它是为了少手动刷新两类东西：

- TestFlight 链接是否从“测试员已满”变成可加入
- RSS 里是否出现自己关心的关键词

项目现在已经把底座搭起来了：监控器、规则、通知渠道、模板、日志和一个 Ant Design 管理界面。后续要加网页变化监控、GitHub Release、价格变动之类的来源，不需要推翻结构。

## 当前功能

- TestFlight 公开邀请链接检查
- RSS / Atom / JSON Feed 拉取和关键词匹配
- 网页文本变化检查，支持简单的 `#id`、`.class` 和标签选择器
- Bark 推送
- SMTP 邮件通知
- 通知模板，支持 `${rss.title}` 这类变量
- SQLite 持久化
- 单用户登录，使用 HttpOnly 签名 cookie
- React + Ant Design 管理界面
- Docker 部署

## 适合的部署方式

WatchBell 现在按单机自托管设计。

推荐放在 VPS、NAS、家里小主机或者内网服务器上跑。默认使用 SQLite，不需要 Redis、PostgreSQL 或额外队列服务。512MB 内存机器也能跑，前提是不要把检查频率设得太激进。

## 快速开始

### Docker Compose

先准备两个环境变量：

```bash
export WATCHBELL_ADMIN_PASSWORD='换成你的登录密码'
export WATCHBELL_SESSION_SECRET='换成至少 32 字节的随机字符串'
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
