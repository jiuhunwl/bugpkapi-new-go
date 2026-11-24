# SQL 目录说明 / SQL Directory Guide

> 本目录是 BugPk-Api 数据库脚本的**唯一分发入口**。
> This directory is the **single distribution entry** for BugPk-Api database scripts.

---

## 中文

### 目录结构

```
sql/
├── full/      全新环境安装用的全量 SQL 包（当前版本）
├── upgrade/   每次发版若有数据库变动，新增一个升级 SQL
└── README.md  本文件
```

数据库结构的**权威来源是 `../migrations/`**（golang-migrate 格式，共 128 个版本，每个版本一对 `*.up.sql` / `*.down.sql`）。
`sql/full/` 与 `sql/upgrade/` 下的文件都是从 `migrations/` 机械生成的分发产物，**不要手工编辑**。

### 文件命名规范

| 类型 | 命名 | 示例 |
| --- | --- | --- |
| 全量安装包 | `full/bugpk_api_full_v<版本>.sql` | `full/bugpk_api_full_v1.2.25.sql` |
| 增量升级包 | `upgrade/bugpk_api_upgrade_v<版本>.sql` | `upgrade/bugpk_api_upgrade_v1.2.25.sql` |

- `<版本>` 与仓库根 `VERSION` 保持一致。
- **每次发版若涉及数据库变动，就在 `upgrade/` 新增一个对应该版本的升级 SQL**；`full/` 里的全量包同步覆盖为最新版本。

### 全量包 `full/bugpk_api_full_v<版本>.sql`

用于**全新数据库**初始化，已内置两条安全不变量：

1. **不含任何用户种子数据**。`bp_user` / `bp_user_role` 为空，第一个管理员由程序按 `INITIAL_ADMIN_PASSWORD` 引导创建（见后端 README「默认管理员账号与密码」）。
2. **不建库、不切库**。文件里没有 `CREATE DATABASE` / `USE`，必须在导入前自行选中一个**空库**。

导入示例：

```bash
# 1) 建库（由你决定库名，下文以 bugpk_api 为例）
mysql -uroot -p -e "CREATE DATABASE IF NOT EXISTS \`bugpk_api\` DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;"

# 2) 选中该空库后导入（必须新开一个 autocommit 会话，禁止加 --force）
mysql -uroot -p --database=bugpk_api < sql/full/bugpk_api_full_v1.2.25.sql
```

导入成功后 `schema_migrations` 会被标记为最新版本（当前 `120:0`），随后请把后端 `.env` 里的 `MIGRATION_ENABLED` 设为 `false`，避免程序再跑一次迁移。

> 若目标库**已有表**，脚本会主动报错中止，不会破坏已有数据。此时请改用「升级」流程。

### 升级包 `upgrade/bugpk_api_upgrade_v<版本>.sql`

用于**已有部署**的数据库升级，同样是从 `migrations/` 生成的正式前向迁移：

- 支持干净基线 `1..98`，已应用过的迁移会自动跳过（幂等）。
- 遇到 `dirty=1`、未知版本或高于当前包版本的基线会**拒绝执行**，不会猜测。
- DDL 无法事务回滚；**执行前务必停止所有应用实例并备份数据库**。

```bash
mysql -uroot -p --database=bugpk_api < sql/upgrade/bugpk_api_upgrade_v1.2.25.sql
```

### 也可以完全不用手写 SQL

后端起服务时内置 golang-migrate，默认 `MIGRATION_ENABLED=true` 会自动执行 `migrations/`：

- **全新部署**：建空库 → 启动后端 → 自动建表。
- **版本升级**：直接替换二进制并重启 → 自动补齐缺失迁移（发布包里的升级脚本 `deploy/upgrade.sh` 就是这么做的）。

`sql/` 下的文件适合「不允许程序自动改结构」或「需要 DBA 人工审计后再执行」的场景。

### 重新生成

```powershell
# 全量包（默认 production 变体，输出到 sql/full/bugpk_api_full_v<VERSION>.sql）
pwsh scripts/generate-full-sql.ps1

# 校验仓库内已提交的全量包是否与 migrations 同步
pwsh scripts/generate-full-sql.ps1 -Check

# 升级包
node scripts/generate-universal-upgrade.mjs <版本>
# 例：node scripts/generate-universal-upgrade.mjs 1.2.25
```

---

## English

### Layout

```
sql/
├── full/      Full SQL package for brand-new installations (current release)
├── upgrade/   One upgrade SQL per release whenever the schema changes
└── README.md  This file
```

The **authoritative source of the schema is `../migrations/`** (golang-migrate, 128 versions, each with a `*.up.sql` / `*.down.sql` pair).
Everything under `sql/full/` and `sql/upgrade/` is generated from it — **never edit these files by hand**.

### Naming convention

| Kind | Name | Example |
| --- | --- | --- |
| Full install package | `full/bugpk_api_full_v<version>.sql` | `full/bugpk_api_full_v1.2.25.sql` |
| Incremental upgrade | `upgrade/bugpk_api_upgrade_v<version>.sql` | `upgrade/bugpk_api_upgrade_v1.2.25.sql` |

`<version>` always matches the repository root `VERSION`. **Whenever a release changes the schema, add the matching upgrade SQL to `upgrade/`** and refresh the full package in `full/`.

### Full package `full/bugpk_api_full_v<version>.sql`

For initialising a **brand-new database**. Two safety invariants are built in:

1. **No user seed data.** `bp_user` / `bp_user_role` stay empty; the first administrator is bootstrapped by the binary from `INITIAL_ADMIN_PASSWORD` (see the backend README, "Default administrator").
2. **No database creation or switching.** There is no `CREATE DATABASE` / `USE` in the file — you must select an **empty** database yourself before importing.

```bash
# 1) Create the database (pick your own name; bugpk_api is used below)
mysql -uroot -p -e "CREATE DATABASE IF NOT EXISTS \`bugpk_api\` DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;"

# 2) Import it in a NEW autocommit session, never with --force
mysql -uroot -p --database=bugpk_api < sql/full/bugpk_api_full_v1.2.25.sql
```

On success `schema_migrations` is marked with the latest version (`98:0`). Set `MIGRATION_ENABLED=false` in the backend `.env` afterwards so the binary does not migrate again.

> If the target database already contains tables the script aborts on purpose instead of damaging data. Use the upgrade path in that case.

### Upgrade package `upgrade/bugpk_api_upgrade_v<version>.sql`

For upgrading an **existing deployment**; also generated from `migrations/`:

- Accepts clean baselines `1..98`; already-applied migrations are skipped (idempotent).
- Refuses to run against `dirty=1`, unknown, or newer-than-package baselines instead of guessing.
- DDL is not transactionally reversible — **stop every application instance and back up the database first**.

```bash
mysql -uroot -p --database=bugpk_api < sql/upgrade/bugpk_api_upgrade_v1.2.25.sql
```

### You can skip raw SQL entirely

The backend embeds golang-migrate and runs `migrations/` automatically while `MIGRATION_ENABLED=true` (the default):

- **Fresh install**: create an empty database, start the backend, tables are created for you.
- **Upgrade**: swap the binary and restart — missing migrations are applied automatically (that is exactly what the packaged `deploy/upgrade.sh` does).

The files under `sql/` are for deployments that forbid automatic schema changes, or that require a DBA to audit every statement first.

### Regenerating

```powershell
# Full package (production variant by default -> sql/full/bugpk_api_full_v<VERSION>.sql)
pwsh scripts/generate-full-sql.ps1

# Verify the committed full package is in sync with migrations
pwsh scripts/generate-full-sql.ps1 -Check

# Upgrade package
node scripts/generate-universal-upgrade.mjs <version>
# e.g. node scripts/generate-universal-upgrade.mjs 1.2.25
```


