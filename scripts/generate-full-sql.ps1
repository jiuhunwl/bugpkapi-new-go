param(
  # 产物变体：
  #   production  —— 生产全量安装包（不含用户种子、不建库不切库，导入前自选空库）
  #   development —— 本地开发全量（含用户种子与 CREATE DATABASE/USE，供 GoLand/手工测试）
  [ValidateSet('production', 'development')]
  [string]$Variant = 'production',

  # 输出路径（相对后端仓库根）。留空则按变体与 VERSION 自动推导：
  #   production  -> sql/full/bugpk_api_full_v<版本>.sql
  [string]$Output = '',

  [switch]$Check
)

$ErrorActionPreference = 'Stop'

# 布局自适应：既支持独立后端仓库（<根>/migrations、<根>/VERSION，即 scripts/ 与 migrations/ 同级），
# 也支持 frontend/ 与 backend/ 并列的合并工作区（<根>/backend/migrations）。
# 两种情况 $backendRoot 都指向后端仓库根。
$scriptParent = Split-Path -Parent $PSScriptRoot
$backendRoot = if (Test-Path -LiteralPath (Join-Path $scriptParent 'migrations')) {
  $scriptParent
} else {
  Join-Path $scriptParent 'backend'
}

$migrationDir = Join-Path $backendRoot 'migrations'

if (-not (Test-Path -LiteralPath $migrationDir)) {
  throw "Migration directory not found: $migrationDir"
}

$versionFile = Join-Path $backendRoot 'VERSION'
if (-not (Test-Path -LiteralPath $versionFile)) {
  throw "VERSION not found: $versionFile"
}
$releaseVersion = (Get-Content -LiteralPath $versionFile -Raw).Trim()
if ($releaseVersion -notmatch '^[0-9]+\.[0-9]+\.[0-9]+$') {
  throw "VERSION must be semantic (x.y.z): $releaseVersion"
}

if (-not $Output) {
  $Output = "sql\full\bugpk_api_full_v$releaseVersion.sql"
}
$outputPath = Join-Path $backendRoot $Output

$migrations = Get-ChildItem -LiteralPath $migrationDir -Filter '*.up.sql' |
  ForEach-Object {
    if ($_.Name -notmatch '^(\d+)_') {
      throw "Invalid migration file name: $($_.Name)"
    }
    [pscustomobject]@{
      File    = $_
      Version = [int]$Matches[1]
    }
  } |
  Sort-Object Version

if (-not $migrations) {
  throw "No up migrations found in $migrationDir"
}

for ($index = 0; $index -lt $migrations.Count; $index++) {
  $expectedVersion = $index + 1
  if ($migrations[$index].Version -ne $expectedVersion) {
    throw "Migration sequence gap: expected $expectedVersion, found $($migrations[$index].Version)"
  }
}

$latestVersion = $migrations[-1].Version
$latestMigration = $latestVersion.ToString('000000')
$builder = [System.Text.StringBuilder]::new()

if ($Variant -eq 'production') {
  [void]$builder.AppendLine('-- BugPk-Api production initialization SQL')
} else {
  [void]$builder.AppendLine('-- BugPk-Api full database initialization SQL')
}
[void]$builder.AppendLine("-- Generated from migrations 000001 through $latestMigration.")
if ($Variant -eq 'production') {
  [void]$builder.AppendLine('-- User and user-role seeds are intentionally omitted; INITIAL_ADMIN_PASSWORD bootstraps the first administrator.')
  [void]$builder.AppendLine('-- Select an EMPTY database with mysql --database=<name> before importing. No database is selected by this file.')
} else {
  [void]$builder.AppendLine('-- Use on an empty MySQL 8 database for local GoLand/manual testing.')
  [void]$builder.AppendLine('-- Default database name matches configs/config.yaml.example: bugpk_api.')
}
[void]$builder.AppendLine()
[void]$builder.AppendLine('SET NAMES utf8mb4;')
[void]$builder.AppendLine()

if ($Variant -ne 'production') {
  [void]$builder.AppendLine('CREATE DATABASE IF NOT EXISTS `bugpk_api` DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;')
  [void]$builder.AppendLine('USE `bugpk_api`;')
}

[void]$builder.AppendLine('-- Import in a NEW autocommit mysql CLI session, without --force; never apply initialization to an existing database.')
[void]$builder.AppendLine('SET @__bugpk_init_lock = CAST(MOD(CRC32(CONCAT(DATABASE(), '':schema_migrations'')) * 1486364155, 4294967296) AS CHAR);')
[void]$builder.AppendLine('SET @__bugpk_init_acquired = GET_LOCK(@__bugpk_init_lock, 0);')
[void]$builder.AppendLine('SET @__bugpk_init_guard = IF(@__bugpk_init_acquired = 1 AND DATABASE() IS NOT NULL AND @@SESSION.autocommit = 1 AND NOT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema=DATABASE()), ''DO 0'', ''SELECT * FROM __BUGPK_INIT_REQUIRES_EMPTY_SELECTED_DATABASE__'');')
[void]$builder.AppendLine('PREPARE bugpk_init_guard_stmt FROM @__bugpk_init_guard;')
[void]$builder.AppendLine('EXECUTE bugpk_init_guard_stmt;')
[void]$builder.AppendLine('DEALLOCATE PREPARE bugpk_init_guard_stmt;')
[void]$builder.AppendLine('SET FOREIGN_KEY_CHECKS = 0;')

foreach ($migration in $migrations) {
  $sql = [System.IO.File]::ReadAllText($migration.File.FullName, [System.Text.Encoding]::UTF8).Trim()
  [void]$builder.AppendLine()
  [void]$builder.AppendLine()
  [void]$builder.AppendLine('-- ============================================================')
  [void]$builder.AppendLine("-- Migration: $($migration.File.Name)")
  [void]$builder.AppendLine('-- ============================================================')
  [void]$builder.AppendLine()
  [void]$builder.AppendLine($sql)
}

[void]$builder.AppendLine()
[void]$builder.AppendLine()
[void]$builder.AppendLine('-- Mark database as migrated for golang-migrate.')
[void]$builder.AppendLine('CREATE TABLE IF NOT EXISTS `schema_migrations` (')
[void]$builder.AppendLine('  `version` bigint NOT NULL,')
[void]$builder.AppendLine('  `dirty` tinyint(1) NOT NULL,')
[void]$builder.AppendLine('  PRIMARY KEY (`version`)')
[void]$builder.AppendLine(') ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;')
[void]$builder.AppendLine('DELETE FROM `schema_migrations`;')
[void]$builder.AppendLine("INSERT INTO ``schema_migrations`` (``version``, ``dirty``) VALUES ($latestVersion, 0);")
[void]$builder.AppendLine()
[void]$builder.AppendLine('SET FOREIGN_KEY_CHECKS = 1;')
[void]$builder.AppendLine('DO RELEASE_LOCK(@__bugpk_init_lock);')

$content = $builder.ToString().Replace("`r`n", "`n")

if ($Variant -eq 'production') {
  # 生产初始化包必须保持"无用户种子、不自选数据库"两条不变量。
  $productionSql = [regex]::Replace(
    $content,
    '(?ims)^INSERT(?: IGNORE)? INTO `bp_user`\s*\([^)]*\)\s*VALUES\s*.*?;\r?\n',
    ''
  )
  $productionSql = [regex]::Replace(
    $productionSql,
    '(?ims)^INSERT(?: IGNORE)? INTO `bp_user_role`\s*\([^)]*\)\s*VALUES\s*.*?;\r?\n',
    ''
  )
  if ($productionSql -match '(?ims)^INSERT(?: IGNORE)? INTO `bp_user(?:_role)?`\s*\([^)]*\)\s*VALUES') {
    throw 'Production SQL still contains a user seed'
  }
  if ($productionSql -notmatch '__BUGPK_INIT_REQUIRES_EMPTY_SELECTED_DATABASE__') {
    throw 'Production SQL is missing the empty-database guard'
  }
  $content = $productionSql
}

if ($Check) {
  if (-not (Test-Path -LiteralPath $outputPath -PathType Leaf) -or
      [System.IO.File]::ReadAllText($outputPath).Replace("`r`n", "`n") -cne $content) {
    throw "Initialization SQL is missing or stale: $Output. Run scripts/generate-full-sql.ps1 -Variant $Variant."
  }
  Write-Host "SQL_FULL_CHECK=PASS variant=$Variant version=$latestVersion output=$Output"
  return
}

$outputDir = Split-Path -Parent $outputPath
if (-not (Test-Path -LiteralPath $outputDir)) {
  New-Item -ItemType Directory -Path $outputDir -Force | Out-Null
}

$utf8NoBom = [System.Text.UTF8Encoding]::new($false)
[System.IO.File]::WriteAllText($outputPath, $content, $utf8NoBom)

Write-Host "SQL_FULL_GENERATE=PASS variant=$Variant version=$latestVersion output=$Output"
