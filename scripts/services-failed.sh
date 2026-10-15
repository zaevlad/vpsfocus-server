#!/bin/sh
# Упавшие службы systemd сервера. ТОЛЬКО ЧТЕНИЕ (трек AB, идея 4, спринт 163).
#
# Агент живёт в контейнере, и systemd хоста ему недоступен; путь через сокет
# D-Bus — того же класса, что сокет Docker (через него можно и управлять), и
# агенту его не дают. Поэтому спрашивает приложение, по SSH, при открытии
# «Здоровья сервера» и по «Обновить».
#
# Скрипт отдаётся на stdin (`sh -s`) и на диск сервера не попадает.
# `systemctl --failed` проверен `find / -newer` до и после: не создаёт ни
# файла. Новая команда сюда — только после такого же прогона.
#
# Факты, а не оценки (Р10): безвредные упавшие службы вроде cloud-init —
# обычное дело, и решает, как их показать, приложение (`serverload.rs`).
# Узнать не удалось — `none` (systemd нет: VPS в контейнере LXC или
# OpenVZ) или `unreadable`, а не пустой список, выданный за «всё работает».
#
# POSIX sh. Вывод — одна строка JSON, другой печати в stdout нет.

set -u

esc() {
    printf '%s' "$1" | sed 's/\\/\\\\/g; s/"/\\"/g' | tr -d '\000-\037'
}

has() { command -v "$1" >/dev/null 2>&1; }

LIMIT=""
has timeout && LIMIT="timeout 20"

state="none"
lines=""
# systemd работает, если есть /run/systemd/system — так проверяет и сам
# systemd (sd_booted). Одной `systemctl` мало: в контейнере она бывает
# установлена, а systemd не запущен.
if has systemctl && [ -d /run/systemd/system ]; then
    # Список упавших читается без прав root. Только службы: упавший
    # .mount или .socket человеку без навыков ничего не скажет, а службы —
    # это то, что он ставил сам.
    if out="$($LIMIT systemctl --failed --plain --no-legend --no-pager --type=service 2>/dev/null)"; then
        state="ok"
        # «имя загрузка активность подсостояние описание» — строки через «;»,
        # не больше ста: больше на экране не прочесть.
        lines="$(printf '%s\n' "$out" | head -n 100 | tr '\n' ';')"
    else
        state="unreadable"
    fi
fi

printf '{"state":"%s","services":"%s"}\n' "$state" "$(esc "$lines")"
