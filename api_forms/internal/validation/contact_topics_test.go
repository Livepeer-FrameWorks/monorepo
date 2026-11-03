package validation

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// The notification names every topic the contact form offers, and only those.
func TestContactTopicLabelsMatchTheForm(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate test source")
	}
	source, err := os.ReadFile(filepath.Join(filepath.Dir(filename), "../../../website_marketing/src/lib/contactTopics.js"))
	if err != nil {
		t.Fatalf("read contact topics: %v", err)
	}
	var form []string
	for _, match := range regexp.MustCompile(`(?m)^\s+id: "([a-z]+)",$`).FindAllStringSubmatch(string(source), -1) {
		form = append(form, match[1])
	}
	var labelled []string
	for id := range contactTopicLabels {
		labelled = append(labelled, id)
	}
	sort.Strings(form)
	sort.Strings(labelled)
	if len(form) == 0 || strings.Join(form, ",") != strings.Join(labelled, ",") {
		t.Fatalf("form topics %v, notification labels %v", form, labelled)
	}
	if ContactTopicLabel("trial") != "Trial usage" || ContactTopicLabel("unknown") != "Not specified" || ContactTopicLabel("") != "Not specified" {
		t.Fatal("topic labels do not resolve")
	}
}
