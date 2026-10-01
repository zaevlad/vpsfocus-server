package main

import (
	"context"
	"testing"
	"time"
)

// Круги агента не стартуют залпом.
//
// До этого спринта проверки, ссылки, замеры и обход начинали первый круг в
// первую же секунду после запуска — то есть после каждого обновления стека
// сайт клиента получал полный обход, проверку всех ссылок и круг замеров
// разом. Сквозное требование трека называет это прямо: нужен разнос стартов
// и посчитанный бюджет нагрузки на чужой прод.
func TestFirstRoundsAreStaggered(t *testing.T) {
	// Порядок не случайный: сначала то, что не ходит по страницам клиента,
	// потом то, что ходит, — и самое тяжёлое последним.
	order := []task{taskChecks, taskProbes, taskLinks, taskVitals, taskSeo, taskBackup}

	previous := time.Duration(-1)
	for _, which := range order {
		delay := startDelay(which)
		if delay <= previous {
			t.Fatalf("круг %d стартует через %v, а предыдущий через %v: старты сошлись",
				which, delay, previous)
		}
		previous = delay
	}
}

// Разнос — это «не всё сразу», а не «реже беспокоить».
//
// Человек, только что обновивший стек, вправе увидеть свежие данные в
// ближайшие минуты. Полчаса ожидания он прочитает как сломанный агент.
// Граница подвинулась с десяти минут до пятнадцати вместе с кругом копии:
// он последний в очереди и единственный, чей результат человек не ждёт
// глазами, — но и он обязан уложиться в те же «минуты, а не завтра».
func TestStaggerStaysWithinMinutes(t *testing.T) {
	for _, which := range []task{taskChecks, taskProbes, taskLinks, taskVitals, taskSeo, taskBackup} {
		if delay := startDelay(which); delay > 15*time.Minute {
			t.Fatalf("круг %d ждёт %v — это уже не разнос, а молчание", which, delay)
		}
	}
}

// Самые лёгкие круги идут первыми: проверки спрашивают реестр и TLS-порт, а
// ключевые адреса — три запроса. Обход сайта — сотни, и он последний.
func TestCheapestRoundsGoFirst(t *testing.T) {
	if startDelay(taskChecks) > time.Minute {
		t.Fatal("проверки сроков ждут дольше минуты: экран останется пустым без причины")
	}
	if startDelay(taskSeo) < startDelay(taskLinks) {
		t.Fatal("обход сайта стартует раньше проверки ссылок: тяжёлое ушло вперёд лёгкого")
	}
}

// wait возвращает false, когда агента останавливают: круг, продолженный
// после SIGTERM, пишет в закрывающуюся базу.
func TestWaitStopsWithTheAgent(t *testing.T) {
	if !wait(t.Context(), time.Millisecond) {
		t.Fatal("живой контекст не дождался паузы")
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if wait(ctx, time.Minute) {
		t.Fatal("остановленный агент досидел паузу до конца")
	}
	// Нулевая пауза тоже смотрит на контекст: иначе круг, стартовавший в
	// момент остановки, прошёл бы её насквозь.
	if wait(ctx, 0) {
		t.Fatal("нулевая пауза не заметила остановки")
	}
}
