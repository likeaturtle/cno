#!/usr/bin/env bash
# 更新自建 DERP 的 TLS 证书：从源目录复制并重命名，然后重启 docker 服务
set -euo pipefail

# ====== 可配置项（也可用环境变量覆盖）======
SRC_DIR="${SRC_DIR:-/home/ubuntu/ssl_certs}"                 # 证书源目录
DST_DIR="${DST_DIR:-/home/ubuntu/tailscale_derp/certs}"      # DERP 证书目录
COMPOSE_DIR="${COMPOSE_DIR:-/home/ubuntu/tailscale_derp}"    # docker-compose 所在目录
DOMAIN="${DOMAIN:-derper.flashatom.online}"                  # DERP 域名（决定目标文件名）
CONTAINER_NAME="${CONTAINER_NAME:-derper}"                   # 容器名（compose 不可用时的回退）
# 源文件名（也可用环境变量覆盖）
SRC_CERT="${SRC_CERT:-flashatom.online.crt}"
SRC_KEY="${SRC_KEY:-flashatom.online.key}"

log()  { echo "[$(date '+%F %T')] $*"; }
die()  { echo "[ERROR] $*" >&2; exit 1; }

# docker 命令封装：无权限时自动改用 sudo
docker_cmd() {
  if docker info &>/dev/null; then
    docker "$@"
  elif command -v sudo >/dev/null; then
    sudo docker "$@"
  else
    die "无法访问 Docker（权限不足）：请用 sudo 运行本脚本，或将用户加入 docker 组（sudo usermod -aG docker \$USER）"
  fi
}

[[ -d "$SRC_DIR" ]] || die "源目录不存在: $SRC_DIR"
[[ -d "$DST_DIR" ]] || mkdir -p "$DST_DIR"

# ---- 1. 定位源证书/私钥 ----
[[ -n "$SRC_CERT" ]] || die "未设置 SRC_CERT"
[[ -n "$SRC_KEY"  ]] || die "未设置 SRC_KEY"
[[ -f "$SRC_DIR/$SRC_CERT" ]] || die "证书不存在: $SRC_DIR/$SRC_CERT"
[[ -f "$SRC_DIR/$SRC_KEY"  ]] || die "私钥不存在: $SRC_DIR/$SRC_KEY"
log "源证书: $SRC_DIR/$SRC_CERT"
log "源私钥: $SRC_DIR/$SRC_KEY"

# ---- 2. 校验证书/私钥匹配 ----
cert_pub="$(openssl x509 -in "$SRC_DIR/$SRC_CERT" -noout -pubkey 2>/dev/null)" \
  || die "无法解析证书文件（是否为有效 X.509 证书？）: $SRC_CERT"
key_pub="$(openssl pkey -in "$SRC_DIR/$SRC_KEY" -pubout 2>/dev/null)" \
  || die "无法解析私钥文件（是否为有效私钥？）: $SRC_KEY"
[[ "$cert_pub" == "$key_pub" ]] || die "证书与私钥不匹配，中止操作"

expiry="$(openssl x509 -in "$SRC_DIR/$SRC_CERT" -noout -enddate | cut -d= -f2)"
log "证书有效期至: $expiry"

DST_CERT="$DST_DIR/${DOMAIN}.crt"
DST_KEY="$DST_DIR/${DOMAIN}.key"

# ---- 3. 复制并重命名（只保留最近一次被替换的备份） ----
for f in "$DST_CERT" "$DST_KEY"; do
  if [[ -f "$f" ]]; then
    cp -a "$f" "$f.bak"
    log "已备份: $f -> $f.bak（覆盖上一次备份）"
  fi
done

# 写入临时文件后再原子替换，避免中断留下半截文件
tmp_cert="$(mktemp "$DST_DIR/.cert.XXXXXX")"
tmp_key="$(mktemp "$DST_DIR/.key.XXXXXX")"
trap 'rm -f "$tmp_cert" "$tmp_key"' EXIT

cp "$SRC_DIR/$SRC_CERT" "$tmp_cert"
cp "$SRC_DIR/$SRC_KEY"  "$tmp_key"
chmod 644 "$tmp_cert"
chmod 600 "$tmp_key"
mv -f "$tmp_cert" "$DST_CERT"
mv -f "$tmp_key"  "$DST_KEY"
trap - EXIT

log "已更新: $DST_CERT"
log "已更新: $DST_KEY"

# ---- 4. 重启 docker 服务 ----
if [[ -f "$COMPOSE_DIR/docker-compose.yml" || -f "$COMPOSE_DIR/docker-compose.yaml" || -f "$COMPOSE_DIR/compose.yaml" ]]; then
  log "在 $COMPOSE_DIR 执行 docker compose restart"
  (cd "$COMPOSE_DIR" && docker_cmd compose restart)
elif docker_cmd ps -a --format '{{.Names}}' | grep -qx "$CONTAINER_NAME"; then
  log "docker restart $CONTAINER_NAME"
  docker_cmd restart "$CONTAINER_NAME"
else
  die "未找到 compose 文件，也未找到容器 $CONTAINER_NAME，请检查 COMPOSE_DIR / CONTAINER_NAME"
fi

log "完成。可用 docker logs -f $CONTAINER_NAME 确认服务正常。"
