#!/bin/sh
# 数据库备份（两种模式自动识别），保留最近 7 份
# 建议 cron：0 4 * * * /opt/token-gateway/backup.sh
set -e
cd "$(dirname "$0")/.."

KEEP=7
STAMP=$(date +%Y%m%d_%H%M%S)
mkdir -p backups

# 从 config.yaml 读取 driver（无 yaml 工具时的朴素解析；行尾注释一并剥离，
# 否则 `path: "x.db"  # 说明` 会把注释带进文件名，sqlite3 对不存在的库
# 不报错而是新建空库——备份静默变成空文件，恢复即全量丢数据）
DRIVER=$(grep -E '^ *driver:' config.yaml 2>/dev/null | head -1 | sed 's/.*driver: *//;s/[[:space:]][[:space:]]*#.*//;s/[" ]//g')

if [ "$DRIVER" = "postgres" ]; then
  DSN_LINE=$(grep -E '^ *dsn:' config.yaml | head -1 | sed 's/.*dsn: *//;s/[[:space:]][[:space:]]*#.*//;s/"//g;s/[[:space:]]*$//')
  # 从 DSN 提取参数（host= user= password= dbname= port=）
  HOST=$(echo "$DSN_LINE" | tr ' ' '\n' | grep '^host=' | cut -d= -f2)
  PORT=$(echo "$DSN_LINE" | tr ' ' '\n' | grep '^port=' | cut -d= -f2)
  USER=$(echo "$DSN_LINE" | tr ' ' '\n' | grep '^user=' | cut -d= -f2)
  DBNAME=$(echo "$DSN_LINE" | tr ' ' '\n' | grep '^dbname=' | cut -d= -f2)
  export PGPASSWORD=$(echo "$DSN_LINE" | tr ' ' '\n' | grep '^password=' | cut -d= -f2)
  pg_dump -h "${HOST:-localhost}" -p "${PORT:-5432}" -U "${USER:-tg}" "${DBNAME:-token_gateway}" \
    --no-owner --no-privileges | gzip > "backups/${DBNAME:-token_gateway}_$STAMP.sql.gz"
  echo "postgres 备份完成: backups/${DBNAME:-token_gateway}_$STAMP.sql.gz"
else
  DBPATH=$(grep -E '^ *path:' config.yaml | head -1 | sed 's/.*path: *//;s/[[:space:]][[:space:]]*#.*//;s/"//g;s/[[:space:]]*$//')
  DBPATH="${DBPATH:-data/token_.db}"
  # 源库不存在（解析出错/被误删）必须失败退出：sqlite3 对缺失文件会静默新建空库
  if [ ! -f "$DBPATH" ]; then
    echo "错误：数据库文件不存在: $DBPATH" >&2
    exit 1
  fi
  sqlite3 "$DBPATH" ".backup 'backups/sqlite_$STAMP.db'"
  echo "sqlite 备份完成: backups/sqlite_$STAMP.db"
fi

# 轮转：只保留最近 KEEP 份
ls -t backups/sqlite_*.db 2>/dev/null | tail -n +$((KEEP + 1)) | xargs rm -f 2>/dev/null
ls -t backups/*_*.sql.gz 2>/dev/null | tail -n +$((KEEP + 1)) | xargs rm -f 2>/dev/null
echo "当前备份文件："
ls -lh backups/ | tail -8
