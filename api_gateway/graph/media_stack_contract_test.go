package graph

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"frameworks/api_gateway/graph/generated"
	"github.com/vektah/gqlparser/v2"
)

func TestMediaStackOperationsMatchSchema(t *testing.T) {
	schema := generated.NewExecutableSchema(generated.Config{}).Schema()
	root := filepath.Join(findRepoRoot(t), "scripts", "stack", "scenarios")
	operation := regexp.MustCompile(`gql\s+'((?:mutation|query)[^']+)'`)
	for _, name := range []string{
		"66-transcode-ladders.sh", "67-transcode-ladder-downgrade.sh", "68-vod-original-retention.sh",
		"78-vod-upload-formats.sh", "79-rotated-upload.sh",
	} {
		t.Run(name, func(t *testing.T) {
			contents, err := os.ReadFile(filepath.Join(root, name))
			if err != nil {
				t.Fatal(err)
			}
			operations := operation.FindAllSubmatch(contents, -1)
			if len(operations) == 0 {
				t.Fatal("no GraphQL operations found in media scenario")
			}
			for _, match := range operations {
				if _, errs := gqlparser.LoadQueryWithRules(schema, string(match[1]), nil); len(errs) != 0 {
					t.Errorf("invalid stack operation %s: %v", match[1], errs)
				}
			}
		})
	}
}
