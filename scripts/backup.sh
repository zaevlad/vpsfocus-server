#!/bin/sh
# Выгрузка копии мониторинга на компьютер человека. Модуль: lifecycle.rs
# (backup).
# МЕНЯЕТ: временный каталог /opt/vpsfocus/.backup-XXXXXX на время выгрузки,
# ловушка удаляет его при любом исходе.
#
# Архив уходит в stdout и сохраняется приложением на диск человека. Внутри —
# дамп базы аналитики и файлы стека, **в том числе .env и agent.env с
# паролями**: без них восстановление неполно. Об этом говорит экран.
#
# Дамп кладётся во временный файл, а не гонится в архив потоком: tar должен
# знать размер каждой записи заранее. Пустой дамп — не копия, а ложное
# спокойствие: выход с ошибкой.
#
# Параметры:
#   DIR — каталог стека, /opt/vpsfocus.

set -eu
SUDO=""
[ "$(id -u)" = 0 ] || SUDO="sudo -n"
TMP=$($SUDO mktemp -d "$DIR/.backup-XXXXXX")
trap '$SUDO rm -rf "$TMP"' EXIT

$SUDO sh -c "cd $DIR && docker compose exec -T postgres pg_dump -U umami -d umami" > "$TMP/umami.sql"
[ -s "$TMP/umami.sql" ] || exit 1

for name in stack-version.json docker-compose.yml Caddyfile .env agent.env; do
    if $SUDO test -r "$DIR/$name"; then $SUDO cp "$DIR/$name" "$TMP/$name"; fi
done
if $SUDO test -d "$DIR/agent-config"; then $SUDO cp -r "$DIR/agent-config" "$TMP/"; fi

# Права не раздаются: tar запускается тем же $SUDO, и root читает .env с
# правами 600 без посторонней помощи. Каталог от mktemp — 700.
$SUDO tar -czf - -C "$TMP" .
