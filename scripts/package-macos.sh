#!/usr/bin/env bash
# Собирает mess.app (universal: arm64 + x86_64) и заворачивает его в .dmg.
#
# Универсальный бинарь нужен, чтобы один файл работал и на Apple Silicon, и на
# Intel: Intel-раннеров у GitHub мало и они висят в очереди часами, а собрать
# обе архитектуры на Apple Silicon можно за один проход.
#
# Бандл (а не голый бинарь) обязателен ещё и потому, что без Info.plist с
# NSMicrophoneUsageDescription macOS не даст доступ к микрофону.
#
# Использование: scripts/package-macos.sh <версия> [выходной .dmg]
set -euo pipefail

VERSION="${1:-0.0.0}"
OUT="${2:-mess-macos.dmg}"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

OPUS_VERSION=1.5.2
OPUS_URL="https://downloads.xiph.org/releases/opus/opus-${OPUS_VERSION}.tar.gz"

echo ">> собираю libopus под обе архитектуры"
curl -sL --retry 3 -o "$WORK/opus.tar.gz" "$OPUS_URL"
tar xzf "$WORK/opus.tar.gz" -C "$WORK"

# Логи пишем в файл и показываем ТОЛЬКО при падении: молча глотать вывод
# нельзя — иначе при ошибке остаёшься с голым «exit 2» и без единой подсказки.
build_opus() { # $1 = arch, $2 = host-триплет
  local arch="$1" host="$2"
  local log="$WORK/opus-$arch.log"
  cp -R "$WORK/opus-${OPUS_VERSION}" "$WORK/opus-build-$arch"
  if ! (
    cd "$WORK/opus-build-$arch"
    ./configure --host="$host" --prefix="$WORK/opus-$arch" \
      --disable-shared --enable-static --disable-doc --disable-extra-programs \
      CFLAGS="-arch $arch -mmacosx-version-min=11.0" \
      LDFLAGS="-arch $arch -mmacosx-version-min=11.0" >"$log" 2>&1
    # без -j: сборка opus занимает секунды, а параллельная изредка
    # разваливается на гонке в автотулзах — надёжность тут важнее
    make >>"$log" 2>&1
    make install >>"$log" 2>&1
  ); then
    echo "!! libopus ($arch) не собрался, хвост лога:" >&2
    tail -30 "$log" >&2
    exit 1
  fi
}
build_opus arm64 aarch64-apple-darwin
build_opus x86_64 x86_64-apple-darwin

echo ">> собираю приложение под обе архитектуры"
# Пути к opus прописываем ЯВНО в CGO_*FLAGS, а не только через PKG_CONFIG_PATH:
# последний не входит в ключ кэша сборки Go, поэтому от прошлого запуска
# подхватывался уже удалённый каталог и линковка падала с "library 'opus' not
# found". PKG_CONFIG_LIBDIR (а не PATH) заодно отрезает системные пути, чтобы
# в x86_64-сборку не приехал arm64-опус из Homebrew.
build_app() { # $1 = arch, $2 = GOARCH
  local arch="$1" goarch="$2"
  local prefix="$WORK/opus-$arch"
  PKG_CONFIG_LIBDIR="$prefix/lib/pkgconfig" \
  CGO_ENABLED=1 GOOS=darwin GOARCH="$goarch" \
  CGO_CFLAGS="-arch $arch -mmacosx-version-min=11.0 -I$prefix/include" \
  CGO_LDFLAGS="-arch $arch -mmacosx-version-min=11.0 -L$prefix/lib" \
  go build -tags nolibopusfile -ldflags="-s -w" -o "$WORK/mess-$arch" "$ROOT/cmd/backmess-app"
}
build_app arm64 arm64
build_app x86_64 amd64
lipo -create -output "$WORK/mess" "$WORK/mess-arm64" "$WORK/mess-x86_64"

echo ">> собираю иконку"
ICONSET="$WORK/mess.iconset"
mkdir -p "$ICONSET"
for sz in 16 32 128 256 512; do
  sips -z $sz $sz "$ROOT/Icon.png" --out "$ICONSET/icon_${sz}x${sz}.png" >/dev/null
  sips -z $((sz*2)) $((sz*2)) "$ROOT/Icon.png" --out "$ICONSET/icon_${sz}x${sz}@2x.png" >/dev/null
done
iconutil -c icns "$ICONSET" -o "$WORK/mess.icns"

echo ">> собираю mess.app"
APP="$WORK/dist/mess.app"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"
cp "$WORK/mess" "$APP/Contents/MacOS/mess"
chmod +x "$APP/Contents/MacOS/mess"
cp "$WORK/mess.icns" "$APP/Contents/Resources/icon.icns"
cat > "$APP/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleName</key><string>mess</string>
	<key>CFBundleDisplayName</key><string>mess</string>
	<key>CFBundleExecutable</key><string>mess</string>
	<key>CFBundleIdentifier</key><string>dev.djaploy.mess</string>
	<key>CFBundleIconFile</key><string>icon.icns</string>
	<key>CFBundlePackageType</key><string>APPL</string>
	<key>CFBundleShortVersionString</key><string>${VERSION}</string>
	<key>CFBundleVersion</key><string>${VERSION}</string>
	<key>LSMinimumSystemVersion</key><string>11.0</string>
	<key>NSHighResolutionCapable</key><true/>
	<!-- без этого macOS не даст доступ к микрофону и звонки будут молчать -->
	<key>NSMicrophoneUsageDescription</key>
	<string>Микрофон нужен, чтобы тебя слышали в голосовых каналах.</string>
</dict>
</plist>
PLIST

# Подписи у нас нет (нужен платный Apple Developer), поэтому подписываем
# ad-hoc: без этого macOS на Apple Silicon вообще откажется запускать бинарь.
codesign --force --deep --sign - "$APP" 2>/dev/null || echo "   (ad-hoc подпись не удалась, не критично)"

echo ">> собираю dmg"
ln -s /Applications "$WORK/dist/Applications"
rm -f "$OUT"
hdiutil create -volname "mess" -srcfolder "$WORK/dist" -ov -format UDZO -quiet "$OUT"

echo ">> готово: $OUT"
lipo -info "$APP/Contents/MacOS/mess" | sed 's/^/   /'
