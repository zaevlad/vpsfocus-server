#!/bin/sh
# Подключение к мониторингу, который уже поставил кто-то из команды: пароли
# стека и настройки агента с сервера. ТОЛЬКО ЧТЕНИЕ. Модуль: team.rs
# (read_secrets).
#
# Прочитанное ложится в хранилище ОС этой машины и на сервер vpsFocus не
# уходит. Нет root и нет `sudo -n` — `::noSudo`: человеку важно знать, что
# ему не хватает доступа, а не что «файла нет».
#
# Параметры:
#   DIR       — каталог стека, /opt/vpsfocus;
#   AGENT_ENV — имя файла настроек агента, agent.env.

SUDO=""
if [ "$(id -u)" != 0 ]; then
	if command -v sudo >/dev/null 2>&1 && sudo -n true 2>/dev/null; then
		SUDO="sudo -n"
	else
		echo '::noSudo'
		exit 0
	fi
fi
echo '::env'
$SUDO cat "$DIR/.env" 2>/dev/null || true
echo '::agentenv'
$SUDO cat "$DIR/$AGENT_ENV" 2>/dev/null || true
