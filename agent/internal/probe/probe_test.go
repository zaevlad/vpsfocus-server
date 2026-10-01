package probe

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func serve(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server
}

func run(t *testing.T, profile Profile) Result {
	t.Helper()
	return Run(t.Context(), Client(5*time.Second), profile)
}

func TestPageIsHtmlAndNotEmpty(t *testing.T) {
	server := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<html><body>магазин</body></html>"))
	})

	result := run(t, Profile{Kind: KindPage, URL: server.URL})
	if !result.OK {
		t.Fatalf("проверка не прошла: %+v", result)
	}
	if result.Status != 200 {
		t.Fatalf("код %d, ждали 200", result.Status)
	}
}

// Двухсотка с пустым телом — обычный признак приложения, упавшего после
// отправки заголовков. Считать её удачей значит молчать ровно там, где
// сайт сломан.
func TestEmptyPageIsAFailure(t *testing.T) {
	server := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
	})

	if result := run(t, Profile{Kind: KindPage, URL: server.URL}); result.Code != CodeNotHTML {
		t.Fatalf("код %q, ждали %q", result.Code, CodeNotHTML)
	}
}

// Ответ API, отданный страницей ошибки, — то, ради чего профиль `json` и
// существует: код 200 при этом честный, а данных нет.
func TestJSONProfileRejectsHtml(t *testing.T) {
	server := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html>502 Bad Gateway</html>"))
	})

	if result := run(t, Profile{Kind: KindJSON, URL: server.URL}); result.Code != CodeNotJSON {
		t.Fatalf("код %q, ждали %q", result.Code, CodeNotJSON)
	}
}

func TestJSONProfileAcceptsJSON(t *testing.T) {
	server := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})

	if result := run(t, Profile{Kind: KindJSON, URL: server.URL}); !result.OK {
		t.Fatalf("проверка не прошла: %+v", result)
	}
}

// Перенаправление — предмет проверки, а не помеха: проходить его нельзя,
// иначе профиль `redirect` проверял бы страницу назначения.
func TestRedirectIsNotFollowed(t *testing.T) {
	server := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/old" {
			http.Redirect(w, r, "https://example.com/new", http.StatusMovedPermanently)
			return
		}
		w.WriteHeader(http.StatusTeapot)
	})

	result := run(t, Profile{
		Kind:   KindRedirect,
		URL:    server.URL + "/old",
		Expect: "https://example.com/new",
	})
	if !result.OK {
		t.Fatalf("проверка не прошла: %+v", result)
	}
	if result.Status != http.StatusMovedPermanently {
		t.Fatalf("код %d, ждали 301", result.Status)
	}
}

func TestRedirectToTheWrongPlaceFails(t *testing.T) {
	server := serve(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://example.com/other", http.StatusFound)
	})

	result := run(t, Profile{
		Kind:   KindRedirect,
		URL:    server.URL,
		Expect: "https://example.com/new",
	})
	if result.Code != CodeWrongTarget {
		t.Fatalf("код %q, ждали %q", result.Code, CodeWrongTarget)
	}
}

// Сравнение по началу строки пропускало чужой хост, начинающийся с нашего:
// `example.com.evil.io` входит в `example.com` так же, как `vpsfocus.xyz`
// входил в `vpsfocus.xyz.evil.io` в сверке Origin на сайте. Ключевой адрес
// объявлялся работающим, а посетитель уезжал на чужой сервер.
func TestRedirectToALookalikeHostFails(t *testing.T) {
	server := serve(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://example.com.evil.io/", http.StatusFound)
	})

	result := run(t, Profile{
		Kind:   KindRedirect,
		URL:    server.URL,
		Expect: "https://example.com",
	})
	if result.Code != CodeWrongTarget {
		t.Fatalf("код %q, ждали %q: чужой хост прошёл проверку", result.Code, CodeWrongTarget)
	}
}

// Причина, по которой нельзя требовать равенства строк, остаётся верной:
// сервер добавляет параметры, человек пишет адрес без хвостового слэша.
func TestRedirectIgnoresQueryAndTrailingSlash(t *testing.T) {
	server := serve(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://example.com/новая/?utm_source=письмо", http.StatusFound)
	})

	result := run(t, Profile{
		Kind:   KindRedirect,
		URL:    server.URL,
		Expect: "https://example.com/новая",
	})
	if !result.OK {
		t.Fatalf("проверка не прошла: %+v", result)
	}
}

// Относительный `Location` законен и встречается чаще полного: он
// разрешается относительно того адреса, который спрашивали.
func TestRelativeRedirectIsResolved(t *testing.T) {
	server := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "/вход")
		w.WriteHeader(http.StatusFound)
	})

	result := run(t, Profile{
		Kind:   KindRedirect,
		URL:    server.URL + "/кабинет",
		Expect: "/вход",
	})
	if !result.OK {
		t.Fatalf("проверка не прошла: %+v", result)
	}
}

// Перенаправление с https на http — понижение, а не мелочь.
func TestRedirectDowngradeFails(t *testing.T) {
	server := serve(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://example.com/новая", http.StatusFound)
	})

	result := run(t, Profile{
		Kind:   KindRedirect,
		URL:    server.URL,
		Expect: "https://example.com/новая",
	})
	if result.Code != CodeWrongTarget {
		t.Fatalf("код %q, ждали %q", result.Code, CodeWrongTarget)
	}
}

// Страница, отдавшая 200 вместо перенаправления, — это сломанный редирект,
// а не удача.
func TestRedirectProfileNeedsThreeHundred(t *testing.T) {
	server := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})

	result := run(t, Profile{Kind: KindRedirect, URL: server.URL, Expect: "https://example.com/"})
	if result.Code != CodeStatus {
		t.Fatalf("код %q, ждали %q", result.Code, CodeStatus)
	}
}

// Текст ищется без учёта регистра: человек пишет искомое по памяти, а не
// копирует из разметки.
func TestTextIsFoundRegardlessOfCase(t *testing.T) {
	server := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<h1>ОФОРМИТЬ ЗАКАЗ</h1>"))
	})

	result := run(t, Profile{Kind: KindText, URL: server.URL, Expect: "Оформить заказ"})
	if !result.OK {
		t.Fatalf("проверка не прошла: %+v", result)
	}
}

// Шаблон, отданный без данных, — ровно то, что ловит профиль `text`.
func TestMissingTextIsAFailure(t *testing.T) {
	server := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<h1>{{ title }}</h1>"))
	})

	result := run(t, Profile{Kind: KindText, URL: server.URL, Expect: "Оформить заказ"})
	if result.Code != CodeTextMissing {
		t.Fatalf("код %q, ждали %q", result.Code, CodeTextMissing)
	}
}

// Сорокачетвёрка на ключевом адресе — беда, а не «не пятисотка».
func TestNotFoundIsAFailure(t *testing.T) {
	server := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	result := run(t, Profile{Kind: KindPage, URL: server.URL})
	if result.Code != CodeStatus {
		t.Fatalf("код %q, ждали %q", result.Code, CodeStatus)
	}
	if result.Status != http.StatusNotFound {
		t.Fatalf("код ответа %d потерян", result.Status)
	}
}

// Заданный код ответа проверяется точно: закрытый раздел, отвечающий 401
// без токена, — это его нормальная работа. Разметки от такого ответа никто
// не ждёт: отказ, которого человек ждал сам, и есть ожидаемый результат.
func TestExpectedStatusIsExact(t *testing.T) {
	server := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})

	if result := run(t, Profile{Kind: KindPage, URL: server.URL, Status: 401}); !result.OK {
		t.Fatalf("проверка не прошла: %+v", result)
	}
	if result := run(t, Profile{Kind: KindPage, URL: server.URL, Status: 200}); result.Code != CodeStatus {
		t.Fatalf("код %q, ждали %q", result.Code, CodeStatus)
	}
}

// Заголовки уезжают в запрос: локальная проверка вправе нести токен
// закрытого раздела — она живёт на сервере клиента, и это его секрет.
func TestHeadersReachTheRequest(t *testing.T) {
	seen := ""
	server := serve(t, func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html>ок</html>"))
	})

	run(t, Profile{
		Kind:    KindPage,
		URL:     server.URL,
		Headers: map[string]string{"Authorization": "Bearer секрет-клиента"},
	})
	if seen != "Bearer секрет-клиента" {
		t.Fatalf("заголовок доехал как %q", seen)
	}
}

// Профиль без того, что составляет его смысл, — это не строгая проверка, а
// тихая: она проходила бы всегда.
func TestProfileWithoutItsPointIsMisconfigured(t *testing.T) {
	for _, profile := range []Profile{
		{Kind: KindText, URL: "https://example.com/"},
		{Kind: KindRedirect, URL: "https://example.com/"},
		{Kind: KindPage},
		{Kind: "выдуманный", URL: "https://example.com/"},
	} {
		if result := run(t, profile); result.Code != CodeMisconfigured {
			t.Fatalf("для %+v код %q, ждали %q", profile, result.Code, CodeMisconfigured)
		}
	}
}

// До адреса не достучались — это отдельный код, а не «сервер ответил не то».
func TestUnreachableHasItsOwnCode(t *testing.T) {
	// Порт, на котором заведомо никого нет: сервер поднят и тут же закрыт.
	server := httptest.NewServer(http.NotFoundHandler())
	url := server.URL
	server.Close()

	result := run(t, Profile{Kind: KindPage, URL: url})
	if result.Code != CodeUnreachable && result.Code != CodeTimeout {
		t.Fatalf("код %q, ждали недоступность", result.Code)
	}
	if result.OK {
		t.Fatal("проверка объявлена удачной")
	}
}

// Время ответа доезжает наружу: медленный ключевой адрес — это то, ради
// чего проверку заводят, и цифра нужна человеку, а не только флаг.
func TestDurationIsMeasured(t *testing.T) {
	server := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(20 * time.Millisecond)
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html>ок</html>"))
	})

	result := run(t, Profile{Kind: KindPage, URL: server.URL})
	if result.DurationMs < 15 {
		t.Fatalf("замер %d мс, ждали хотя бы двадцать", result.DurationMs)
	}
}
