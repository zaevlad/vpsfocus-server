// bots-build собирает список роботов и диапазоны их адресов для агента
// (трек AI, спринт 271, Р1, Р29–Р32):
//
//	cd agent
//	go run ./cmd/bots-build            # пишет internal/bots/data/
//	go run ./cmd/bots-build -allow-shrink bingbot,ahrefs
//
// Запускается руками перед выпуском агента; результат коммитится, CI его
// только вшивает — так образ из тега повторяется. На сервер клиента
// сборщик не попадает: Dockerfile собирает только cmd/agent.
//
// Отказывает громко, а не пишет урезанное: сменилась метка лицензии
// источника, не разобралась запись, у робота нет роли, диапазон шире пола,
// набор опустел или сжался больше чем вдвое против прошлой сборки.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"agent/internal/bots"
)

func main() {
	out := flag.String("out", filepath.Join("internal", "bots", "data"), "куда писать")
	allow := flag.String("allow-shrink", "", "наборы через запятую, которым можно сжаться больше чем вдвое")
	flag.Parse()

	allowShrink := map[string]bool{}
	for _, id := range strings.Split(*allow, ",") {
		if id = strings.TrimSpace(id); id != "" {
			allowShrink[id] = true
		}
	}
	if err := run(context.Background(), *out, allowShrink); err != nil {
		fmt.Fprintln(os.Stderr, "bots-build:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, out string, allowShrink map[string]bool) error {
	built := time.Now().UTC().Truncate(24 * time.Hour)
	if !postHogOutsideEE() {
		return errors.New("posthog: файл определений под ee/ — там не MIT")
	}

	files := map[string][]byte{}
	licenses := map[string][]byte{}
	sources := []bots.Source{}
	for _, source := range nameSources {
		license, err := fetch(ctx, source.url(source.license))
		if err != nil {
			return err
		}
		if !source.licenseMark(string(license)) {
			return fmt.Errorf("%s: метка лицензии не сошлась — прочитать LICENSE при коммите %s", source.id, source.commit)
		}
		licenseFile := "LICENSE-" + source.id
		licenses[licenseFile] = license
		for _, file := range source.files {
			body, err := fetch(ctx, source.url(file))
			if err != nil {
				return err
			}
			files[source.id+"/"+file] = body
		}
		sources = append(sources, bots.Source{ID: source.id, URL: "https://github.com/" + source.repo,
			Commit: source.commit, License: licenseFile})
	}

	all := ownCandidates()
	aiRobots, err := parseAIRobots(files["ai-robots-txt/robots.json"])
	if err != nil {
		return err
	}
	postHog, err := parsePostHog(files["posthog/"+postHogFile])
	if err != nil {
		return err
	}
	botscent, err := parseBotscent(files["botscent/registry/tokens.json"], files["botscent/registry/names.json"])
	if err != nil {
		return err
	}
	all = append(all, aiRobots...)
	all = append(all, postHog...)
	all = append(all, botscent...)
	robots, err := merge(all)
	if err != nil {
		return err
	}

	sets := []bots.Set{}
	for _, source := range rangeSources {
		body, err := fetch(ctx, source.url)
		if err != nil {
			return err
		}
		set, err := buildSet(source, body, built)
		if err != nil {
			return err
		}
		if set.Names, err = resolveNames(source.tokens, robots); err != nil {
			return fmt.Errorf("%s: %w", source.id, err)
		}
		sets = append(sets, set)
	}
	previous, err := readPrevious(filepath.Join(out, "ranges.json"))
	if err != nil {
		return err
	}
	if err := checkSets(sets, previous, allowShrink); err != nil {
		return err
	}

	cloudflare := []string{}
	for _, url := range cloudflareURLs {
		body, err := fetch(ctx, url)
		if err != nil {
			return err
		}
		cloudflare = append(cloudflare, parseLines(body)...)
	}
	cfV4, cfV6, err := normalize(cloudflare, false)
	if err != nil {
		return fmt.Errorf("cloudflare: %w", err)
	}
	if len(cfV4) == 0 || len(cfV6) == 0 {
		return errors.New("cloudflare: пустой список")
	}

	stamp := built.Format("2006-01-02")
	catalog := bots.Catalog{Built: stamp, Sources: sources, Robots: robots}
	ranges := bots.Ranges{Built: stamp, Sets: sets,
		Cloudflare: bots.Proxy{URLs: cloudflareURLs, V4: cfV4, V6: cfV6}}

	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(out, "robots.json"), catalog); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(out, "ranges.json"), ranges); err != nil {
		return err
	}
	for name, body := range licenses {
		if err := os.WriteFile(filepath.Join(out, name), body, 0o644); err != nil {
			return err
		}
	}
	report(catalog, ranges)
	return nil
}

func readPrevious(path string) (*bots.Ranges, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var previous bots.Ranges
	if err := json.Unmarshal(raw, &previous); err != nil {
		return nil, fmt.Errorf("прошлая сборка %s: %w", path, err)
	}
	return &previous, nil
}

func writeJSON(path string, value any) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0o644)
}

// report — коротко, что собралось: глазами сверить с прошлой сборкой.
func report(catalog bots.Catalog, ranges bots.Ranges) {
	roles := map[string]int{}
	for _, robot := range catalog.Robots {
		roles[robot.Role]++
	}
	fmt.Printf("роботов %d:", len(catalog.Robots))
	for _, role := range bots.Roles {
		fmt.Printf(" %s %d", role, roles[role])
	}
	fmt.Println()
	for _, set := range ranges.Sets {
		stale := ""
		if set.Stale {
			stale = " — старше 12 месяцев, имена «не проверить»"
		}
		fmt.Printf("  %-22s %4d префиксов, файл от %s (%s)%s\n", set.ID, len(set.V4)+len(set.V6), set.Created, set.CreatedFrom, stale)
	}
	fmt.Printf("  cloudflare            %d + %d\n", len(ranges.Cloudflare.V4), len(ranges.Cloudflare.V6))
}
