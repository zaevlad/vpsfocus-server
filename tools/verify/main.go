// Command verify checks a vpsFocus server-code release.
//
// It is the same check the CI runs on every bundle-* tag, and anyone can run
// it on their own machine:
//
//	go run ./tools/verify                                  # every version in bundle/
//	go run ./tools/verify -version 0.27.0                  # one version
//	go run ./tools/verify -version 0.27.0 -archive bundle-0.27.0.tar.gz
//
// What it checks for each version in bundle/<version>/:
//
//  1. bundle.sig is a valid minisign signature of bundle.json made with the
//     release key in keys/release.pub;
//  2. bundle.json (the signed inventory) lists exactly the files of the
//     version directory, each with the right SHA-256;
//  3. every image in docker-compose.yml.tpl is pinned by digest;
//  4. with -archive: the archive you downloaded (or the one the vpsFocus app
//     cached) contains exactly these files, byte for byte, with the modes
//     from the inventory.
//
// And once for the repository: SERVER-SIDE.md names every script in
// scripts/.
//
// Image provenance (which commit an image digest was built from) is checked
// separately with `gh attestation verify`; see README.
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"golang.org/x/crypto/blake2b"
)

func main() {
	repo := flag.String("repo", ".", "root of the vpsfocus-server checkout")
	version := flag.String("version", "", "bundle version to check; empty — every version")
	archive := flag.String("archive", "", "downloaded bundle archive (.tar.gz) to compare with -version")
	flag.Parse()

	failures := run(*repo, *version, *archive)
	if len(failures) > 0 {
		fmt.Println()
		for _, failure := range failures {
			fmt.Println("FAIL:", failure)
		}
		os.Exit(1)
	}
	fmt.Println("\nOK")
}

func run(repo, version, archive string) []string {
	var failures []string
	fail := func(format string, args ...any) {
		failures = append(failures, fmt.Sprintf(format, args...))
	}

	key, err := readPublicKey(filepath.Join(repo, "keys", "release.pub"))
	if err != nil {
		return []string{err.Error()}
	}
	fmt.Printf("release key %s\n", strings.ToUpper(hex.EncodeToString(reverse(key.id[:]))))

	versions := []string{version}
	if version == "" {
		if archive != "" {
			return []string{"-archive needs -version"}
		}
		versions, err = listVersions(filepath.Join(repo, "bundle"))
		if err != nil {
			return []string{err.Error()}
		}
	}

	for _, v := range versions {
		fmt.Printf("\nbundle %s\n", v)
		for _, problem := range checkVersion(key, filepath.Join(repo, "bundle", v), v, archive) {
			fail("bundle %s: %s", v, problem)
		}
	}

	for _, problem := range checkInventoryOfScripts(repo) {
		fail("%s", problem)
	}
	return failures
}

// ── minisign ──────────────────────────────────────────────────────────────

type publicKey struct {
	id  [8]byte
	key ed25519.PublicKey
}

// readPublicKey reads a minisign public key file: a comment line and a base64
// line of "Ed" + key id (8 bytes) + Ed25519 key (32 bytes).
func readPublicKey(file string) (publicKey, error) {
	raw, err := os.ReadFile(file)
	if err != nil {
		return publicKey{}, fmt.Errorf("release key: %w", err)
	}
	line := lastLine(string(raw))
	data, err := base64.StdEncoding.DecodeString(line)
	if err != nil || len(data) != 42 || string(data[:2]) != "Ed" {
		return publicKey{}, fmt.Errorf("release key %s: not a minisign public key", file)
	}
	var key publicKey
	copy(key.id[:], data[2:10])
	key.key = ed25519.PublicKey(data[10:42])
	return key, nil
}

// verifySignature checks a minisign signature of message. The vpsFocus
// bundle.sig is the minisign signature file encoded in base64 once more (the
// format of the Tauri signer); a plain minisign file is accepted too.
func verifySignature(key publicKey, message, sigFile []byte) (string, error) {
	text := string(sigFile)
	if !strings.HasPrefix(strings.TrimSpace(text), "untrusted comment:") {
		decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(text))
		if err != nil {
			return "", errors.New("bundle.sig: neither minisign text nor base64 of it")
		}
		text = string(decoded)
	}

	lines := strings.Split(strings.ReplaceAll(text, "\r", ""), "\n")
	if len(lines) < 4 || !strings.HasPrefix(lines[2], "trusted comment: ") {
		return "", errors.New("bundle.sig: unexpected layout")
	}

	sig, err := base64.StdEncoding.DecodeString(lines[1])
	if err != nil || len(sig) != 74 {
		return "", errors.New("bundle.sig: bad signature line")
	}
	algorithm, id, signature := string(sig[:2]), sig[2:10], sig[10:74]
	if !bytes.Equal(id, key.id[:]) {
		return "", fmt.Errorf("bundle.sig: signed by key %s, not by the release key",
			strings.ToUpper(hex.EncodeToString(reverse(id))))
	}

	signed := message
	switch algorithm {
	case "ED":
		digest := blake2b.Sum512(message)
		signed = digest[:]
	case "Ed":
	default:
		return "", fmt.Errorf("bundle.sig: unknown algorithm %q", algorithm)
	}
	if !ed25519.Verify(key.key, signed, signature) {
		return "", errors.New("bundle.sig: signature does not match bundle.json")
	}

	trusted := strings.TrimPrefix(lines[2], "trusted comment: ")
	global, err := base64.StdEncoding.DecodeString(lines[3])
	if err != nil || len(global) != 64 {
		return "", errors.New("bundle.sig: bad trusted comment signature")
	}
	if !ed25519.Verify(key.key, append(append([]byte{}, signature...), trusted...), global) {
		return "", errors.New("bundle.sig: trusted comment was changed")
	}
	return trusted, nil
}

// ── one version ───────────────────────────────────────────────────────────

type inventory struct {
	Version string `json:"version"`
	Files   []struct {
		Path   string `json:"path"`
		Mode   string `json:"mode"`
		SHA256 string `json:"sha256"`
	} `json:"files"`
}

var reserved = map[string]bool{"bundle.json": true, "bundle.sig": true}

func checkVersion(key publicKey, dir, version, archive string) []string {
	var problems []string

	manifest, err := os.ReadFile(filepath.Join(dir, "bundle.json"))
	if err != nil {
		return []string{err.Error()}
	}
	sig, err := os.ReadFile(filepath.Join(dir, "bundle.sig"))
	if err != nil {
		return []string{err.Error()}
	}

	trusted, err := verifySignature(key, manifest, sig)
	if err != nil {
		problems = append(problems, err.Error())
	} else {
		fmt.Printf("  signature   ok (%s)\n", trusted)
	}

	var listed inventory
	if err := json.Unmarshal(manifest, &listed); err != nil {
		return append(problems, "bundle.json: "+err.Error())
	}
	if listed.Version != version {
		problems = append(problems, fmt.Sprintf("bundle.json says version %q", listed.Version))
	}

	files, err := filesUnder(dir)
	if err != nil {
		return append(problems, err.Error())
	}
	want := map[string]string{}
	modes := map[string]string{}
	for _, file := range listed.Files {
		want[file.Path] = file.SHA256
		modes[file.Path] = file.Mode
		content, ok := files[file.Path]
		if !ok {
			problems = append(problems, "missing file "+file.Path)
			continue
		}
		if sum := sha256.Sum256(content); hex.EncodeToString(sum[:]) != file.SHA256 {
			problems = append(problems, "SHA-256 differs from the signed inventory: "+file.Path)
		}
	}
	for name := range files {
		if _, ok := want[name]; !ok && !reserved[name] {
			problems = append(problems, "file not in the signed inventory: "+name)
		}
	}
	if len(problems) == 0 {
		fmt.Printf("  inventory   ok (%d files)\n", len(listed.Files))
	}

	problems = append(problems, checkImages(files["docker-compose.yml.tpl"])...)

	if archive != "" {
		problems = append(problems, compareArchive(archive, files, modes)...)
	}
	return problems
}

var imageLine = regexp.MustCompile(`(?m)^\s*image:\s*(\S+)\s*$`)

func checkImages(compose []byte) []string {
	if compose == nil {
		return []string{"docker-compose.yml.tpl is missing"}
	}
	var problems []string
	for _, match := range imageLine.FindAllSubmatch(compose, -1) {
		image := string(match[1])
		if !regexp.MustCompile(`@sha256:[0-9a-f]{64}$`).MatchString(image) {
			problems = append(problems, "image not pinned by digest: "+image)
			continue
		}
		fmt.Printf("  image       %s\n", image)
	}
	return problems
}

func compareArchive(file string, files map[string][]byte, modes map[string]string) []string {
	raw, err := os.ReadFile(file)
	if err != nil {
		return []string{err.Error()}
	}
	sum := sha256.Sum256(raw)
	fmt.Printf("  archive     %s sha256 %s\n", filepath.Base(file), hex.EncodeToString(sum[:]))

	zip, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return []string{"archive: " + err.Error()}
	}
	reader := tar.NewReader(zip)

	var problems []string
	seen := map[string]bool{}
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return append(problems, "archive: "+err.Error())
		}
		if header.Typeflag == tar.TypeDir {
			continue
		}
		name := path.Clean(header.Name)
		seen[name] = true
		content, err := io.ReadAll(reader)
		if err != nil {
			return append(problems, "archive: "+err.Error())
		}
		local, ok := files[name]
		switch {
		case !ok:
			problems = append(problems, "archive has a file the tag does not: "+name)
		case !bytes.Equal(local, content):
			problems = append(problems, "archive file differs from the tag: "+name)
		}
		if mode, ok := modes[name]; ok && fmt.Sprintf("%04o", header.Mode&0o7777) != mode {
			problems = append(problems, fmt.Sprintf("archive mode of %s is %04o, inventory says %s", name, header.Mode&0o7777, mode))
		}
	}
	for name := range files {
		if !seen[name] {
			problems = append(problems, "tag has a file the archive does not: "+name)
		}
	}
	if len(problems) == 0 {
		fmt.Println("  archive     ok: same files, byte for byte")
	}
	return problems
}

// ── repository ────────────────────────────────────────────────────────────

func checkInventoryOfScripts(repo string) []string {
	inventory, err := os.ReadFile(filepath.Join(repo, "SERVER-SIDE.md"))
	if err != nil {
		return []string{err.Error()}
	}
	entries, err := os.ReadDir(filepath.Join(repo, "scripts"))
	if err != nil {
		return []string{err.Error()}
	}
	var problems []string
	count := 0
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".sh") {
			continue
		}
		count++
		if !bytes.Contains(inventory, []byte("`"+entry.Name()+"`")) {
			problems = append(problems, "SERVER-SIDE.md does not name scripts/"+entry.Name())
		}
	}
	if len(problems) == 0 {
		fmt.Printf("\nscripts       ok (%d, each named in SERVER-SIDE.md)\n", count)
	}
	return problems
}

// ── helpers ───────────────────────────────────────────────────────────────

func filesUnder(dir string) (map[string][]byte, error) {
	files := map[string][]byte{}
	err := filepath.WalkDir(dir, func(full string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		rel, err := filepath.Rel(dir, full)
		if err != nil {
			return err
		}
		content, err := os.ReadFile(full)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(rel)] = content
		return nil
	})
	return files, err
}

func listVersions(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var versions []string
	for _, entry := range entries {
		if entry.IsDir() && entry.Name() != "shared" {
			versions = append(versions, entry.Name())
		}
	}
	sort.Strings(versions)
	if len(versions) == 0 {
		return nil, errors.New("no versions in bundle/")
	}
	return versions, nil
}

func lastLine(text string) string {
	lines := strings.Split(strings.TrimSpace(strings.ReplaceAll(text, "\r", "")), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// reverse gives the key id in the order minisign prints it.
func reverse(id []byte) []byte {
	out := make([]byte, len(id))
	for i := range id {
		out[i] = id[len(id)-1-i]
	}
	return out
}
