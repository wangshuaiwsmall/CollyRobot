# vibe coding练手项目

# CollyRobot

CollyRobot 是一个前后端分离的爬虫管理系统骨架：

- 前端：Vue 3 + TypeScript + Vite
- 后端：Go + Gin
- 爬虫：Colly
- 数据库：SQLite（通过 repository 接口隔离，便于后续迁移 MongoDB）

## 目录结构

```text
.
├── backend/                 # Gin API 服务
│   ├── cmd/server/          # 程序入口
│   └── internal/            # 配置、HTTP、存储和爬虫模块
└── frontend/                # Vue 3 管理界面
```

## 本地启动

后端：

```bash
cd backend
go run ./cmd/server
```

默认启动会创建处于待命状态的调度器和 Worker，但不会自动索引、加载或抓取任何主题。索引与抓取均需要通过管理 UI 或 API 显式触发：

```bash
# 兼容参数：调度器现已默认待命，使用它不会额外入队或抓取
go run ./cmd/server -start

# 异步触发一次索引构建；发现的主题仍保持 waiting，需再显式下达抓取指令
go run ./cmd/server -index -workers 4 -sync-concurrency 8

# 使用本地模拟索引和 Worker 调试管理界面（不会访问真实论坛）
go run ./cmd/server -demo
```

演示模式也可通过 `DEMO_MODE=true` 启用。触发“开始索引”后，模拟索引器会分三批产出九个 `waiting` 主题；再在“抓取任务”页点击“开始抓取等待项”，模拟 Worker 才会开始处理，并在状态面板及 `indexer` / `crawler` 日志中展示进度。

可选参数：`-port`、`-database`、`-log-dir`、`-workers`、`-sync-concurrency`、`-demo`。命令行参数优先于环境变量。

前端（另开一个终端）：

```bash
cd frontend
pnpm install
pnpm dev
```

Windows 开发环境也可以直接双击 `frontend/start-dev.bat`。脚本会在首次运行时自动安装依赖，然后启动 Vite 开发服务器；生产部署请使用 `webhost` 构建产物。

打开 `http://localhost:5173`。Vite 会将 `/api` 请求代理到 `http://localhost:8080`。

## 无 Node.js 部署

根目录的 `webhost` 是独立的 Go 静态站点托管程序。构建环境执行以下脚本后，Vue `dist` 会被嵌入到 `webhost/bin/collyrobot-webhost.exe`；部署目标无需安装 Node.js 或 pnpm：

```bat
webhost\scripts\build-webhost.cmd
```

运行时先启动后端 API（默认 `:8080`），再启动 Web Host（默认 `:8081`）。Web Host 会将浏览器的 `/api/*` 请求反向代理到后端，并为 Vue History 路由返回 `index.html`。

```bat
webhost\bin\collyrobot-webhost.exe

# 可覆盖监听端口和 API 地址
webhost\bin\collyrobot-webhost.exe -addr :80 -backend http://127.0.0.1:8080
```

## 接口

- `GET /api/hello`：前后端联通演示
- `GET /api/health`：服务健康检查
- `GET /api/scheduler`：查询调度器、索引和 Worker 状态
- `POST /api/scheduler/start`：停止后恢复调度器待命状态
- `POST /api/scheduler/stop`：停止调度器，并取消 API 触发的索引任务
- `POST /api/scheduler/index`：异步触发索引工作流，返回 `202 Accepted`
- `POST /api/scheduler/index/cancel`：中断当前索引任务，不停止 Worker 或移除已发现主题
- `POST /api/scheduler/queue/waiting`：将所有 `waiting` 主题显式加入内存抓取队列
- `POST /api/scheduler/retry/failed`：将所有 `failed` 主题恢复为 `waiting` 并加入内存队列
- `GET /api/topics`：返回全部已索引主题，并按 `waiting`、`done`、`failed` 分组
- `PUT /api/scheduler/limits`：动态调整 Worker 上限和主题内并发上限

调整调度上限示例：

```json
{
  "workers": 4,
  "sync_concurrency": 8
}
```

也可以在启动前设置 `WORKER_LIMIT` 和 `SYNC_CONCURRENCY` 环境变量。

## 日志

服务默认将日志写入 `backend/logs`，也可以通过 `LOG_DIRECTORY` 环境变量修改目录：

- `backend-YYYY-MM-DD.log`：后端业务、调度器、Gin 访问及异常日志
- `frontend-YYYY-MM-DD.log`：浏览器通过 `/api/logs/frontend` 上报的前端日志
- `indexer-YYYY-MM-DD.log`：论坛列表页、分页和主题索引构建状态
- `crawler-YYYY-MM-DD.log`：主题领取、正文分页抓取、断点续爬和完成/失败状态

每个日志流均使用独立文件，并在服务器本地日期变化后的第一次写入时自动切换到新文件。前端日志上报失败不会阻断页面的正常业务请求。管理 UI 的“运行日志”页面每两秒读取索引或抓取日志的尾部；也可以调用 `GET /api/logs/indexer/tail?lines=160` 或 `GET /api/logs/crawler/tail?lines=160` 获取最近日志行。
后端每条日志均以 `source=文件名:行号` 结尾，例如：

```text
2026/07/22 19:00:00.123456 level=INFO event=service_start port=8080 source=main.go:24
```

## 后端模块

```text
internal/
├── domain/       # 主题等核心模型
├── repository/   # 存储抽象，隔离 SQLite/MongoDB
├── store/        # SQLite 实现与数据表初始化
├── indexer/      # 论坛主题索引工作流
├── taskqueue/    # 单进程内存任务队列
├── worker/       # 单主题抓取实例及 reset 生命周期
├── scheduler/    # 全局调度、动态扩缩容和任务分配
└── httpserver/   # 管理 API
```

`indexer/forum_stub.go` 和 `worker/fetcher.go` 中保留了论坛相关伪代码扩展点。下一步只需针对目标论坛实现主题列表解析、“只看作者”URL 规则、分页与正文持久化。

索引器使用一个同步 Colly Collector，通过当前请求的 `Request.Visit` 递归访问“下一页”。每解析完一个主题列表页，就将该页主题作为一个批次提交到仓库；新增主题持久化为 `waiting`，由用户在“抓取任务”页显式下达入队指令。这样服务重启、重新索引或临时排错都不会意外触发网络抓取。

具体论坛的索引起始 URL、列表页主题解析和下一页选择器位于 `indexer/forum_rules_stub.go` 的 `ForumIndexRulesStub` 中。索引 Collector 限制在起始 URL 同一域名内访问，并使用 Colly 已访问 URL 记录避免分页链接循环。

数据库只保存业务状态 `waiting`、`done` 与 `failed`，以及断点续爬的分页缓存；`queued`、`running` 等实时调度状态仅存在于进程内存。调度器服务启动后默认待命，不会自动加载 `waiting` 主题；只有收到显式抓取或重试指令时才会查询并放入内存队列。运行期间的领取、排队和去重均在内存中完成。这降低了 SQLite 写锁竞争，也避免依赖 `UPDATE ... RETURNING` 等特定数据库语法。当前内存队列面向单进程运行；未来横向扩容时可将该队列替换为 Redis Stream、NATS 或其他消息队列。

主题正文抓取使用 Colly 编排：先同步访问“只看作者”首页以确认总页数，再创建异步 Collector，并将 `sync_concurrency` 映射为 Colly `LimitRule.Parallelism`。分页响应携带页码上下文，全部完成后按页码排序并调用持久化规则。具体论坛的 URL 拼接、HTML 选择器和小说存储规则位于 `worker/forum_rules_stub.go`，尚待按目标论坛实现。

为支持断点续爬，SQLite 额外维护 `topic_page_content` 表。每个分页成功解析后立即将内容序列化写入该表；下次执行同一主题时，抓取器仍会访问首页确认最新总页数，但会跳过已经缓存的后续页面，仅请求缺失页。所有页面齐全后才调用正式小说持久化规则，因此任一页面失败时，已完成页面仍能保留供后续恢复使用。
