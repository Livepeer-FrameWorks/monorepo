package provisioner

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"testing/fstest"

	"frameworks/cli/pkg/gitops"
)

func edgeRenderTestTree() fstest.MapFS {
	roles := edgeRenderRolesDir + "/"
	return fstest.MapFS{
		"ansible.cfg":      {Data: []byte("[defaults]\n")},
		"requirements.yml": {Data: []byte("collections: []\n")},
		"playbooks/edge.yml": {
			Data: []byte("- hosts: all\n  roles:\n    - role: frameworks.infra.edge\n"),
		},
		"playbooks/postgres.yml":                                      {Data: []byte("- hosts: all\n")},
		edgeRenderPluginsDir + "/modules/m.py":                        {Data: []byte("# module\n")},
		roles + "edge/tasks/main.yml":                                 {Data: []byte("- ansible.builtin.include_role:\n    name: frameworks.infra.helmsman\n")},
		roles + "edge/templates/edge.env.j2":                          {Data: []byte("NODE_ID={{ edge_node_id }}\n")},
		roles + "edge/molecule/default/verify.yml":                    {Data: []byte("- hosts: all\n")},
		roles + "helmsman/tasks/main.yml":                             {Data: []byte("- ansible.builtin.include_role:\n    name: frameworks.infra.helmsman\n- frameworks.infra.yugabyte_migration_apply: {}\n")},
		roles + "helmsman/templates/helmsman.env.j2":                  {Data: []byte("A=1\n")},
		roles + "postgres/tasks/main.yml":                             {Data: []byte("- debug: {}\n")},
		roles + "helmsman/files/__pycache__/x.pyc":                    {Data: []byte("bytecode")},
		roles + "edge/templates/.DS_Store":                            {Data: []byte("finder")},
		"collections/ansible_collections/frameworks/infra/galaxy.yml": {Data: []byte("namespace: frameworks\n")},
	}
}

func mustEdgeRenderDigest(t *testing.T, tree fstest.MapFS, page []byte) string {
	t.Helper()
	digest, err := edgeRenderDigest(tree, page, edgeRoleVarsFrom)
	if err != nil {
		t.Fatalf("edgeRenderDigest: %v", err)
	}
	return digest
}

// The render digest follows the edge playbook, the edge role, every role it
// references, the collection plugins and the maintenance page, and nothing
// else: an unrelated role, another playbook or a molecule scenario leaves it
// unchanged, so it moves only when this CLI would render an edge differently.
func TestEdgeRenderDigestCoversEdgeRenderLogicOnly(t *testing.T) {
	page := []byte("<html>maintenance</html>")
	base := mustEdgeRenderDigest(t, edgeRenderTestTree(), page)
	if base != mustEdgeRenderDigest(t, edgeRenderTestTree(), page) {
		t.Fatal("render digest is not deterministic")
	}

	changes := map[string]string{
		edgeRenderRolesDir + "/edge/templates/edge.env.j2":         "NODE_ID={{ edge_node_id }}\nX=1\n",
		edgeRenderRolesDir + "/helmsman/templates/helmsman.env.j2": "A=2\n",
		edgeRenderPlaybook:                     "- hosts: all\n  become: false\n",
		edgeRenderRequirement:                  "collections: [x]\n",
		edgeRenderPluginsDir + "/modules/m.py": "# module v2\n",
	}
	for path, content := range changes {
		tree := edgeRenderTestTree()
		tree[path] = &fstest.MapFile{Data: []byte(content)}
		if mustEdgeRenderDigest(t, tree, page) == base {
			t.Fatalf("changing %s left the render digest unchanged", path)
		}
	}
	if mustEdgeRenderDigest(t, edgeRenderTestTree(), []byte("<html>v2</html>")) == base {
		t.Fatal("changing the maintenance page left the render digest unchanged")
	}

	unrelated := map[string]string{
		edgeRenderRolesDir + "/postgres/tasks/main.yml":               "- debug: {msg: x}\n",
		edgeRenderRolesDir + "/edge/molecule/default/verify.yml":      "- hosts: none\n",
		edgeRenderRolesDir + "/helmsman/files/__pycache__/x.pyc":      "other bytecode",
		edgeRenderRolesDir + "/edge/templates/.DS_Store":              "other finder",
		"playbooks/postgres.yml":                                      "- hosts: none\n",
		"collections/ansible_collections/frameworks/infra/galaxy.yml": "namespace: frameworks\nversion: 2\n",
	}
	for path, content := range unrelated {
		tree := edgeRenderTestTree()
		tree[path] = &fstest.MapFile{Data: []byte(content)}
		if mustEdgeRenderDigest(t, tree, page) != base {
			t.Fatalf("changing %s, which edge provisioning does not render, changed the render digest", path)
		}
	}
}

// A CLI whose Go derivation of the role variables changes renders different
// provisioned files from the same node inputs, so the digest moves even when
// every role file is unchanged. Every install shape of the fixture counts.
func TestEdgeRenderDigestCoversGoDerivedRoleVars(t *testing.T) {
	tree := edgeRenderTestTree()
	base, err := edgeRenderDigest(tree, nil, edgeRoleVarsFrom)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range edgeRenderFixtureCases {
		changed := func(config *EdgeProvisionConfig, remoteOS, remoteArch string, src edgeRoleSources) (map[string]any, error) {
			vars, err := edgeRoleVarsFrom(config, remoteOS, remoteArch, src)
			if err != nil {
				return nil, err
			}
			if config.Mode == c.Mode && remoteOS == c.OS && remoteArch == c.Arch {
				// A derivation bug of the kind a release could ship: bits
				// instead of bytes per second.
				vars["edge_bandwidth_limit_bytes_per_sec"] = uint64(config.BandwidthMbps) * 1000 * 1000
			}
			return vars, nil
		}
		got, err := edgeRenderDigest(tree, nil, changed)
		if err != nil {
			t.Fatal(err)
		}
		if got == base {
			t.Fatalf("changing a Go-derived role var for %s left the render digest unchanged", c.Name)
		}
	}
}

// The digest does not depend on the node being provisioned or on the release
// it is provisioned at: two different nodes on different releases carry the
// same render digest.
func TestEdgeRoleVarsRenderDigestIgnoresNodeAndReleaseInputs(t *testing.T) {
	want, digestErr := EdgeRenderDigest()
	if digestErr != nil {
		t.Skipf("this test binary cannot read its Ansible tree: %v", digestErr)
	}
	prev := fetchEdgeManifestFn
	defer func() { fetchEdgeManifestFn = prev }()
	digests := map[string]any{}
	for _, node := range []struct{ id, version string }{{"edge-eu-1", "v0.3.12"}, {"edge-us-7", "v0.3.13"}} {
		fetchEdgeManifestFn = func(string) (*gitops.Manifest, error) {
			m := edgeRenderFixtureManifest()
			m.PlatformVersion = node.version
			m.Services[0].Digest = "sha256:" + node.id
			return m, nil
		}
		vars, err := edgeRoleVars(&EdgeProvisionConfig{Mode: "container", Version: node.version, NodeID: node.id, ClusterID: "media-" + node.id, NodeDomain: node.id + ".example.com"}, "linux", "amd64")
		if err != nil {
			t.Fatal(err)
		}
		if vars["edge_node_id"] != node.id {
			t.Fatalf("edge_node_id = %#v, want %s", vars["edge_node_id"], node.id)
		}
		digests[node.id] = vars["edge_config_render_digest"]
	}
	if digests["edge-eu-1"] != want || digests["edge-us-7"] != want {
		t.Fatalf("render digests = %v, want both %s", digests, want)
	}
}

func TestEdgeRenderDigestFailsWithoutTheEdgeRole(t *testing.T) {
	tree := edgeRenderTestTree()
	for path := range tree {
		if strings.HasPrefix(path, edgeRenderRolesDir+"/edge/") {
			delete(tree, path)
		}
	}
	if _, err := edgeRenderDigest(tree, nil, edgeRoleVarsFrom); err == nil {
		t.Fatal("render digest of a tree without the edge role succeeded")
	}
}

// The checkout's edge role resolves to a digest that includes the roles it
// runs: a change to the helmsman role's files changes it.
func TestEdgeRenderDigestFollowsCheckoutRoleReferences(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	ansibleDir := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", "..", "ansible"))
	tree := fstest.MapFS{}
	if err := filepath.WalkDir(ansibleDir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// .cache holds Galaxy installs from local lint runs, not CLI content.
		if d.IsDir() && d.Name() == ".cache" {
			return filepath.SkipDir
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(ansibleDir, p)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		tree[filepath.ToSlash(rel)] = &fstest.MapFile{Data: data}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	base := mustEdgeRenderDigest(t, tree, nil)
	for _, role := range []string{"helmsman", "mistserver", "node_baseline"} {
		changed := fstest.MapFS{}
		for k, v := range tree {
			changed[k] = v
		}
		changed[edgeRenderRolesDir+"/"+role+"/defaults/render-digest-probe.yml"] = &fstest.MapFile{Data: []byte("---\n")}
		if mustEdgeRenderDigest(t, changed, nil) == base {
			t.Fatalf("a change to the %s role, which edge provisioning runs, left the render digest unchanged", role)
		}
	}
}
