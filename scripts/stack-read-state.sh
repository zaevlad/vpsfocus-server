#!/bin/sh
# Файлы установленного мониторинга — перед установкой, обновлением и заменой
# сертификата. ТОЛЬКО ЧТЕНИЕ. Модуль приложения: stack.rs (read_remote_state).
#
# Зачем: стек уже стоит, и его пароли и списки дороже записи о нём на этой
# машине. Том Postgres хранит пароль, заданный при создании базы, —
# сгенерированный заново не подошёл бы.
#
# Параметры (приложение пишет их строками перед текстом):
#   DIR — каталог стека, /opt/vpsfocus.
#
# Вывод: содержимое каждого файла после своей метки `::имя`. Файла нет —
# метка без содержимого.

SUDO=""
[ "$(id -u)" = 0 ] || SUDO="sudo -n"

show() {
    $SUDO test -r "$DIR/$1" && $SUDO cat "$DIR/$1" || true
}

echo '::env'
show .env
echo '::agentenv'
show agent.env
echo '::sites'
show agent-config/sites.json
echo '::pages'
show agent-config/vitals.json
echo '::logs'
show agent-config/logs.json
echo '::projects'
show agent-config/projects.json
echo '::probes'
show agent-config/probes.json
echo '::servermark'
show server.json
