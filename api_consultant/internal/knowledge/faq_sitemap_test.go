package knowledge

import (
	"bufio"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The curated FAQ reaches Skipper only through local: entries in
// sitemaps/faq.txt, so a file under config/skipper/faq that the sitemap does
// not list is never ingested.
func TestFAQSitemapListsEveryCuratedFAQFile(t *testing.T) {
	root := filepath.Join("..", "..", "..", "config", "skipper")
	entries, err := os.ReadDir(filepath.Join(root, "faq"))
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".md") {
			want = append(want, "../faq/"+entry.Name())
		}
	}
	file, err := os.Open(filepath.Join(root, "sitemaps", "faq.txt"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var listed []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		if line := strings.TrimSpace(scanner.Text()); strings.HasPrefix(line, localPrefix) {
			listed = append(listed, strings.TrimSpace(strings.TrimPrefix(line, localPrefix)))
		}
	}
	if err = scanner.Err(); err != nil {
		t.Fatal(err)
	}
	slices.Sort(listed)
	if !slices.Equal(listed, want) {
		t.Fatalf("faq.txt lists %v, want every curated FAQ file %v", listed, want)
	}
}
