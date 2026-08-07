#!/usr/bin/env bash
# ============================================================
# Tommy-Cat Agent CLI 部署脚本
#
# 用法:
#   ./deploy/deploy-cli.sh build             # 仅编译
#   ./deploy/deploy-cli.sh install           # 编译并安装到 INSTALL_DIR
#   ./deploy/deploy-cli.sh run [配置文件]    # 编译（如未编译）并启动交互式 CLI
#
# 可配置环境变量:
#   INSTALL_DIR   二进制安装目录        (默认 <repo>/bin)
#   CONFIG_FILE   配置文件路径          (默认 <repo>/config/config.yaml，run 的位置参数优先)
#   GOPROXY       Go 代理               (国内建议 https://goproxy.cn,direct)
# ============================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

INSTALL_DIR="${INSTALL_DIR:-$ROOT_DIR/bin}"
CONFIG_FILE="${CONFIG_FILE:-$ROOT_DIR/config/config.yaml}"
BIN_NAME="tommy-agent"
BIN_PATH="$INSTALL_DIR/$BIN_NAME"

log() { echo "[deploy-cli] $*"; }

check_go() {
    if command -v go >/dev/null 2>&1; then
        return 0
    fi
    # 常见非 PATH 安装位置兜底
    if [ -x "$HOME/local/go/bin/go" ]; then
        export PATH="$HOME/local/go/bin:$PATH"
        return 0
    fi
    log "错误: 未找到 go 命令，请安装 Go 1.25+ 或 export PATH"
    exit 1
}

do_build() {
    check_go
    log "编译 $BIN_NAME ..."
    mkdir -p "$INSTALL_DIR"
    (cd "$ROOT_DIR" && go build -o "$BIN_PATH" ./cmd/agent)
    log "编译完成: $BIN_PATH"
}

do_install() {
    do_build
    # install 模式下额外复制到系统级目录（可选，失败不阻断）
    if [ "${SYSTEM_INSTALL:-0}" = "1" ]; then
        local target="/usr/local/bin/$BIN_NAME"
        if cp "$BIN_PATH" "$target" 2>/dev/null; then
            log "已安装到 $target"
        else
            log "提示: 复制到 $target 失败（可能需要 sudo），二进制位于 $BIN_PATH"
        fi
    else
        log "安装位置: $BIN_PATH（设 SYSTEM_INSTALL=1 可同时复制到 /usr/local/bin）"
    fi
}

do_run() {
    local cfg="${1:-$CONFIG_FILE}"
    [ -x "$BIN_PATH" ] || do_build
    [ -f "$cfg" ] || { log "错误: 配置文件不存在: $cfg"; exit 1; }
    log "启动 CLI（配置: $cfg），退出请输入 /quit"
    # CLI 是交互式 REPL，必须前台运行并保持 stdin/tty
    exec "$BIN_PATH" "$cfg"
}

usage() {
    sed -n '2,13p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
}

case "${1:-}" in
    build)   do_build ;;
    install) do_install ;;
    run)     do_run "${2:-}" ;;
    *)       usage; exit 1 ;;
esac
