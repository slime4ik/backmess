#!/usr/bin/env bash
# Собирает mess под Linux и кладёт в .tar.gz вместе с ярлыком и иконкой,
# плюс install.sh, который раскладывает всё по местам (ярлык в меню приложений).
#
# Голый бинарь тоже работает, но тогда приложения нет в меню и иконка не
# подхватывается — для друзей это выглядит недоделкой.
#
# Использование: scripts/package-linux.sh <версия> [выходной .tar.gz]
set -euo pipefail

VERSION="${1:-0.0.0}"
OUT="${2:-mess-linux-amd64.tar.gz}"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

DIR="$WORK/mess"
mkdir -p "$DIR"

echo ">> собираю бинарь"
CGO_ENABLED=1 go build -tags nolibopusfile -ldflags="-s -w" -o "$DIR/mess" "$ROOT/cmd/backmess-app"

cp "$ROOT/Icon.png" "$DIR/mess.png"

cat > "$DIR/mess.desktop" <<DESKTOP
[Desktop Entry]
Type=Application
Name=mess
Comment=Голосовой чат для своих
Exec=mess
Icon=mess
Terminal=false
Categories=Network;InstantMessaging;AudioVideo;
DESKTOP

cat > "$DIR/install.sh" <<'INSTALL'
#!/usr/bin/env sh
# Ставит mess для текущего пользователя (root не нужен).
set -e
DIR="$(cd "$(dirname "$0")" && pwd)"
mkdir -p "$HOME/.local/bin" "$HOME/.local/share/applications" "$HOME/.local/share/icons/hicolor/512x512/apps"
install -m 755 "$DIR/mess" "$HOME/.local/bin/mess"
install -m 644 "$DIR/mess.png" "$HOME/.local/share/icons/hicolor/512x512/apps/mess.png"
install -m 644 "$DIR/mess.desktop" "$HOME/.local/share/applications/mess.desktop"
update-desktop-database "$HOME/.local/share/applications" 2>/dev/null || true
echo "готово: mess в меню приложений."
case ":$PATH:" in
  *":$HOME/.local/bin:"*) ;;
  *) echo "добавь в PATH: export PATH=\"\$HOME/.local/bin:\$PATH\"" ;;
esac
INSTALL
chmod +x "$DIR/install.sh"

cat > "$DIR/README.txt" <<TXT
mess ${VERSION}

Запустить сразу:      ./mess
Поставить в меню:     ./install.sh

Нужны системные библиотеки (обычно уже стоят на любом десктопе):
  Debian/Ubuntu: sudo apt install libgl1 libx11-6 libxkbcommon0 libwayland-client0 libopus0
  Fedora:        sudo dnf install mesa-libGL libX11 libxkbcommon wayland libopus
TXT

echo ">> пакую"
rm -f "$OUT"
tar czf "$OUT" -C "$WORK" mess
echo ">> готово: $OUT"
