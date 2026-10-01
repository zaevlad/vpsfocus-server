#!/bin/sh
# «Данные сайта»: карточки к базе SQLite сайта. ТОЛЬКО ЧТЕНИЕ.
# Модуль: query/sqlite.rs (script).
#
# База открывается только на чтение и в безопасном режиме (`sqlite3
# -readonly -safe`): запрос карточки не может ни записать в базу, ни
# выполнить команду. Строк — не больше заданного предела, у каждого запроса
# свой потолок времени. Пароль не нужен: файл читается правами входа по SSH.
#
# Проверки — один раз, дальше карточка за карточкой.
#
# Параметры:
#   DB            — путь к файлу базы; приложение пропускает только абсолютный
#                   путь без кавычек и подстановок;
#   MIN_MAJOR, MIN_MINOR — самая старая годная версия sqlite3 (нужен -json);
#   SECONDS_LIMIT — потолок времени одного запроса;
#   BUSY_MS       — сколько ждать, если база занята записью сайта;
#   LINES         — предел строк ответа (на одну больше показываемых — знак,
#                   что строк больше).
# На месте @CARDS@ — строки `card номер 'SQL в base64'`. SQL едет в base64, и
# экранировать его для оболочки не приходится вовсе.
#
# Вывод: в stderr — метки `vpsfocus:…` (версия, начало, конец, код выхода
# карточки), в stdout — ответ каждой карточки в JSON после своей метки.

db=$DB
command -v sqlite3 >/dev/null 2>&1 || { echo 'vpsfocus:sqlite=missing' >&2; exit 0; }
v=$(sqlite3 -version 2>/dev/null | cut -d' ' -f1)
major=${v%%.*}; rest=${v#*.}; minor=${rest%%.*}
case "$major$minor" in ''|*[!0-9]*) echo "vpsfocus:sqlite=old $v" >&2; exit 0 ;; esac
if [ "$major" -lt "$MIN_MAJOR" ] || { [ "$major" -eq "$MIN_MAJOR" ] && [ "$minor" -lt "$MIN_MINOR" ]; }; then
echo "vpsfocus:sqlite=old $v" >&2; exit 0
fi
echo "vpsfocus:sqlite=$v" >&2
[ -r "$db" ] || { echo 'vpsfocus:open=denied' >&2; exit 0; }
T=""; timeout 1 true >/dev/null 2>&1 && T="timeout $SECONDS_LIMIT"
card() {
echo "vpsfocus:card=$1"; echo "vpsfocus:card=$1" >&2
echo "vpsfocus:start=$(date +%s%N)" >&2
printf '%s' "$2" | base64 -d | { $T sqlite3 -readonly -safe -bail -json -cmd ".timeout $BUSY_MS" "$db"; echo "vpsfocus:exit=$?" >&2; } | head -n "$LINES"
echo "vpsfocus:end=$(date +%s%N)" >&2
}

@CARDS@
