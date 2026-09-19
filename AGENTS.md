# AGENTS.md

本文件定义 AI 编码代理在本仓库中的默认协作规则。用户的明确要求以及更深层目录中的 `AGENTS.md` 优先于本文件。

## 项目概览

Dujiao-Next 是一个模块化单体数字商品商城：

- 后端：Go、Gin、GORM，入口位于 `cmd/server/`。
- 用户端：Vue 3 + Vite + TypeScript，位于 `frontend/user/`。
- 管理端：Vue 3 + Vite + TypeScript，位于 `frontend/admin/`。
- 默认数据库：SQLite；也支持 PostgreSQL。
- Redis 与异步队列属于可选运行依赖。

## 分支与远端

- `origin` 是 Gloamere 定制仓库，只向该远端推送。
- `upstream` 是官方 Dujiao-Next 仓库，只用于获取上游更新，禁止向其推送。
- `main` 是 Gloamere 稳定定制分支，也是仓库默认分支。
- `develop` 是日常开发和上游更新集成分支。
- 官方代码通过 `upstream/main` 获取，不另设长期镜像分支。
- 功能与修复从 `develop` 创建 `feat/<name>`、`fix/<name>` 短期分支，通过 PR 合入 `develop`；上游更新使用 `sync/upstream-<version-or-date>` 短期分支。
- `main` 和 `develop` 均要求通过 PR 合入且 CI 检查通过，禁止强推与删除；单人开发不强制他人审批。
- CI 必须覆盖 `main`、`develop` 的 push 和 PR，必需检查为 `Verify installer`、`Verify API`、`Verify release config`、`Verify fullstack build`。
- 上游同步 PR 和 `develop` 到 `main` 的发布 PR 使用 merge commit，保留共同历史，不使用 squash 或 rebase 合并。稳定分支上的紧急修复应及时合回 `develop`。
- 未经明确要求，不提交、推送、改写历史或向稳定分支合并。

## 修改原则

- 修改前先查看 `git status --short`，保留用户已有的未提交改动。
- 采用最小且完整的改动，沿用现有模块边界、命名、错误处理和依赖方案。
- 不进行无关重构、整仓格式化或非必要依赖升级。
- 用户可见文本必须使用现有 i18n 机制，不在前端或 API 中硬编码展示文案。
- 新增后台路由时同步维护 `internal/authz/bootstrap.go` 中的内置角色权限，并运行 RBAC 覆盖测试。
- 不提交 `config.yml`、数据库、日志、上传文件、令牌、密码或其他本地运行数据。
- `.runtime/` 是本地运行目录，不属于源码，不得提交。

## 架构边界

业务模块位于 `internal/modules/<name>/`，保持以下依赖方向：

- `domain` 不依赖 `application`、`infrastructure` 或 `transport`。
- `application` 不依赖 Gin、asynq 等传输和基础设施实现。
- `transport` 依赖应用层契约，不直接依赖具体存储实现。
- GORM 访问集中在模块的 `infrastructure/gormstore` 适配器中。
- 跨模块调用通过 `contract/`，装配代码放在 `internal/bootstrap/`。
- `internal/shared` 不依赖业务模块；`internal/platform` 不依赖业务模块。

SQLite 默认只有一个连接。事务闭包内的查询必须使用事务句柄或经 `WithTx(tx)` 绑定的 store，禁止回退到全局数据库连接；外部 HTTP 调用应放在事务之外。

## 常用命令

后端：

```bash
go run ./cmd/server
go test ./...
go test ./internal/architecture/...
```

用户端：

```bash
cd frontend/user
corepack pnpm install
corepack pnpm run dev
corepack pnpm run build
```

管理端：

```bash
cd frontend/admin
corepack pnpm install
corepack pnpm run dev
corepack pnpm run build
```

使用 pnpm 时应进入对应前端目录，避免使用 `pnpm --dir` 绕过该目录的 `packageManager` 版本声明。

## 验证要求

- Go 改动至少运行直接相关包的测试；跨模块或架构改动运行 `go test ./...`。
- 前端改动至少运行对应应用的 `pnpm run build`，该命令包含 TypeScript 检查。
- 涉及完整发行构建时，管理端使用 `pnpm run build:fullstack`，然后验证带 `release,fullstack` 标签的 Go 构建。
- 修改后检查实际 diff；提交前运行 `git diff --check`。
- 无法执行的检查必须在交付说明中注明原因和残余风险。

## 上游同步

在工作区干净时，从最新开发分支创建一次性同步分支（替换示例中的日期或版本，每次使用新名称）：

```bash
git switch develop
git pull --ff-only origin develop
git fetch upstream
git switch -c sync/upstream-20260920-01
git merge upstream/main
```

解决冲突并完成验证，获得提交与推送授权后，将同步分支推送到 `origin`，创建目标为 `develop` 的 PR。CI 通过后使用 merge commit 合入；发布时再创建 `develop` 到 `main` 的 PR，同样在 CI 通过后使用 merge commit 合入。

代码合并不等于部署。当前本地 `.runtime/` 运行的是下载的发行二进制，源码切换或合并后不会自动更新运行中的程序；验证源码变更需要重新构建并启动对应版本。
