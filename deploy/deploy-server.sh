#!/usr/bin/env bash
# ============================================================
# Tommy-Cat Agent HTTP 服务部署脚本
#
# 用法:
#   ./deploy/deploy-server.sh build              # 仅编译
#   ./deploy/deploy-server.sh start              # 编译并后台启动（如未编译）
#   ./deploy/deploy-server.sh stop               # 停止服务
#   ./deploy/deploy-server.sh restart            # 重启
#   ./deploy/deploy-server.sh status             # 查看状态
#
# 可配置环境变量:
#   INSTALL_DIR   二进制安装目录        (默认 <repo>/bin)
#   CONFIG_FILE   配置文件路径          (默认 <repo>/config/config.yaml)
#   SERVER_ADDR   监听地址              (默认读取配置不便，脚本缺省 :8080)
#   GOPROXY       Go 代理               (国内建议 https://goproxy.cn,direct)
#
# 运行产物:
#   deploy/run/tommy-server.pid   进程 PID 文件
#   deploy/logs/server.log        服务日志（stdout/stderr 追加）
# ============================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

INSTALL_DIR="${INSTALL_DIR:-$ROOT_DIR/bin}"
RUN_DIR="$SCRIPT_DIR/run"
LOG_DIR="$SCRIPT_DIR/logs"
CONFIG_FILE="${CONFIG_FILE:-$ROOT_DIR/config/config.yaml}"
SERVER_ADDR="${SERVER_ADDR:-:8080}"
BIN_NAME="tommy-server"
BIN_PATH="$INSTALL_DIR/$BIN_NAME"
PID_FILE="$RUN_DIR/$BIN_NAME.pid"
LOG_FILE="$LOG_DIR/server.log"
HEALTH_URL="http://localhost${SERVER_ADDR}/api/v1/health"

log() { echo "[deploy-server] $*"; }

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
    (cd "$ROOT_DIR" && go build -o "$BIN_PATH" ./cmd/server)
    log "编译完成: $BIN_PATH"
}

is_running() {
    [ -f "$PID_FILE" ] || return 1
    local pid
    pid="$(cat "$PID_FILE" 2>/dev/null || true)"
    [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null
}

wait_health() {
    # 最多等待 15 秒健康检查通过；无 curl 时跳过
    if ! command -v curl >/dev/null 2>&1; then
        log "提示: 未安装 curl，跳过健康检查"
        return 0
    fi
    local i
    for i in $(seq 1 15); do
        if curl -fsS "$HEALTH_URL" >/dev/null 2>&1; then
            log "健康检查通过: $HEALTH_URL"
            return 0
        fi
        sleep 1
    done
    log "警告: 15 秒内健康检查未通过，请查看日志: $LOG_FILE"
    return 1
}

do_start() {
    if is_running; then
        log "服务已在运行 (PID $(cat "$PID_FILE"))"
        return 0
    fi
    [ -x "$BIN_PATH" ] || do_build
    [ -f "$CONFIG_FILE" ] || { log "错误: 配置文件不存在: $CONFIG_FILE"; exit 1; }

    # 提示：server 段未启用时进程会直接退出
    if ! grep -qE '^\s*mode:\s*"http"' "$CONFIG_FILE"; then
        log "警告: $CONFIG_FILE 中未发现 server.mode: \"http\"，请确认已启用 HTTP 模式"
    fi

    mkdir -p "$RUN_DIR" "$LOG_DIR"
    log "启动服务: $BIN_PATH（配置: $CONFIG_FILE）"
    nohup "$BIN_PATH" "$CONFIG_FILE" >>"$LOG_FILE" 2>&1 &
    echo $! > "$PID_FILE"
    sleep 1
    if ! is_running; then
        log "错误: 进程启动后立即退出，请查看日志: $LOG_FILE"
        rm -f "$PID_FILE"
        exit 1
    fi
    log "已启动 (PID $(cat "$PID_FILE"))，日志: $LOG_FILE"
    wait_health || true
}

do_stop() {
    if ! is_running; then
        log "服务未在运行"
        rm -f "$PID_FILE"
        return 0
    fi
    local pid
    pid="$(cat "$PID_FILE")"
    log "停止服务 (PID $pid) ..."
    kill "$pid" 2>/dev/null || true
    # 最多等待 10 秒优雅退出，超时强杀
    local i
    for i in $(seq 1 10); do
        kill -0 "$pid" 2>/dev/null || break
        sleep 1
    done
    if kill -0 "$pid" 2>/dev/null; then
        log "优雅退出超时，强制终止"
        kill -9 "$pid" 2>/dev/null || true
    fi
    rm -f "$PID_FILE"
    log "已停止"
}

do_status() {
    if is_running; then
        log "运行中 (PID $(cat "$PID_FILE"))"
        if command -v curl >/dev/null 2>&1; then
            curl -fsS "$HEALTH_URL" && echo "" || log "健康检查失败（进程存活但端点未响应）"
        fi
    else
        log "未运行"
        exit 1
    fi
}

usage() {
    sed -n '2,17p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
}

case "${1:-}" in
    build)   do_build ;;
    start)   do_start ;;
    stop)    do_stop ;;
    restart) do_stop; do_start ;;
    status)  do_status ;;
    *)       usage; exit 1 ;;
esac
