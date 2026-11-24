# 贡献指南

感谢参与 BugPk-Api。本文件适用于后端仓库（Go + Hertz + MySQL + Redis）；前端请同时阅读其仓库的 `CONTRIBUTING.md`。

## 开发环境

- Go `1.26+`
- MySQL `8.0+`（字符集 `utf8mb4`）
- Redis `6+`
- 可选：`make`、`pwsh`（重新生成全量 SQL 时需要）

## 本地启动

```bash
cp configs/config.yaml.example configs/config.yaml

mysql -uroot -p -e "CREATE DATABASE bugpk_api DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;"
mysql -uroot -p bugpk_api < sql/full/bugpk_api_full_v1.2.113.sql

export DB_TIMEZONE_DATA_CONFIRMATION='mysql-civil-v1:<按配置参考计算>'
export JWT_SECRET="$(openssl rand -hex 32)"
export PAYMENT_CONFIG_SECRET="$(openssl rand -hex 32)"
export SYSTEM_CONFIG_SECRET="$(openssl rand -hex 32)"
export INITIAL_ADMIN_PASSWORD='<8-64 位强口令>'

go run ./cmd/bugpk-api
```

`DB_TIMEZONE_DATA_CONFIRMATION` 的算法见文档仓 `docs/configuration.md`，不可跳过。

## 数据库变更

- `migrations/*.up.sql` 是唯一权威来源，不要只改 `sql/full`。
- 新增迁移必须连续编号，并同时提供 `.down.sql`。
- 修改迁移后重新生成全量包并跑契约测试：

```bash
pwsh ./scripts/generate-full-sql.ps1 -Variant production
go test ./internal/app -run TestSQLArtifactsMatchCurrentMigrations -count=1
```

- 全量 SQL 的两条硬约束：不含 `bp_user` / `bp_user_role` 种子；不建库、不切库。
- 不要在迁移里写入真实用户、订单、调用日志或任何环境专属数据。

## 代码约定

- 依赖方向保持 `app → domain → infra/pkg`，领域层不感知 HTTP 细节。
- 新增接口需同时考虑权限点、操作日志与错误码，不要只改 handler。
- 用户可见文案由前端负责 i18n；后端返回稳定错误码，不返回面向用户的展示文案。
- 提交前执行：

```bash
gofmt -l cmd internal
go build ./...
go vet ./...
go test -count=1 ./...
```

## 提交与 PR

- 提交信息使用 Conventional Commits，例如：
  - `feat(payment): 新增渠道回调验签`
  - `fix(gateway): 修正超时后的计费状态`
  - `docs: 补充时区确认说明`
- 一个 PR 聚焦一件事；涉及数据库、配置或接口契约变更时，在描述里写清迁移步骤与回滚方式。
- PR 需说明验证方式（命令与结果），并确认未提交密钥、真实配置、日志或备份文件。
- 安全问题不要开公开 Issue，按 `SECURITY.md` 私下报告。

## 许可证

提交即表示你同意以 [AGPL-3.0](./LICENSE) 授权你的贡献。
