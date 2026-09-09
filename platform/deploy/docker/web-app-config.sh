#!/bin/sh
# 生成前端运行时配置 /usr/share/nginx/html/app-config.js（XM-AUTH1）。
#
# 放在 /docker-entrypoint.d/ 下：官方 nginx 镜像的入口脚本会在起 nginx 之前按
# 文件名顺序执行这里的 *.sh（必须可执行，web.Dockerfile 里 chmod +x）。
# 任何一步失败都让容器起不来（set -e）——一个没有 app-config.js 的 web 容器会
# 静默回落到 dev-header，在生产那就是每个请求 403，不如当场停下。
#
# 为什么是运行时而不是构建期：同一个 xingmang/web 镜像要同时服务 staging
# （dev-header）与生产（oidc）；鉴权方式烧进构建产物就得每个环境各构建一份。
#
# 输入（环境变量）：
#   XM_WEB_AUTH_MODE        dev-header（默认）| oidc | local
#                           local＝管理台自带账号密码登录（XM-LOGIN），会话是
#                           HttpOnly Cookie，不需要下面两个 oidc 专属变量
#   XM_WEB_OIDC_ISSUER      oidc 时必填，例 https://auth.solov.cc/realms/solov-staff
#                           必须与 platform-api 的 XM_OIDC_ISSUER 逐字相同
#   XM_WEB_OIDC_CLIENT_ID   oidc 时必填，例 xingmang-admin-web
#                           必须与 platform-api 的 XM_OIDC_AUDIENCE 相同
#   XM_WEB_OIDC_SCOPES      可选；留空由前端取 "openid profile email"
#   XM_INVOICE_CONSOLE_ORIGIN 可选；开票控制台嵌入来源（CR-0005 平台线 i），
#                           例 https://invoice.solov.cc（不含路径）。静态直传，
#                           同 reqlog/CPA 先例——不像上面的 auth 三项那样互相
#                           校验；这里只做直传 + 一句非致命提醒，真正「形状
#                           对不对」由前端 runtimeConfig.ts 判断，缺省或形状
#                           不对时三处开票页签按未配置处理，不拦容器启动
#   XM_WEB_APP_CONFIG_PATH  可选；默认 /usr/share/nginx/html/app-config.js（本地验证用）
#
# 输出文件的形状（浏览器端 src/auth/runtimeConfig.ts 读它）：
#   window.__XM_CONFIG__ = { "authMode": "...", "oidcIssuer": "...", "oidcClientId": "..." };
set -eu

mode="${XM_WEB_AUTH_MODE:-dev-header}"
issuer="${XM_WEB_OIDC_ISSUER:-}"
client_id="${XM_WEB_OIDC_CLIENT_ID:-}"
scopes="${XM_WEB_OIDC_SCOPES:-}"
invoice_console_origin="${XM_INVOICE_CONSOLE_ORIGIN:-}"
out="${XM_WEB_APP_CONFIG_PATH:-/usr/share/nginx/html/app-config.js}"

case "$mode" in
  dev-header|oidc|local) ;;
  *)
    echo "[web-app-config] XM_WEB_AUTH_MODE 只能是 dev-header、oidc 或 local（当前：$mode）" >&2
    exit 1
    ;;
esac

if [ "$mode" = "oidc" ]; then
  if [ -z "$issuer" ]; then
    echo "[web-app-config] XM_WEB_AUTH_MODE=oidc 时 XM_WEB_OIDC_ISSUER 必填" >&2
    exit 1
  fi
  if [ -z "$client_id" ]; then
    echo "[web-app-config] XM_WEB_AUTH_MODE=oidc 时 XM_WEB_OIDC_CLIENT_ID 必填" >&2
    exit 1
  fi
  case "$issuer" in
    https://*) ;;
    *) echo "[web-app-config] 警告：XM_WEB_OIDC_ISSUER 不是 https:// 地址（$issuer），只应出现在本地联调" >&2 ;;
  esac
fi

# 静态直传，不像上面的 oidc 三项那样致命校验：这里只提醒、不 exit 1——
# 缺省或形状不对时前端 runtimeConfig.ts 会按未配置处理（三处开票页签显示
# PageState kind=unavailable），拦启动反而会让一个可选功能挡住整个 web 容器
case "$invoice_console_origin" in
  ""|https://*) ;;
  *) echo "[web-app-config] 警告：XM_INVOICE_CONSOLE_ORIGIN 不是 https:// 地址（$invoice_console_origin），前端会按未配置处理" >&2 ;;
esac

# 写进 JS 字符串字面量前做最小转义：去换行、转义反斜杠与双引号。
# 这些值是运营填的**公开**配置（issuer / client id 不是秘密），转义只是防手滑
# （值里带引号）把整个文件写成语法错误——那会让前端悄悄回落到 dev-header。
js_string() {
  printf '%s' "$1" | tr -d '\r\n' | sed -e 's/\\/\\\\/g' -e 's/"/\\"/g'
}

tmp="$out.tmp.$$"
{
  echo "// 由 deploy/docker/web-app-config.sh 在容器启动时生成，请勿手改（XM-AUTH1）。"
  echo "window.__XM_CONFIG__ = {"
  echo "  \"authMode\": \"$(js_string "$mode")\","
  echo "  \"oidcIssuer\": \"$(js_string "$issuer")\","
  echo "  \"oidcClientId\": \"$(js_string "$client_id")\","
  if [ -n "$scopes" ]; then
    echo "  \"oidcScopes\": \"$(js_string "$scopes")\","
  fi
  if [ -n "$invoice_console_origin" ]; then
    echo "  \"invoiceConsoleOrigin\": \"$(js_string "$invoice_console_origin")\","
  fi
  echo "  \"generatedAt\": \"$(date -u +%Y-%m-%dT%H:%M:%SZ)\""
  echo "};"
} > "$tmp"
mv "$tmp" "$out"

echo "[web-app-config] 已生成 $out：authMode=$mode issuer=${issuer:-<无>} clientId=${client_id:-<无>} invoiceConsoleOrigin=${invoice_console_origin:-<未配置>}"
