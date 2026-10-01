#!/bin/sh
# Что стоит на сервере: сколько контейнеров мониторинга работает, его версия,
# состав проектов и отметка сервера для команды. ТОЛЬКО ЧТЕНИЕ.
# Модуль: stack.rs (status).
#
# Параметры:
#   DIR — каталог стека, /opt/vpsfocus.
#
# Вывод: `::counts:работает:должно` первой строкой, затем
# stack-version.json, затем после меток — хеш и содержимое projects.json и
# server.json. Хеш считает сервер: так приложение сверяет, не изменил ли файл
# кто-то из команды между чтением и записью.

SUDO=""
[ "$(id -u)" = 0 ] || SUDO="sudo -n"
running=0
expected=0
if command -v docker >/dev/null 2>&1 && [ -f "$DIR/docker-compose.yml" ]; then
    running=$($SUDO sh -c "cd $DIR && docker compose ps --status running -q 2>/dev/null" | grep -c . || true)
    expected=$($SUDO sh -c "cd $DIR && docker compose config --services 2>/dev/null" | grep -c . || true)
fi
printf '::counts:%s:%s\n' "$running" "$expected"
$SUDO test -r "$DIR/stack-version.json" && $SUDO cat "$DIR/stack-version.json" || true
echo '::projects_hash'
$SUDO sha256sum "$DIR/agent-config/projects.json" 2>/dev/null | cut -d' ' -f1 || true
echo '::projects_content'
$SUDO cat "$DIR/agent-config/projects.json" 2>/dev/null || true
echo '::server_hash'
$SUDO sha256sum "$DIR/server.json" 2>/dev/null | cut -d' ' -f1 || true
echo '::server_content'
$SUDO cat "$DIR/server.json" 2>/dev/null || true
