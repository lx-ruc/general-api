#!/bin/sh
# 慧沐引擎 Agent 一键接入助手 · 引导器
# 用法（推荐，stdin 保持终端可交互）：
#   sh -c "$(curl -fsSL __HUIMU_BASE__/agent-helper)"
# 带参数（非交互安装）：
#   sh -c "$(curl -fsSL __HUIMU_BASE__/agent-helper)" install claude-code --model <模型名> --key sk-...
set -e

BASE='__HUIMU_BASE__'
DIR="$HOME/.huimu"

if ! command -v node >/dev/null 2>&1; then
  echo "错误：未检测到 Node.js（需要 >= 18），请先安装：https://nodejs.org" >&2
  exit 1
fi

mkdir -p "$DIR"
if curl -fsSL "$BASE/agent-helper.mjs" -o "$DIR/helper.mjs"; then
  :
elif command -v wget >/dev/null 2>&1; then
  wget -qO "$DIR/helper.mjs" "$BASE/agent-helper.mjs"
else
  echo "错误：curl/wget 均不可用" >&2
  exit 1
fi

# sh -c "脚本" 首参数会落到 $0（不进 "$@"），这里把被吞掉的子命令找回来
case "$0" in
  install|uninstall|status|selftest|wizard) set -- "$0" "$@" ;;
esac

exec node "$DIR/helper.mjs" --base "$BASE" "$@"
