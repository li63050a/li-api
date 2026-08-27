#!/usr/bin/env bash
# 一键交叉编译所有 Go 支持的平台/架构
# 产物全部为静态链接（CGO_ENABLED=0），无任何 .so 依赖
# 个别平台（android/ios）必须开启 cgo 才能链接，会被自动跳过。
set -uo pipefail

MODULE="api-gateway"
OUT_DIR="dist"
LDFLAGS="-s -w"

mkdir -p "$OUT_DIR"

echo "==> 开始交叉编译（CGO_ENABLED=0 ${LDFLAGS}）"

built=0
skipped=0

# 遍历 go 支持的所有 GOOS/GOARCH 组合
while IFS=/ read -r GOOS GOARCH; do
    # 输出文件名：windows 加 .exe，js/wasm 加 .wasm
    ext=""
    [ "$GOOS" = "windows" ] && ext=".exe"
    [ "$GOOS" = "js" ] && ext=".wasm"
    output="${OUT_DIR}/${MODULE}-${GOOS}-${GOARCH}${ext}"

    if CGO_ENABLED=0 GOOS="$GOOS" GOARCH="$GOARCH" \
        go build -ldflags="$LDFLAGS" -o "$output" . 2>/dev/null; then
        echo "  [OK]   $GOOS/$GOARCH"
        built=$((built + 1))
    else
        echo "  [SKIP] $GOOS/$GOARCH (需要 cgo，已跳过)"
        skipped=$((skipped + 1))
        rm -f "$output"
    fi
done < <(go tool dist list)

echo "==> 完成：成功 $built 个，跳过 $skipped 个，产物在 $OUT_DIR/"
