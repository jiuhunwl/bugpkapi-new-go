#!/usr/bin/env bash
# BugPk-Api 后端启动脚本（Linux / 宝塔 Go 项目插件）
# 用法：
#   1. 复制同目录 .env.example 为 .env 并填入真实配置
#      （手动导入全量 SQL 的部署请设 MIGRATION_ENABLED=false，防止重复建表）
#   2. 执行: bash start.sh
#   3. 守护进程（宝塔 Go 项目 / Supervisor / systemd 可选）
#
# 注意：二进制（bugpk-api）自身已支持自动加载同目录 .env，
# 宝塔「Go 项目」插件可直接填二进制路径启动，本脚本仅作兜底/手动启动用。
set -euo pipefail

BASE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$BASE_DIR"

# 加载环境变量（.env 中 KEY=value 格式，与二进制、config.yaml 同目录）
# 兼容 Windows CRLF 换行的 .env（避免 source 时 $'\r' 报错）
if [ -f "$BASE_DIR/.env" ]; then
  set -a
  # shellcheck disable=SC1091
  source <(tr -d '\r' < "$BASE_DIR/.env")
  set +a
fi

# 配置文件与二进制同级（平铺结构：bugpk-api、config.yaml、.env、start.sh 在同一目录）
export CONFIG_FILE="${CONFIG_FILE:-$BASE_DIR/config.yaml}"

# 运行环境：production / staging / development
export APP_ENV="${APP_ENV:-production}"

exec "$BASE_DIR/bugpk-api"
