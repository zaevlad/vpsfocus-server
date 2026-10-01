#!/bin/sh
# Запись файла настроек мониторинга: настройки уведомлений (agent.env),
# списки сайтов, страниц, логов, проектов, ключевых адресов, общий календарь
# команды. Модули: agent.rs (write_remote), calendar_share.rs.
# МЕНЯЕТ: один файл в каталоге стека; при RESTART=1 после записи
# пересоздаёт контейнер агента, чтобы он перечитал agent.env.
#
# Запись идёт поверх свежего (Р7): если файл с момента чтения изменил кто-то
# из команды, запись не делается, и приложение перечитывает. Под замком на
# /opt с ожиданием до 15 секунд: две записи разом смешали бы содержимое.
#
# Параметры:
#   FILE     — путь к файлу в каталоге стека;
#   EXPECTED — хеш, который приложение видело при чтении; пусто — не сверять;
#   MODE     — права файла (600 у agent.env: в нём пароли);
#   GROUP    — группа файла; пусто — не менять;
#   STACK    — каталог стека, /opt/vpsfocus;
#   RESTART  — 1: пересоздать контейнер агента после записи;
#   IN_PLACE — текст write-in-place.sh: сама запись.
# На месте @CONTENT@ — содержимое файла в heredoc. Через stdin, а не
# аргументом: в agent.env пароли, а аргументы видны в списке процессов.
#
# Коды: 75 — замок не дался за 15 секунд; `::missing:инструмент` — на
# сервере нет flock, sha256sum, mktemp или cmp.

set -eu
SUDO=""
[ "$(id -u)" = 0 ] || SUDO="sudo -n"
for tool in flock sha256sum mktemp cmp; do
	command -v "$tool" >/dev/null 2>&1 || { printf '::missing:%s\n' "$tool"; exit 0; }
done
OUTCOME=$($SUDO flock -w 15 -E 75 /opt sh -c "$IN_PLACE" write "$FILE" "$EXPECTED" "$MODE" "$GROUP" <<'VPSFOCUS_AGENT_EOF_5c81af'
@CONTENT@
VPSFOCUS_AGENT_EOF_5c81af
)
echo "$OUTCOME"
if [ "$RESTART" = 1 ] && [ "$OUTCOME" = "::written" ]; then
	cd "$STACK" && $SUDO docker compose up -d agent >/dev/null 2>&1
fi
