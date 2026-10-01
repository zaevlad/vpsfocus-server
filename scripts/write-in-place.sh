#!/bin/sh
# Запись файла настроек поверх свежего: выполняется под замком на /opt внутри
# write-file.sh (`sh -c` с текстом этого файла). Содержимое — на stdin.
#
# Аргументы: файл, ожидаемый хеш (пусто — не сверять), права, группа (пусто —
# не менять).
#
# Своя оболочка не наследует `set -eu` внешнего скрипта, поэтому он здесь
# свой: без него сбой `cat` во временный файл (полный диск) не остановил бы ни
# `mv`, ни `::written`, и на месте agent.env оказался бы обрезанный.
#
# Итог: `::changed` — файл с момента чтения изменил кто-то другой, запись не
# делалась; `::same` — там уже то же самое; `::written` — записан. Временный
# файл убирает ловушка при любом исходе.

set -eu
file="$1"; expected="$2"; mode="$3"; group="$4"
tmp=""
cleanup() { [ -z "$tmp" ] || rm -f "$tmp"; }
trap cleanup EXIT
current=$(sha256sum "$file" 2>/dev/null | cut -d" " -f1 || true)
if [ -n "$expected" ] && [ "$current" != "$expected" ]; then
	echo ::changed
	exit 0
fi
tmp=$(mktemp "$file.XXXXXX")
cat > "$tmp"
if [ -f "$file" ] && cmp -s "$tmp" "$file"; then
	echo ::same
	exit 0
fi
[ -z "$group" ] || chown "root:$group" "$tmp"
chmod "$mode" "$tmp"
mv "$tmp" "$file"
tmp=""
echo ::written
