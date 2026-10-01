// Package s3 — минимальный клиент S3-совместимого хранилища.
//
// Своими руками, а не готовым SDK: агент живёт на проде клиента, собирается
// статически под две архитектуры, и тянуть ради четырёх запросов дерево
// зависимостей размером с сам агент незачем. Нужны ровно PUT, HEAD, список,
// удаление и подписанная ссылка — остальное S3 умеет без нас.
//
// Хранилище оплачивает клиент, и оно у него любое: Backblaze B2, Cloudflare
// R2, Wasabi, Selectel, Yandex Object Storage, AWS, MinIO на соседней
// машине. Протокол у всех один, различается только адресация — и она
// выбирается сама, а не спрашивается у человека.
package s3

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Сколько ждать хранилище. Выгрузка идёт своим таймаутом — она долгая, — а
// это про короткие запросы: список, HEAD, удаление.
const requestTimeout = 30 * time.Second

// Регион по умолчанию. Подпись SigV4 без региона не собирается вовсе, а
// половина совместимых хранилищ его не использует: там принято `us-east-1`.
const defaultRegion = "us-east-1"

// Config — что нужно, чтобы попасть в чужой бакет.
type Config struct {
	// Адрес хранилища целиком, со схемой: https://s3.us-west-004.backblazeb2.com
	Endpoint string
	Region   string
	Bucket   string
	KeyID    string
	Secret   string
	// Адресация: имя бакета в пути или в имени хоста. Пусто — решаем сами.
	//
	// Строкой, а не булевым: «не задано» и «задано в ноль» здесь разные
	// ответы, а окружение других форм не знает.
	PathStyle string
}

// Client — клиент одного бакета.
type Client struct {
	cfg    Config
	http   *http.Client
	scheme string
	host   string
	// Путь-префикс эндпоинта: у MinIO за обратным прокси бывает и такое.
	base      string
	pathStyle bool
}

// Object — то, что лежит в бакете.
type Object struct {
	Key      string
	Size     int64
	Modified time.Time
}

// Error — отказ хранилища с его собственным кодом.
//
// Код нужен разбору: «ключ не подошёл» и «бакета нет» чинятся по-разному, и
// сказать об этом человеку надо разными словами.
type Error struct {
	Status int
	Code   string
	Msg    string
}

func (e *Error) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("хранилище ответило %d", e.Status)
	}
	return fmt.Sprintf("хранилище ответило %d: %s", e.Status, e.Code)
}

// New собирает клиент и проверяет, что настройки вообще похожи на настройки.
func New(cfg Config) (*Client, error) {
	if strings.TrimSpace(cfg.Endpoint) == "" {
		return nil, errors.New("адрес хранилища не задан")
	}
	if strings.TrimSpace(cfg.Bucket) == "" {
		return nil, errors.New("бакет не задан")
	}
	if strings.TrimSpace(cfg.KeyID) == "" || strings.TrimSpace(cfg.Secret) == "" {
		return nil, errors.New("ключ доступа не задан")
	}

	endpoint := strings.TrimSpace(cfg.Endpoint)
	// Схему дописываем сами: человек копирует адрес из панели хостера, и
	// там он бывает без неё. https, а не http: копия едет через интернет.
	//
	// Вписанный руками `http://` при этом принимается — MinIO в своей сети
	// законен, — но не молча: [Client.Insecure] доводит это до экрана копий.
	if !strings.Contains(endpoint, "://") {
		endpoint = "https://" + endpoint
	}

	parsed, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("адрес хранилища: %w", err)
	}
	if parsed.Host == "" {
		return nil, errors.New("в адресе хранилища нет имени сервера")
	}

	if cfg.Region == "" {
		cfg.Region = defaultRegion
	}

	client := &Client{
		cfg:    cfg,
		http:   &http.Client{Timeout: requestTimeout},
		scheme: parsed.Scheme,
		host:   parsed.Host,
		base:   strings.TrimSuffix(parsed.Path, "/"),
	}
	client.pathStyle = pathStyleFor(cfg.PathStyle, parsed.Host)

	return client, nil
}

// pathStyleFor решает, как адресовать бакет.
//
// Спрашивать это у человека нельзя: «путём или поддоменом» — вопрос, на
// который он не обязан знать ответ, а ошибка в нём выглядит как «ключ не
// подошёл». AWS объявил адресацию путём устаревшей, а B2, R2, Selectel и
// MinIO именно её и понимают всегда, — отсюда правило.
func pathStyleFor(setting, host string) bool {
	switch strings.ToLower(strings.TrimSpace(setting)) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}

	name := host
	if index := strings.IndexByte(name, ':'); index >= 0 {
		name = name[:index]
	}
	return !strings.HasSuffix(strings.ToLower(name), "amazonaws.com")
}

// Endpoint — адрес хранилища, как его видит человек. Для экрана: секретов в
// нём нет, а «куда именно уехала копия» — первый вопрос при разборе.
func (c *Client) Endpoint() string { return c.scheme + "://" + c.host + c.base }

// Insecure — копия едет в бакет открытым каналом.
//
// Запрета здесь нет и быть не должно: MinIO в своей сети по `http` законен,
// и спринт 35 так и проверялся. Но копия не шифруется, и допустимо это ровно
// потому, что канал до бакета защищён, — в дампе лежит вся аналитика
// клиента. Молча принять `http://` значит снять вторую половину этого
// рассуждения, никому об этом не сказав. Отсюда признак: экран копий
// называет вещи своими именами, а выбор остаётся за человеком.
func (c *Client) Insecure() bool { return c.scheme == "http" }

// Bucket — имя бакета.
func (c *Client) Bucket() string { return c.cfg.Bucket }

// url собирает адрес объекта.
func (c *Client) url(key string) (host, path string) {
	if c.pathStyle {
		return c.host, c.base + "/" + c.cfg.Bucket + "/" + key
	}
	return c.cfg.Bucket + "." + c.host, c.base + "/" + key
}

// Put кладёт объект.
//
// Длина и суммы известны заранее: архив собран в файл, и сумму мы считаем,
// пока его проверяем. Content-MD5 хранилище проверяет само и отказывает при
// расхождении — это дешевле, чем качать копию обратно ради сверки на трафике
// клиента.
func (c *Client) Put(
	ctx context.Context,
	key string,
	size int64,
	payloadSHA256 string,
	contentMD5 string,
	body io.Reader,
	timeout time.Duration,
) error {
	host, path := c.url(key)

	request, err := http.NewRequestWithContext(ctx, http.MethodPut,
		c.scheme+"://"+host+encodePath(path), body)
	if err != nil {
		return err
	}
	request.ContentLength = size
	request.Header.Set("Content-Type", "application/gzip")
	if contentMD5 != "" {
		request.Header.Set("Content-MD5", contentMD5)
	}

	c.sign(request, payloadSHA256, time.Now().UTC())

	// Выгрузка идёт своим таймаутом: копия в гигабайт по каналу VPS —
	// это минуты, а общий таймаут клиента считан на короткие запросы.
	uploader := &http.Client{Timeout: timeout}
	response, err := uploader.Do(request)
	if err != nil {
		return err
	}
	defer drain(response)

	return statusError(response)
}

// Head спрашивает, лежит ли объект и какого он размера.
func (c *Client) Head(ctx context.Context, key string) (Object, error) {
	host, path := c.url(key)

	request, err := http.NewRequestWithContext(ctx, http.MethodHead,
		c.scheme+"://"+host+encodePath(path), nil)
	if err != nil {
		return Object{}, err
	}
	c.sign(request, emptyPayload, time.Now().UTC())

	response, err := c.http.Do(request)
	if err != nil {
		return Object{}, err
	}
	defer drain(response)

	if err := statusError(response); err != nil {
		return Object{}, err
	}

	object := Object{Key: key}
	if size, err := strconv.ParseInt(response.Header.Get("Content-Length"), 10, 64); err == nil {
		object.Size = size
	}
	if stamp, err := http.ParseTime(response.Header.Get("Last-Modified")); err == nil {
		object.Modified = stamp.UTC()
	}
	return object, nil
}

// listResult — ответ ListObjectsV2.
type listResult struct {
	XMLName     xml.Name `xml:"ListBucketResult"`
	IsTruncated bool     `xml:"IsTruncated"`
	NextToken   string   `xml:"NextContinuationToken"`
	Contents    []struct {
		Key          string `xml:"Key"`
		Size         int64  `xml:"Size"`
		LastModified string `xml:"LastModified"`
	} `xml:"Contents"`
}

// List перечисляет объекты под префиксом, от новых к старым.
//
// Нужен двоим: ротации (что удалить) и свежести (что хранилище само думает о
// наших копиях). Второе важнее: правило жизненного цикла в бакете, стирающее
// объекты через неделю, — обычная настройка, и наша запись «копия сделана
// вчера» её не заметит никогда.
func (c *Client) List(ctx context.Context, prefix string, limit int) ([]Object, error) {
	var objects []Object
	token := ""

	for {
		query := url.Values{}
		query.Set("list-type", "2")
		if prefix != "" {
			query.Set("prefix", prefix)
		}
		if token != "" {
			query.Set("continuation-token", token)
		}

		host, path := c.url("")
		path = strings.TrimSuffix(path, "/")
		if path == "" {
			path = "/"
		}

		request, err := http.NewRequestWithContext(ctx, http.MethodGet,
			c.scheme+"://"+host+encodePath(path)+"?"+encodeQuery(query), nil)
		if err != nil {
			return nil, err
		}
		c.sign(request, emptyPayload, time.Now().UTC())

		response, err := c.http.Do(request)
		if err != nil {
			return nil, err
		}

		if err := statusError(response); err != nil {
			drain(response)
			return nil, err
		}

		var result listResult
		err = xml.NewDecoder(io.LimitReader(response.Body, 8<<20)).Decode(&result)
		drain(response)
		if err != nil {
			return nil, fmt.Errorf("список объектов: %w", err)
		}

		for _, item := range result.Contents {
			object := Object{Key: item.Key, Size: item.Size}
			if stamp, err := time.Parse(time.RFC3339, item.LastModified); err == nil {
				object.Modified = stamp.UTC()
			}
			objects = append(objects, object)
		}

		// Потолок применяется ПОСЛЕ сортировки, а не здесь.
		//
		// S3 отдаёт ключи по возрастанию, а ключ начинается со времени: на
		// первой странице лежат самые старые копии. Оборвав чтение по
		// счётчику, мы отсортировали бы старьё и назвали его последними
		// копиями — экран показывал бы прошлогоднюю копию как свежую, а
		// «свежесть спрашивается у бакета» перестало бы что-либо значить.
		// Найдено ревизией трека F.
		//
		// Страниц поэтому читаем столько, сколько есть, но не бесконечно:
		// бакет без ротации (`BACKUP_KEEP=0` — законный выбор) растёт
		// годами, и держать его весь в памяти агента на чужом сервере
		// незачем. Упёрлись в потолок страниц — берём что прочитали: это
		// по-прежнему самые старые, но ошибиться уже негде — до такого
		// числа копий доходит только тот, кто отключил ротацию сам.
		if !result.IsTruncated || result.NextToken == "" || len(objects) >= maxListed {
			break
		}
		token = result.NextToken
	}

	sort.Slice(objects, func(i, j int) bool {
		return objects[i].Key > objects[j].Key
	})

	if limit > 0 && len(objects) > limit {
		objects = objects[:limit]
	}

	return objects, nil
}

// Сколько объектов вычитываем, прежде чем остановиться.
//
// Десять тысяч — это годы суточных копий без ротации. Память под них —
// сотни килобайт, и это потолок, а не расчёт: он существует затем, чтобы у
// цикла был конец.
const maxListed = 10000

// Delete убирает объект. Ротация — единственное, что мы удаляем в чужом
// бакете, и только под своим префиксом.
func (c *Client) Delete(ctx context.Context, key string) error {
	host, path := c.url(key)

	request, err := http.NewRequestWithContext(ctx, http.MethodDelete,
		c.scheme+"://"+host+encodePath(path), nil)
	if err != nil {
		return err
	}
	c.sign(request, emptyPayload, time.Now().UTC())

	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer drain(response)

	return statusError(response)
}

// Presign выдаёт ссылку, по которой объект можно скачать без наших ключей.
//
// Ею восстановление и живёт: приложение не умеет говорить с хранилищем и не
// должно — второй клиент S3, на Rust, был бы второй реализацией одного
// протокола, и разошлась бы та, про которую забыли. Сервер клиента скачивает
// копию сам, обычным curl.
//
// Срок короткий: в ссылке подпись, дающая доступ к аналитике клиента.
func (c *Client) Presign(key string, ttl time.Duration) (string, error) {
	if ttl <= 0 || ttl > 12*time.Hour {
		return "", errors.New("срок ссылки вне допустимого")
	}

	now := time.Now().UTC()
	stamp := now.Format("20060102T150405Z")
	scope := now.Format("20060102") + "/" + c.cfg.Region + "/s3/aws4_request"

	host, path := c.url(key)

	query := url.Values{}
	query.Set("X-Amz-Algorithm", "AWS4-HMAC-SHA256")
	query.Set("X-Amz-Credential", c.cfg.KeyID+"/"+scope)
	query.Set("X-Amz-Date", stamp)
	query.Set("X-Amz-Expires", strconv.Itoa(int(ttl.Seconds())))
	query.Set("X-Amz-SignedHeaders", "host")

	canonical := strings.Join([]string{
		http.MethodGet,
		encodePath(path),
		encodeQuery(query),
		"host:" + host + "\n",
		"host",
		// Тело не подписывается: его нет, а сервер клиента будет качать
		// объект обычным GET.
		"UNSIGNED-PAYLOAD",
	}, "\n")

	signature := c.signature(now, canonical)
	query.Set("X-Amz-Signature", signature)

	return c.scheme + "://" + host + encodePath(path) + "?" + encodeQuery(query), nil
}

// ── Подпись ──────────────────────────────────────────────────────────────

// Пустое тело: sha256 от нуля байт. Константой, а не пересчётом на каждый
// запрос.
const emptyPayload = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

// sign подписывает запрос по SigV4.
func (c *Client) sign(request *http.Request, payloadSHA256 string, now time.Time) {
	stamp := now.Format("20060102T150405Z")

	request.Header.Set("X-Amz-Date", stamp)
	request.Header.Set("X-Amz-Content-Sha256", payloadSHA256)
	request.Host = request.URL.Host

	headers := []string{"host", "x-amz-content-sha256", "x-amz-date"}
	values := map[string]string{
		"host":                 request.URL.Host,
		"x-amz-content-sha256": payloadSHA256,
		"x-amz-date":           stamp,
	}
	if md5sum := request.Header.Get("Content-MD5"); md5sum != "" {
		headers = append(headers, "content-md5")
		values["content-md5"] = md5sum
	}
	if kind := request.Header.Get("Content-Type"); kind != "" {
		headers = append(headers, "content-type")
		values["content-type"] = kind
	}
	sort.Strings(headers)

	var canonicalHeaders strings.Builder
	for _, name := range headers {
		canonicalHeaders.WriteString(name)
		canonicalHeaders.WriteString(":")
		canonicalHeaders.WriteString(strings.TrimSpace(values[name]))
		canonicalHeaders.WriteString("\n")
	}

	query := request.URL.Query()

	canonical := strings.Join([]string{
		request.Method,
		encodePath(request.URL.Path),
		encodeQuery(query),
		canonicalHeaders.String(),
		strings.Join(headers, ";"),
		payloadSHA256,
	}, "\n")

	scope := now.Format("20060102") + "/" + c.cfg.Region + "/s3/aws4_request"
	request.Header.Set("Authorization", strings.Join([]string{
		"AWS4-HMAC-SHA256 Credential=" + c.cfg.KeyID + "/" + scope,
		"SignedHeaders=" + strings.Join(headers, ";"),
		"Signature=" + c.signature(now, canonical),
	}, ", "))
}

// signature считает подпись по каноническому запросу.
func (c *Client) signature(now time.Time, canonical string) string {
	date := now.Format("20060102")
	scope := date + "/" + c.cfg.Region + "/s3/aws4_request"

	sum := sha256.Sum256([]byte(canonical))
	toSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		now.Format("20060102T150405Z"),
		scope,
		hex.EncodeToString(sum[:]),
	}, "\n")

	key := hmacSum([]byte("AWS4"+c.cfg.Secret), date)
	key = hmacSum(key, c.cfg.Region)
	key = hmacSum(key, "s3")
	key = hmacSum(key, "aws4_request")

	return hex.EncodeToString(hmacSum(key, toSign))
}

func hmacSum(key []byte, data string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(data))
	return mac.Sum(nil)
}

// encodePath кодирует путь так, как того требует подпись: каждый сегмент
// отдельно, косые черты остаются на месте.
//
// `url.URL.EscapedPath` для этого не годится: он оставляет неэкранированными
// символы, которые S3 в подписи ждёт экранированными, и подпись расходится
// на ровном месте — с ответом «ключ не подошёл», который уводит разбор
// совсем не туда.
func encodePath(path string) string {
	if path == "" {
		return "/"
	}
	parts := strings.Split(path, "/")
	for i, part := range parts {
		parts[i] = encodeSegment(part)
	}
	joined := strings.Join(parts, "/")
	if !strings.HasPrefix(joined, "/") {
		joined = "/" + joined
	}
	return joined
}

// encodeSegment — RFC 3986 без исключений: пробел это %20, а не плюс.
func encodeSegment(segment string) string {
	var out strings.Builder
	for _, symbol := range []byte(segment) {
		switch {
		case symbol >= 'A' && symbol <= 'Z',
			symbol >= 'a' && symbol <= 'z',
			symbol >= '0' && symbol <= '9',
			symbol == '-', symbol == '_', symbol == '.', symbol == '~':
			out.WriteByte(symbol)
		default:
			out.WriteString(fmt.Sprintf("%%%02X", symbol))
		}
	}
	return out.String()
}

// encodeQuery собирает строку запроса в каноническом виде: параметры
// отсортированы, значения экранированы по тем же правилам.
func encodeQuery(query url.Values) string {
	keys := make([]string, 0, len(query))
	for key := range query {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var parts []string
	for _, key := range keys {
		values := append([]string(nil), query[key]...)
		sort.Strings(values)
		for _, value := range values {
			parts = append(parts, encodeSegment(key)+"="+encodeSegment(value))
		}
	}
	return strings.Join(parts, "&")
}

// ── Ответы ───────────────────────────────────────────────────────────────

// statusError превращает отказ хранилища в ошибку с его кодом.
func statusError(response *http.Response) error {
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		return nil
	}

	failure := &Error{Status: response.StatusCode}

	// Тело ошибки у S3 — XML, но у совместимых бывает и пустым (HEAD его не
	// возвращает вовсе). Пустое тело — не повод потерять код ответа.
	var payload struct {
		Code    string `xml:"Code"`
		Message string `xml:"Message"`
	}
	if err := xml.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&payload); err == nil {
		failure.Code = payload.Code
		failure.Msg = payload.Message
	}

	return failure
}

// drain дочитывает и закрывает тело: без этого соединение не переиспользуется.
func drain(response *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
	_ = response.Body.Close()
}
