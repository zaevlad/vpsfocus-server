#!/bin/sh
# Заливка установочных файлов и запуск установки или обновления.
# МЕНЯЕТ: /opt/vpsfocus (файлы стека), /opt/vpsfocus.run (журнал прогона),
# запускает install.sh или update.sh из бандла. Модуль: stack.rs
# (launch_script_into).
#
# Это обёртка: проверяет права и отдаёт тело (stack-launch-body.sh) оболочке
# под root. Тело приходит через heredoc, а не аргументом `sh -c`: в нём
# содержимое .env с паролями, а аргументы видны в списке процессов.
#
# Права проверяются заранее и своим маркером: без этого `sudo -n` просто
# уронил бы скрипт, и приложение показало бы «нечитаемый результат» вместо
# «нужны права».

set -eu
SUDO=""
if [ "$(id -u)" != 0 ]; then
  if command -v sudo >/dev/null 2>&1 && sudo -n true 2>/dev/null; then
    SUDO="sudo -n"
  else
    printf '::step:preflight\n::fail:preflight:notRoot\n'
    exit 1
  fi
fi
exec $SUDO sh -s <<'VPSFOCUS_LOCK_EOF_2f9c7a'
@BODY@
VPSFOCUS_LOCK_EOF_2f9c7a
