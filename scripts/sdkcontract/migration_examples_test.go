package main

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/vektah/gqlparser/v2"
)

func TestLivepeerSessionMigrationExamplesValidate(t *testing.T) {
	schema, err := loadPublicSchema("../..", headRef)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("../../website_docs/src/content/docs/migrate/livepeer-studio.mdx")
	if err != nil {
		t.Fatal(err)
	}
	_, section, found := strings.Cut(string(raw), "## Sessions, saved chapters, and live state")
	if !found {
		t.Fatal("session migration guide missing")
	}
	section, _, _ = strings.Cut(section, "\n## ")
	blocks := regexp.MustCompile("(?m)^```graphql\\s*\n([\\s\\S]*?)^```").FindAllStringSubmatch(section, -1)
	if len(blocks) == 0 {
		t.Fatal("session migration query missing")
	}
	for _, block := range blocks {
		if _, problems := gqlparser.LoadQueryWithRules(schema, block[1], nil); len(problems) > 0 {
			t.Errorf("session migration example does not validate: %s", problems)
		}
	}
}

func TestStreamIntegrationGuideExamplesValidate(t *testing.T) {
	schema, err := loadPublicSchema("../..", headRef)
	if err != nil {
		t.Fatal(err)
	}
	for _, page := range []string{"recordings", "clips", "dvr-chapters", "streams"} {
		t.Run(page, func(t *testing.T) {
			raw, err := os.ReadFile("../../website_docs/src/content/docs/builders/" + page + ".mdx")
			if err != nil {
				t.Fatal(err)
			}
			blocks := regexp.MustCompile("(?m)^```graphql\\s*\n([\\s\\S]*?)^```").FindAllStringSubmatch(string(raw), -1)
			if len(blocks) == 0 {
				t.Fatal("integration guide has no GraphQL examples")
			}
			for index, block := range blocks {
				if _, problems := gqlparser.LoadQueryWithRules(schema, block[1], nil); len(problems) > 0 {
					t.Errorf("example %d does not validate: %s", index+1, problems)
				}
			}
		})
	}
}
