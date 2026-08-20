package accesspolicy

import (
	"bufio"
	"os"
	"regexp"
	"strings"
	"testing"
)

var graphqlFieldPattern = regexp.MustCompile(`^  ([A-Za-z_][A-Za-z0-9_]*)\s*(?:\(|:)`)

// This contract deliberately reads the source schema. Adding a mutation must
// be accompanied by an explicit access-class decision in the same change.
func TestEveryGraphQLMutationIsClassified(t *testing.T) {
	file, err := os.Open("../graphql/schema.graphql")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	seen := make(map[string]bool)
	inMutation := false
	inDescription := false
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if !inMutation {
			inMutation = strings.TrimSpace(line) == "type Mutation {"
			continue
		}
		if strings.Count(line, `"""`)%2 == 1 {
			inDescription = !inDescription
			continue
		}
		if inDescription {
			continue
		}
		if strings.TrimSpace(line) == "}" {
			break
		}
		match := graphqlFieldPattern.FindStringSubmatch(line)
		if len(match) == 2 {
			seen[match[1]] = true
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}

	for name := range seen {
		if _, ok := GraphQLMutationClass(name); !ok {
			t.Errorf("GraphQL mutation %q has no access policy", name)
		}
	}
	for name := range GraphQLMutationClasses() {
		if !seen[name] {
			t.Errorf("access policy refers to missing GraphQL mutation %q", name)
		}
	}
}
