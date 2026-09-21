package topology

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"
	"time"
)

// declaredTopicConstants parses kafka_topics.go so a Topic* constant added
// without a canonical spec fails the test instead of silently shipping with the
// broker's default retention.
func declaredTopicConstants(t *testing.T) map[string]string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "kafka_topics.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			vs := spec.(*ast.ValueSpec)
			for i, name := range vs.Names {
				if !strings.HasPrefix(name.Name, "Topic") || i >= len(vs.Values) {
					continue
				}
				lit, ok := vs.Values[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				value, err := strconv.Unquote(lit.Value)
				if err != nil {
					t.Fatal(err)
				}
				out[name.Name] = value
			}
		}
	}
	if len(out) == 0 {
		t.Fatal("no Topic* constants found")
	}
	return out
}

func TestEveryCanonicalTopicDeclaresRetention(t *testing.T) {
	for constName, topic := range declaredTopicConstants(t) {
		spec, ok := CanonicalTopic(topic)
		if !ok {
			t.Fatalf("%s (%q) has no CanonicalTopics entry", constName, topic)
		}
		if spec.Retention <= 0 {
			t.Fatalf("%s: retention %v", topic, spec.Retention)
		}
		if spec.Config["retention.ms"] != strconv.FormatInt(spec.Retention.Milliseconds(), 10) {
			t.Fatalf("%s: retention.ms %q does not match %v", topic, spec.Config["retention.ms"], spec.Retention)
		}
	}
	if got, want := len(CanonicalTopics()), len(declaredTopicConstants(t)); got != want {
		t.Fatalf("CanonicalTopics has %d entries, %d Topic* constants are declared", got, want)
	}
}

func TestCanonicalTopicRetentionPolicy(t *testing.T) {
	const day = 24 * time.Hour
	want := map[string]time.Duration{
		TopicAnalyticsEvents:     7 * day,
		TopicServiceEvents:       180 * day,
		TopicRawMistTriggers:     30 * day,
		TopicBillingUsageReports: 365 * day,
		TopicDecklogDLQ:          90 * day,
		TopicLookoutIncidents:    7 * day,
		TopicDomainEvents:        180 * day,
	}
	for topic, retention := range want {
		spec, ok := CanonicalTopic(topic)
		if !ok || spec.Retention != retention {
			t.Fatalf("%s retention = %v (found %v), want %v", topic, spec.Retention, ok, retention)
		}
	}
}

func TestDomainEventsTopicContract(t *testing.T) {
	spec, ok := CanonicalTopic(TopicDomainEvents)
	if !ok {
		t.Fatal("domain.events missing")
	}
	if spec.Partitions != 12 || spec.ReplicationFactor != 3 {
		t.Fatalf("domain.events partitions/RF = %d/%d, want 12/3", spec.Partitions, spec.ReplicationFactor)
	}
	if spec.Config["cleanup.policy"] != "delete" || spec.Config["retention.ms"] != "15552000000" {
		t.Fatalf("domain.events config = %v", spec.Config)
	}
}

func TestCanonicalTopicsReturnsFreshConfig(t *testing.T) {
	first := CanonicalTopics()
	first[0].Config["retention.ms"] = "1"
	if again, _ := CanonicalTopic(first[0].Name); again.Config["retention.ms"] == "1" {
		t.Fatal("CanonicalTopics returned shared config maps")
	}
}
