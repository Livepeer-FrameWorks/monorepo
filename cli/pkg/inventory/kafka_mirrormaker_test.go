package inventory

import (
	"strings"
	"testing"
)

func TestParseManifestKafkaMirrorMakerLinks(t *testing.T) {
	manifest, err := ParseManifest([]byte(`
infrastructure:
  kafka:
    enabled: true
    region_id: eu-west
    role: aggregator
    regional:
      - region_id: us-east
        role: regional
    mirrormaker:
      enabled: true
      heap_opts: "-Xmx1G -Xms1G"
      task_count: 2
      links:
        - source: us-east
          target: eu-west
          hosts: [regional-eu-1, regional-eu-2, regional-eu-3]
        - source: eu-west
          target: us-east
          hosts: [regional-us-1]
          topics: [analytics_events]
          task_count: 4
`))
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	links := manifest.Infrastructure.Kafka.MirrorMaker.Links
	if len(links) != 2 {
		t.Fatalf("links = %#v, want 2", links)
	}
	if links[0].Source != "us-east" || links[0].Target != "eu-west" || len(links[0].Hosts) != 3 {
		t.Fatalf("first link = %#v", links[0])
	}
	if links[1].TaskCount != 4 || len(links[1].Topics) != 1 || links[1].Topics[0] != "analytics_events" {
		t.Fatalf("second link overrides = %#v", links[1])
	}
}

func TestParseManifestRejectsRemovedMirrorMakerFields(t *testing.T) {
	for name, tc := range map[string]struct {
		yaml  string
		field string
	}{
		"worker hosts on mirrormaker": {
			yaml: `
infrastructure:
  kafka:
    mirrormaker:
      enabled: true
      hosts: [regional-eu-1]
`,
			field: "hosts",
		},
		"single worker host on mirrormaker": {
			yaml: `
infrastructure:
  kafka:
    mirrormaker:
      enabled: true
      host: regional-eu-1
`,
			field: "host",
		},
		"mirror topics on regional cluster": {
			yaml: `
infrastructure:
  kafka:
    regional:
      - region_id: us-east
        mirror_topics: [analytics_events]
`,
			field: "mirror_topics",
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ParseManifest([]byte(tc.yaml))
			if err == nil || !strings.Contains(err.Error(), "field "+tc.field+" not found") {
				t.Fatalf("ParseManifest error = %v, want unknown field %q", err, tc.field)
			}
		})
	}
}
