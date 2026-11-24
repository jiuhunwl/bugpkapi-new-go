# BugPk-Api 后端

[![CI](https://github.com/jiuhunwl/bugpkapi-new-go/actions/workflows/ci.yml/badge.svg)](https://github.com/jiuhunwl/bugpkapi-new-go/actions/workflows/ci.yml)

[简体中文](README.md) · [English](README_EN.md)

开源的**开放 API 服务平台**后端，基于 Go + [Hertz](https://github.com/cloudwego/hertz)，采用 DDD 分层架构。提供管理后台接口、开放 API 网关、接口与分组、计费与套餐、订单与支付、风控与审计、工单与内容运营等能力。

| 仓库 | 说明 |
| --- | --- |
| **bugpkapi-new-go**（本仓库） | 后端服务（Go + MySQL + Redis），AGPL-3.0 |
| [bugpkapi-new-vue](https://github.com/jiuhunwl/bugpkapi-new-vue) | 前端（Vue 3 + Vben Admin），MIT |
| [bugpkapi-new-docs](https://github.com/jiuhunwl/bugpkapi-new-docs) | 文档、架构说明与部署指南 |

## 功能概览

| 模块 | 能力 |
| --- | --- |
| 开放 API 网关 | `/api/{apiCode}` 统一入口，按接口配置转发上游，参数校验、缓存与限流 |
| 接口与计费 | 接口管理、QPS 限制、套餐（全站 / 接口 / 点数）、按次计费、余额与流水 |
| 支付 | 支付宝、微信支付及可扩展渠道；回调幂等、订单对账、通知重放防护 |
| 用户与权限 | RBAC 角色 / 菜单 / 按钮权限、注册与找回密码、多场景验证码 |
| 风控 | 调用日志指纹、风控快照、账号封禁、IP 地域解析 |
| 内容运营 | 站点设置、SEO、公告、广告位、友链、捐赠与致谢名单、协议模板 |
| 数据分析 | 调用日志增量汇总（分钟 / 小时 / 天），看板与原始日志解耦 |
| 工单 | 用户工单与消息、管理端处理流、附件策略 |
| 可观测 | 统一请求 ID、操作日志、`/healthz` 与 `/readyz` |

## 目录结构

```text
.
├── cmd/            可执行入口（主服务与运维小工具）
├── internal/       DDD 分层业务代码（app / domain / infra / pkg）
├── migrations/     数据库迁移（权威来源）
├── sql/full/       由迁移生成的全量初始化 SQL
├── configs/        配置示例
├── deploy/         通用部署模板（systemd / Nginx / 环境变量示例）
└── scripts/        工程脚本（全量 SQL 生成等）
```

## 运行要求

- Go `1.26+`
- MySQL `8.0+`
- Redis `6+`（缓存、限流与验证码）
- Linux / macOS / Windows 均可本地运行

## 快速开始

```bash
# 1. 准备配置
cp configs/config.yaml.example configs/config.yaml

# 2. 创建空数据库（不要复用已有数据的库）
mysql -uroot -p -e "CREATE DATABASE bugpk_api DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;"

# 3. 导入初始化 SQL（文件会对非空库直接报错中止）
mysql -uroot -p bugpk_api < sql/full/bugpk_api_full_v1.2.113.sql

# 4. 设置首个管理员口令，并启动
export INITIAL_ADMIN_PASSWORD='<8-64 位强口令>'
go run ./cmd/bugpk-api
```

首次启动时若库中没有管理员，会用 `INITIAL_ADMIN_PASSWORD` 创建 `admin` 账号；创建成功后可移除该环境变量。生产环境请通过环境变量注入数据库口令、JWT 签名密钥与支付配置密钥，不要写进 `config.yaml` 或提交到仓库。

更多变量（`APP_ENV`、`DB_*`、`JWT_SECRET`、`PAYMENT_CONFIG_SECRET`、`FRONTEND_SERVE_ENABLED` 等）与部署方式见 [deploy](./deploy) 与文档仓。

## 构建

```bash
make build              # 当前平台
make build-linux-amd64  # Linux amd64
make build-linux-arm64  # Linux arm64
```

## 数据库与迁移

`migrations/*.up.sql` 是唯一权威来源，`sql/full` 由脚本机械拼接生成，二者由契约测试保证一致：

```bash
pwsh ./scripts/generate-full-sql.ps1 -Variant production
go test ./internal/app -run TestSQLArtifactsMatchCurrentMigrations -count=1
```

- 全量 SQL **不含任何用户种子**，管理员由 `INITIAL_ADMIN_PASSWORD` 创建。
- 全量 SQL **不建库、不切库**，并要求目标库为空，避免误覆盖数据。
- 手工导入全量 SQL 后，应将 `MIGRATION_ENABLED` 设为 `false`，防止重复迁移。

## 测试

```bash
make test       # go test -count=1 ./...
make vet
make release-check   # build + vet + test
```

部分集成测试需要可用的 MySQL / Redis，会通过环境变量自动跳过未配置的场景。

## 前端联调

前端默认把 `/system-api` 与 `/api` 代理到 `http://127.0.0.1:8080`。也可由本服务直接托管前端产物：

```yaml
frontend:
  enabled: true
  distPath: frontend/dist
```

## 贡献

开发环境、数据库变更规则与提交约定见 [CONTRIBUTING.md](./CONTRIBUTING.md)。安全问题请按 [SECURITY.md](./SECURITY.md) 私下报告。

## 许可证

本项目采用 [GNU Affero General Public License v3.0](./LICENSE)（AGPL-3.0）。

AGPL 第 13 条要求：如果你修改本程序并通过网络向用户提供服务，必须向这些用户提供对应源码的获取方式。配套前端以 MIT 协议发布，详见前端仓库 LICENSE。