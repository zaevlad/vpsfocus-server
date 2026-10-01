#!/bin/sh
# Тело чтения журнала установки: выполняется под root внутри stack-follow.sh.
#
# Почему не `tail -f`: его нечем остановить изнутри конвейера, а бросать на
# чужом сервере процесс, который никто не закроет, нельзя. Цикл читает ровно
# `размер - смещение` байт (`tail -c` плюс `head -c`), поэтому приложение
# всегда знает, сколько прочитало, и после обрыва связи продолжает с того же
# байта — без повторов и без пропусков.
#
# Итог уходит в stderr: stdout байт в байт повторяет журнал, и любая наша
# строка в нём сбила бы счёт смещения.
#
# Цикл скупой на процессы: он крутится на чужой машине всё время установки, а
# машина бывает одноядерной и занятой apt. Отсюда круг в две секунды, один
# `wc` на круг и проверка живости прогона раз в пять кругов.
#
# Параметры:
#   RUN    — каталог прогона, /opt/vpsfocus.run;
#   WANT   — идентификатор своего прогона; пусто — любой;
#   OFFSET — с какого байта журнала читать;
#   GRACE  — сколько кругов ждать каталога прогона, пока его нет.

while [ ! -d "$RUN" ]; do
    GRACE=$((GRACE - 1))
    if [ "$GRACE" -le 0 ]; then printf '::run:missing\n' >&2; exit 0; fi
    sleep 2
done

if [ -n "$WANT" ]; then
    SEEN="$(cat "$RUN/id" 2>/dev/null || true)"
    if [ "$SEEN" != "$WANT" ]; then printf '::run:foreign\n' >&2; exit 0; fi
fi

GONE=0
TICK=0
while :; do
    # Итог смотрим ДО размера журнала, а не после. `done` пишется, когда
    # установка уже вышла и журнал закрыт, — значит размер, снятый после
    # него, окончательный. В обратном порядке прогон мог дописать последние
    # строки и закончиться между `wc` и проверкой `done`, и следящий вышел
    # бы, не дочитав их (аудит 2026-09-22).
    FINISHED=0
    if [ -f "$RUN/done" ]; then FINISHED=1; fi

    # ${SIZE##* } снимает пробелы, которыми wc на части систем выравнивает
    # число, — без второго процесса на каждый круг.
    SIZE="$(wc -c < "$RUN/log" 2>/dev/null || true)"
    SIZE="${SIZE##* }"
    [ -n "$SIZE" ] || SIZE=0

    READ=0
    if [ "$SIZE" -gt "$OFFSET" ]; then
        tail -c "+$((OFFSET + 1))" "$RUN/log" 2>/dev/null | head -c "$((SIZE - OFFSET))"
        OFFSET="$SIZE"
        READ=1
    fi

    if [ "$FINISHED" = 1 ]; then
        printf '::run:%s\n' "$(cat "$RUN/done" 2>/dev/null || true)" >&2
        exit 0
    fi

    if [ "$READ" = 1 ]; then continue; fi

    # Жив ли прогон — раз в пять кругов. Прогон, дошедший до конца сам,
    # ловится файлом done выше; сюда доходит только убитый.
    TICK=$((TICK + 1))
    if [ "$TICK" -ge 5 ]; then
        TICK=0
        PID="$(cat "$RUN/pid" 2>/dev/null || true)"
        if [ -z "$PID" ]; then
            GRACE=$((GRACE - 5))
            if [ "$GRACE" -le 0 ]; then printf '::run:missing\n' >&2; exit 0; fi
        elif kill -0 "$PID" 2>/dev/null; then
            GONE=0
        else
            GONE=$((GONE + 1))
            if [ "$GONE" -ge 2 ]; then printf '::run:gone\n' >&2; exit 0; fi
        fi
    fi

    sleep 2
done
