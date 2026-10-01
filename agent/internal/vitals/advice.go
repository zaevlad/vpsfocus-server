package vitals

import (
	"encoding/json"
	"math"
	"regexp"
	"sort"
	"strings"

	"agent/internal/text"
)

// Подробности замера (трек P «Производительность подробно»): остальные категории
// Lighthouse, советы «что исправить», вес страницы и время ответа сервера.
// Всё это приходит в том же ответе PSI, что и оценка, — лишнего обращения к
// API и лишней квоты не стоит.
//
// Формы сверены с живым ответом Lighthouse 13.5.0 для getgliph.com
// (`testdata/psi-getgliph-mobile.json`), а не взяты из документации.

// Category — оценка одной категории Lighthouse.
type Category struct {
	// performance, accessibility, best-practices, seo.
	ID string `json:"id"`
	// Название на языке агента — Lighthouse переводит его сам.
	Title  string `json:"title"`
	Score  int    `json:"score"`
	Rating string `json:"rating"`
}

// Advice — совет Lighthouse: что не так и сколько это стоит.
type Advice struct {
	ID       string `json:"id"`
	Category string `json:"category"`
	Title    string `json:"title"`
	// «Ожидаемая экономия – 348 КиБ» — строкой Lighthouse, уже переведённой.
	DisplayValue string `json:"displayValue,omitempty"`
	// Первый абзац объяснения без ссылок-разметки.
	Description string `json:"description,omitempty"`
	// Оценка проверки 0–1: чем ниже, тем хуже.
	Score float64 `json:"score"`
	// Сколько миллисекунд LCP, FCP или TBT можно выиграть — по оценке
	// Lighthouse. Ноль у советов, которые время не считают.
	SavingsMs float64 `json:"savingsMs,omitempty"`
	// Где именно: файлы или элементы страницы, самые тяжёлые первыми.
	Items []AdviceItem `json:"items,omitempty"`
}

// AdviceItem — файл или элемент страницы, к которому относится совет.
type AdviceItem struct {
	Label string  `json:"label"`
	Bytes float64 `json:"bytes,omitempty"`
	Ms    float64 `json:"ms,omitempty"`
}

// Resource — сколько страница тянет файлов одного типа и какого веса.
type Resource struct {
	// script, image, font, stylesheet, document, media, other.
	Type string `json:"type"`
	// Название на языке агента.
	Label    string `json:"label"`
	Requests int    `json:"requests"`
	Bytes    int64  `json:"bytes"`
}

// Пороги времени до интерактивности — Lighthouse для мобильных.
const ttiGood, ttiPoor = 3800.0, 7300.0

// Сколько советов хранить на категорию и сколько мест на совет. Отчёт
// ложится в базу агента на сервере клиента вместе с историей замеров.
const (
	advicePerCategory = 12
	itemsPerAdvice    = 5
	labelMax          = 200
	descriptionMax    = 400
)

// Порядок категорий на экране и в запросе к PSI.
var categoryOrder = []string{"performance", "accessibility", "best-practices", "seo"}

type psiCategory struct {
	Title     string   `json:"title"`
	Score     *float64 `json:"score"`
	AuditRefs []struct {
		ID     string  `json:"id"`
		Weight float64 `json:"weight"`
		Group  string  `json:"group"`
	} `json:"auditRefs"`
}

type psiAudit struct {
	Title            string             `json:"title"`
	Description      string             `json:"description"`
	DisplayValue     string             `json:"displayValue"`
	Score            *float64           `json:"score"`
	ScoreDisplayMode string             `json:"scoreDisplayMode"`
	NumericValue     *float64           `json:"numericValue"`
	MetricSavings    map[string]float64 `json:"metricSavings"`
	Details          *struct {
		Type string `json:"type"`
		// Сырым: у проверок вида checklist `items` — объект, а не массив,
		// и жёсткий разбор уронил бы весь ответ из-за одной проверки.
		Items json.RawMessage `json:"items"`
	} `json:"details"`
}

// psiItem — строка таблицы деталей. Поля у проверок разные; берём те, что
// называют место и цену.
type psiItem struct {
	URL         string   `json:"url"`
	ScriptURL   string   `json:"scriptUrl"`
	WastedBytes *float64 `json:"wastedBytes"`
	WastedMs    *float64 `json:"wastedMs"`
	Node        *struct {
		NodeLabel string `json:"nodeLabel"`
		Snippet   string `json:"snippet"`
	} `json:"node"`
	Source *struct {
		URL string `json:"url"`
	} `json:"source"`

	// resource-summary.
	ResourceType string  `json:"resourceType"`
	Label        string  `json:"label"`
	RequestCount int     `json:"requestCount"`
	TransferSize float64 `json:"transferSize"`
}

func (a psiAudit) items() []psiItem {
	if a.Details == nil || len(a.Details.Items) == 0 || a.Details.Items[0] != '[' {
		return nil
	}
	var items []psiItem
	if err := json.Unmarshal(a.Details.Items, &items); err != nil {
		return nil
	}
	return items
}

// categories — оценки всех категорий, что вернул Lighthouse, в своём порядке.
func categories(raw map[string]psiCategory) []Category {
	var out []Category
	for _, id := range categoryOrder {
		category, ok := raw[id]
		if !ok || category.Score == nil {
			continue
		}
		score := int(math.Round(*category.Score * 100))
		out = append(out, Category{
			ID:     id,
			Title:  category.Title,
			Score:  score,
			Rating: RateScore(score),
		})
	}
	return out
}

// Группы, в которых нет советов: сами метрики и скрытое Lighthouse.
var notAdviceGroups = map[string]bool{"metrics": true, "hidden": true}

// Режимы, в которых оценки нет или она не про сайт.
var notAdviceModes = map[string]bool{
	"notApplicable": true,
	"manual":        true,
	"informative":   true,
	"error":         true,
}

// advice — проваленные и полупроваленные проверки всех категорий.
//
// Порог — 0.9, как у Lighthouse: ниже он сам рисует проверку жёлтой или
// красной. В производительности первыми идут советы с большим выигрышем по
// времени; в остальных — самые весомые для оценки.
func advice(rawCategories map[string]psiCategory, audits map[string]psiAudit) []Advice {
	var out []Advice
	seen := map[string]bool{}

	for _, categoryID := range categoryOrder {
		category, ok := rawCategories[categoryID]
		if !ok {
			continue
		}

		type candidate struct {
			advice Advice
			weight float64
		}
		var found []candidate

		for _, ref := range category.AuditRefs {
			if notAdviceGroups[ref.Group] || seen[ref.ID] {
				continue
			}
			audit, ok := audits[ref.ID]
			if !ok || audit.Score == nil || *audit.Score >= 0.9 || notAdviceModes[audit.ScoreDisplayMode] {
				continue
			}
			seen[ref.ID] = true
			found = append(found, candidate{
				advice: Advice{
					ID:           ref.ID,
					Category:     categoryID,
					Title:        text.Truncate(audit.Title, labelMax),
					DisplayValue: text.Truncate(audit.DisplayValue, labelMax),
					Description:  cleanDescription(audit.Description),
					Score:        *audit.Score,
					SavingsMs:    savingsMs(audit.MetricSavings),
					Items:        adviceItems(audit.items()),
				},
				weight: ref.Weight,
			})
		}

		sort.SliceStable(found, func(i, j int) bool {
			a, b := found[i], found[j]
			if a.advice.SavingsMs != b.advice.SavingsMs {
				return a.advice.SavingsMs > b.advice.SavingsMs
			}
			if a.weight != b.weight {
				return a.weight > b.weight
			}
			return a.advice.Score < b.advice.Score
		})
		for index, item := range found {
			if index == advicePerCategory {
				break
			}
			out = append(out, item.advice)
		}
	}
	return out
}

// savingsMs — наибольший выигрыш по времени. CLS безразмерный и в
// миллисекунды не складывается, поэтому не считается.
func savingsMs(savings map[string]float64) float64 {
	best := 0.0
	for metric, value := range savings {
		if metric == "CLS" {
			continue
		}
		best = math.Max(best, value)
	}
	return best
}

// adviceItems — места, к которым относится совет, самые дорогие первыми.
func adviceItems(items []psiItem) []AdviceItem {
	var out []AdviceItem
	for _, item := range items {
		label := ""
		switch {
		case item.Node != nil && strings.TrimSpace(item.Node.NodeLabel) != "":
			label = item.Node.NodeLabel
		case item.Node != nil && item.Node.Snippet != "":
			label = item.Node.Snippet
		case item.URL != "":
			label = item.URL
		case item.ScriptURL != "":
			label = item.ScriptURL
		case item.Source != nil && item.Source.URL != "":
			label = item.Source.URL
		}
		label = strings.TrimSpace(label)
		if label == "" {
			continue
		}
		entry := AdviceItem{Label: text.Truncate(label, labelMax)}
		if item.WastedBytes != nil {
			entry.Bytes = *item.WastedBytes
		}
		if item.WastedMs != nil {
			entry.Ms = *item.WastedMs
		}
		out = append(out, entry)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Ms != out[j].Ms {
			return out[i].Ms > out[j].Ms
		}
		return out[i].Bytes > out[j].Bytes
	})
	if len(out) > itemsPerAdvice {
		out = out[:itemsPerAdvice]
	}
	return out
}

var markdownLink = regexp.MustCompile(`\[([^\]]+)\]\([^)]*\)`)

// Концовка «Подробнее…» / «Learn more…» — ссылка на документацию Google,
// которую приложение открыть не может.
var learnMore = regexp.MustCompile(`(?i)\s*(подробнее|узнайте больше|learn more|learn how)[^.]*\.?\s*$`)

// cleanDescription — объяснение без разметки ссылок и без «подробнее».
func cleanDescription(raw string) string {
	cleaned := markdownLink.ReplaceAllString(raw, "$1")
	cleaned = learnMore.ReplaceAllString(cleaned, "")
	cleaned = strings.Join(strings.Fields(cleaned), " ")
	return text.Truncate(cleaned, descriptionMax)
}

// resources — разбивка веса страницы по типам файлов, без итога и без
// «сторонних»: те уже учтены в своих типах, и сложение дало бы больше, чем
// страница весит.
func resources(summary []psiItem) (list []Resource, totalBytes int64, requests int) {
	for _, item := range summary {
		switch item.ResourceType {
		case "total":
			totalBytes = int64(item.TransferSize)
			requests = item.RequestCount
		case "third-party", "":
		default:
			if item.RequestCount == 0 {
				continue
			}
			list = append(list, Resource{
				Type:     item.ResourceType,
				Label:    item.Label,
				Requests: item.RequestCount,
				Bytes:    int64(item.TransferSize),
			})
		}
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].Bytes > list[j].Bytes })
	return list, totalBytes, requests
}
