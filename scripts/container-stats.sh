#!/bin/sh
# Что ест ресурсы: процессор и память контейнеров сейчас. ТОЛЬКО ЧТЕНИЕ
# (трек AB, идея 8, спринт 163).
#
# Агенту сокет Docker не дают ни ради какой функции, поэтому спрашивает
# приложение, по SSH, при открытии «Здоровья сервера» и по «Обновить». Тот
# же путь прав, что у `containers.sh` (Р13): root, группа docker или
# `sudo -n` без пароля; пароль не спрашивается никогда.
#
# Скрипт отдаётся на stdin (`sh -s`) и на диск сервера не попадает.
# `docker stats --no-stream` и `docker ps` проверены `find / -newer` до и
# после: не создают ни файла, даже `~/.docker`. Новая команда сюда —
# только после такого же прогона.
#
# Параметр STACK — имя стека (`stack::STACK_NAME`), дописывает приложение.
#
# Процессор — мгновенный замер Docker, а не среднее: экран так и
# подписывает («сейчас»). Узнать не удалось — `none` или `unreadable`, а не
# пустой список, выданный за «контейнеров нет».
#
# POSIX sh. Вывод — одна строка JSON, другой печати в stdout нет.

set -u

esc() {
    printf '%s' "$1" | sed 's/\\/\\\\/g; s/"/\\"/g' | tr -d '\000-\037'
}

has() { command -v "$1" >/dev/null 2>&1; }

LIMIT=""
has timeout && LIMIT="timeout 20"

PRIV=""
if has docker && ! $LIMIT docker ps -q >/dev/null 2>&1; then
    if has sudo && sudo -n true 2>/dev/null; then
        PRIV="sudo -n"
    fi
fi

state="none"
lines=""
ours=""
if has docker; then
    if out="$($LIMIT $PRIV docker stats --no-stream --format '{{.Name}}|{{.CPUPerc}}|{{.MemUsage}}|{{.MemPerc}}' 2>/dev/null)"; then
        state="ok"
        lines="$(printf '%s\n' "$out" | head -n 200 | tr '\n' ';')"
        # Контейнеры самого мониторинга — по метке compose, а не по имени:
        # имя контейнера человек выбирает сам.
        ours="$($LIMIT $PRIV docker ps --filter "label=com.docker.compose.project=$STACK" --format '{{.Names}}' 2>/dev/null | tr '\n' ';')"
    else
        state="unreadable"
    fi
fi

printf '{"dockerState":"%s","stats":"%s","ours":"%s"}\n' "$state" "$(esc "$lines")" "$(esc "$ours")"
