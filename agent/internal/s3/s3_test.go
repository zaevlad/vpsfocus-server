package s3

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// Адресация выбирается сама, и это не удобство, а защита от вопроса, на
// который человек не обязан знать ответ.
//
// AWS объявил адресацию путём устаревшей, а B2, R2, Selectel и MinIO
// понимают именно её. Ошибка здесь выглядит как «ключ не подошёл» и уводит
// разбор совсем не туда.
func TestPathStyleIsChosenByEndpoint(t *testing.T) {
	cases := []struct {
		host    string
		setting string
		want    bool
	}{
		{"s3.eu-central-1.amazonaws.com", "", false},
		{"s3.amazonaws.com", "", false},
		{"s3.us-west-004.backblazeb2.com", "", true},
		{"abc123.r2.cloudflarestorage.com", "", true},
		{"minio.local:9000", "", true},
		// Явное указание сильнее догадки: у человека может стоять прокси,
		// о котором мы ничего не знаем.
		{"s3.amazonaws.com", "1", true},
		{"minio.local:9000", "0", false},
	}

	for _, item := range cases {
		if got := pathStyleFor(item.setting, item.host); got != item.want {
			t.Errorf("%s (%q): адресация путём = %v, ожидалось %v",
				item.host, item.setting, got, item.want)
		}
	}
}

// Путь в подписи кодируется по RFC 3986, посегментно.
//
// `url.EscapedPath` оставляет неэкранированным то, что S3 в подписи ждёт
// экранированным, — и подпись расходится на ровном месте, с ответом «ключ не
// подошёл».
func TestPathEncodingKeepsSlashesAndEscapesTheRest(t *testing.T) {
	cases := map[string]string{
		"/bucket/vpsfocus/srv-1/2026-09-08T12-00-00Z.tar.gz": "/bucket/vpsfocus/srv-1/2026-09-08T12-00-00Z.tar.gz",
		"/bucket/копия.tar.gz":                               "/bucket/%D0%BA%D0%BE%D0%BF%D0%B8%D1%8F.tar.gz",
		"/bucket/имя с пробелом":                             "/bucket/%D0%B8%D0%BC%D1%8F%20%D1%81%20%D0%BF%D1%80%D0%BE%D0%B1%D0%B5%D0%BB%D0%BE%D0%BC",
		"":                                                   "/",
	}

	for input, want := range cases {
		if got := encodePath(input); got != want {
			t.Errorf("encodePath(%q) = %q, ожидалось %q", input, got, want)
		}
	}

	// Плюс — это плюс, а не пробел: `url.Values.Encode` кодирует пробел
	// плюсом, и подпись с ним не сходится.
	query := url.Values{"prefix": {"a b"}, "list-type": {"2"}}
	if got := encodeQuery(query); got != "list-type=2&prefix=a%20b" {
		t.Fatalf("строка запроса собрана как %q", got)
	}
}

// Подписанная ссылка несёт всё, что нужно, и живёт недолго.
//
// В ней подпись, дающая доступ к аналитике клиента: срок в сутки означал бы
// сутки, в течение которых утёкшая ссылка работает.
func TestPresignedLinkCarriesTheSignature(t *testing.T) {
	client := testClient(t, "https://s3.example.test")

	link, err := client.Presign("vpsfocus/srv-1/copy.tar.gz", 15*time.Minute)
	if err != nil {
		t.Fatalf("подпись ссылки: %v", err)
	}

	parsed, err := url.Parse(link)
	if err != nil {
		t.Fatalf("ссылка не разобралась: %v", err)
	}
	query := parsed.Query()

	for _, name := range []string{
		"X-Amz-Algorithm", "X-Amz-Credential", "X-Amz-Date",
		"X-Amz-Expires", "X-Amz-SignedHeaders", "X-Amz-Signature",
	} {
		if query.Get(name) == "" {
			t.Errorf("в ссылке нет %s", name)
		}
	}
	if query.Get("X-Amz-Expires") != "900" {
		t.Errorf("срок ссылки %s секунд", query.Get("X-Amz-Expires"))
	}
	if !strings.Contains(parsed.Path, "copy.tar.gz") {
		t.Errorf("ссылка ведёт не туда: %s", parsed.Path)
	}

	// Ключ и секрет в ссылку не попадают: идентификатор ключа в подписи
	// есть по протоколу, сам секрет — никогда.
	if strings.Contains(link, "секрет") {
		t.Fatal("секрет уехал в ссылку")
	}

	if _, err := client.Presign("copy.tar.gz", 24*time.Hour); err == nil {
		t.Fatal("сутки — не срок для ссылки с подписью")
	}
}

// Подпись одного и того же запроса одинакова, а разных — разная.
//
// Проверка не про криптографию, а про то, что в канонический запрос попали
// путь и заголовки: подпись, не зависящая от пути, подошла бы к любому
// объекту — и не подошла бы ни к одному на живом хранилище.
func TestSignatureDependsOnTheRequest(t *testing.T) {
	client := testClient(t, "https://s3.example.test")
	at := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)

	sign := func(key string) string {
		request, err := http.NewRequest(http.MethodGet, "https://s3.example.test/bucket/"+key, nil)
		if err != nil {
			t.Fatal(err)
		}
		client.sign(request, emptyPayload, at)
		return request.Header.Get("Authorization")
	}

	first, second := sign("a.tar.gz"), sign("b.tar.gz")
	if first == second {
		t.Fatal("подпись не зависит от пути: она подошла бы к любому объекту")
	}
	if sign("a.tar.gz") != first {
		t.Fatal("подпись одного запроса дважды разная")
	}
	if !strings.Contains(first, "Credential=ключ/") ||
		!strings.Contains(first, "SignedHeaders=host;x-amz-content-sha256;x-amz-date") {
		t.Fatalf("подпись собрана не по SigV4: %s", first)
	}
}

// Список объектов читается и приходит от новых к старым.
func TestListReadsTheAnswer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("list-type") != "2" {
			t.Errorf("список запрошен не тем способом: %s", r.URL.RawQuery)
		}
		if r.Header.Get("Authorization") == "" {
			t.Error("запрос не подписан")
		}
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(`<?xml version="1.0"?>
<ListBucketResult>
  <IsTruncated>false</IsTruncated>
  <Contents><Key>p/2026-09-01T00-00-00Z.tar.gz</Key><Size>10</Size>
    <LastModified>2026-09-01T00:00:00Z</LastModified></Contents>
  <Contents><Key>p/2026-09-08T00-00-00Z.tar.gz</Key><Size>20</Size>
    <LastModified>2026-09-08T00:00:00Z</LastModified></Contents>
</ListBucketResult>`))
	}))
	defer server.Close()

	client := testClient(t, server.URL)

	objects, err := client.List(context.Background(), "p/", 0)
	if err != nil {
		t.Fatalf("список: %v", err)
	}
	if len(objects) != 2 {
		t.Fatalf("объектов %d", len(objects))
	}
	if objects[0].Key != "p/2026-09-08T00-00-00Z.tar.gz" {
		t.Errorf("порядок не от новых к старым: %s первым", objects[0].Key)
	}
	if objects[0].Size != 20 || objects[0].Modified.IsZero() {
		t.Errorf("объект разобран наполовину: %+v", objects[0])
	}

	// Потолок режет после сортировки: одна копия из двух — это самая
	// свежая, а не первая, которую отдало хранилище. S3 отдаёт ключи по
	// возрастанию, и до спринта 36 счётчик обрывал чтение раньше сортировки
	// — экран показывал бы самую старую копию как последнюю.
	newest, err := client.List(context.Background(), "p/", 1)
	if err != nil {
		t.Fatalf("список с потолком: %v", err)
	}
	if len(newest) != 1 {
		t.Fatalf("потолок не применён: объектов %d", len(newest))
	}
	if newest[0].Key != "p/2026-09-08T00-00-00Z.tar.gz" {
		t.Errorf("под потолок попала не самая свежая копия: %s", newest[0].Key)
	}
}

// Отказ хранилища доходит своим кодом, а не «что-то пошло не так».
//
// «Ключ не подошёл» и «бакета нет» чинятся разными действиями, и первый шаг
// человека зависит от того, что мы ему скажем.
func TestRefusalKeepsTheStorageCode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`<Error><Code>SignatureDoesNotMatch</Code>
			<Message>подпись не сошлась</Message></Error>`))
	}))
	defer server.Close()

	client := testClient(t, server.URL)

	// Удаление, а не HEAD: у HEAD тела нет по протоколу, и кода в нём не
	// будет ни у какого хранилища — там остаётся только номер ответа.
	err := client.Delete(context.Background(), "copy.tar.gz")
	if err == nil {
		t.Fatal("отказ хранилища проехал молча")
	}

	var failure *Error
	if !errors.As(err, &failure) {
		t.Fatalf("ошибка потеряла тип: %v", err)
	}
	if failure.Status != http.StatusForbidden || failure.Code != "SignatureDoesNotMatch" {
		t.Fatalf("код отказа потерян: %+v", failure)
	}

	// HEAD отвечает тем же отказом, но без кода — и это тоже надо донести:
	// «403 без объяснений» лучше, чем молчание.
	if _, err := client.Head(context.Background(), "copy.tar.gz"); err == nil {
		t.Fatal("HEAD проглотил отказ")
	}
}

// Бакет адресуется так, как выбрано: путём или поддоменом.
func TestAddressingPutsTheBucketWhereItBelongs(t *testing.T) {
	path := testClient(t, "https://minio.local:9000")
	host, route := path.url("p/copy.tar.gz")
	if host != "minio.local:9000" || route != "/бакет/p/copy.tar.gz" {
		t.Fatalf("адресация путём: %s%s", host, route)
	}

	virtual := testClient(t, "https://s3.amazonaws.com")
	host, route = virtual.url("p/copy.tar.gz")
	if host != "бакет.s3.amazonaws.com" || route != "/p/copy.tar.gz" {
		t.Fatalf("адресация поддоменом: %s%s", host, route)
	}
}

// Настройки без бакета или без ключа — это не «попробуем и посмотрим».
func TestEmptySettingsAreRefusedUpFront(t *testing.T) {
	cases := []Config{
		{Bucket: "b", KeyID: "k", Secret: "s"},
		{Endpoint: "https://s3.example.test", KeyID: "k", Secret: "s"},
		{Endpoint: "https://s3.example.test", Bucket: "b", Secret: "s"},
		{Endpoint: "https://s3.example.test", Bucket: "b", KeyID: "k"},
	}
	for _, cfg := range cases {
		if _, err := New(cfg); err == nil {
			t.Errorf("настройки %+v приняты, хотя работать им нечем", cfg)
		}
	}

	// Схему дописываем сами: человек копирует адрес из панели хостера.
	client, err := New(Config{Endpoint: "s3.example.test", Bucket: "b", KeyID: "k", Secret: "s"})
	if err != nil {
		t.Fatalf("адрес без схемы отвергнут: %v", err)
	}
	if client.Endpoint() != "https://s3.example.test" {
		t.Fatalf("адрес собран как %s", client.Endpoint())
	}
}

func testClient(t *testing.T, endpoint string) *Client {
	t.Helper()
	client, err := New(Config{
		Endpoint: endpoint,
		Bucket:   "бакет",
		KeyID:    "ключ",
		Secret:   "секрет",
	})
	if err != nil {
		t.Fatalf("клиент: %v", err)
	}
	return client
}
