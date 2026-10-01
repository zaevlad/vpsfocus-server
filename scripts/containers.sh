#!/bin/sh
# Контейнеры сервера: что запущено, когда и сколько раз перезапускалось.
# ТОЛЬКО ЧТЕНИЕ (трек W, спринт 124).
#
# Нужен разбору сбоя: «контейнер сайта перезапустился в 14:03» — то, чего
# агент не видит и видеть не должен (сокет Docker в его контейнере равен
# root на сервере клиента). Поэтому спрашивает приложение, по SSH, разово и
# по кнопке.
#
# Скрипт отдаётся на stdin (`sh -s`) и на диск сервера не попадает. Обе
# команды — `docker ps -a` и `docker inspect` — проверены `find / -newer`
# в контейнере до и после: не создают ни файла, даже `~/.docker`.
# Новая команда сюда — только после такого же прогона.
#
# Факты, а не оценки: что из этого подсказка, решает приложение
# (`containers.rs`, `outage-hints.ts`). Узнать не удалось — `unreadable`
# или `none`, а не пустой список, выданный за «контейнеров нет».
#
# POSIX sh. Вывод — одна строка JSON, другой печати в stdout нет.

set -u

esc() {
    printf '%s' "$1" | sed 's/\\/\\\\/g; s/"/\\"/g' | tr -d '\000-\037'
}

has() { command -v "$1" >/dev/null 2>&1; }

LIMIT=""
has timeout && LIMIT="timeout 20"

# Права: контейнеры видны root и группе docker; остальным — через
# `sudo -n`, если он есть без пароля. Пароль не спрашивается никогда.
# Пробуем той же командой, что и читаем, — новых команд ради проверки прав
# не заводим.
PRIV=""
if has docker && ! $LIMIT docker ps -a -q >/dev/null 2>&1; then
    if has sudo && sudo -n true 2>/dev/null; then
        PRIV="sudo -n"
    fi
fi

# Сколько контейнеров берём. Больше на экране не прочесть, а `inspect`
# по сотням — секунды чужого одноядерного сервера.
MAX=200

state="none"
lines=""
if has docker; then
    if ids="$($LIMIT $PRIV docker ps -a -q 2>/dev/null)"; then
        state="ok"
        ids="$(printf '%s\n' "$ids" | head -n "$MAX" | tr '\n' ' ')"
        if [ -n "$(printf '%s' "$ids" | tr -d ' ')" ]; then
            # shellcheck disable=SC2086
            lines="$($LIMIT $PRIV docker inspect --format \
'{{.Name}}|{{.Config.Image}}|{{.State.Status}}|{{.RestartCount}}|{{.State.StartedAt}}|{{.State.FinishedAt}}|{{.State.ExitCode}}|{{.State.OOMKilled}}|{{index .Config.Labels "com.docker.compose.project"}}' \
                $ids 2>/dev/null | tr '\n' ';')" || state="unreadable"
        fi
    else
        state="unreadable"
    fi
fi

printf '{"dockerState":"%s","now":%s,"containers":"%s"}\n' \
    "$state" "$(date +%s 2>/dev/null || echo 0)" "$(esc "$lines")"
