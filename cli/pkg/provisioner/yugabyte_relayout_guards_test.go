package provisioner

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// routedRelayoutNode answers queries by the first route whose fragment appears in the SQL, and records shell scripts.
type routedRelayoutNode struct {
	name     string
	routes   []relayoutRoute
	shellOut string

	mu      sync.Mutex
	scripts []string
}

type relayoutRoute struct {
	fragment string
	reply    string
}

func (n *routedRelayoutNode) Name() string { return n.name }

func (n *routedRelayoutNode) Query(_ context.Context, _, sql string) (string, error) {
	for _, route := range n.routes {
		if strings.Contains(sql, route.fragment) {
			return route.reply, nil
		}
	}
	return "", fmt.Errorf("%s: unexpected query %q", n.name, sql)
}

func (n *routedRelayoutNode) Shell(_ context.Context, script string) (string, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.scripts = append(n.scripts, script)
	return n.shellOut, nil
}

func tserverNode(name, addresses string, routes ...relayoutRoute) *routedRelayoutNode {
	// The shell output doubles as the node's identities and as the worker-stop script's report.
	return &routedRelayoutNode{name: name, shellOut: name + "\n" + addresses + "\nremaining=0\n", routes: append(routes,
		relayoutRoute{"inet_server_addr", "127.0.0.1"}, relayoutRoute{"yb_is_client_ysqlconnmgr", "off"})}
}

func TestRequireLiveTopologyMatchesNodesToLiveTservers(t *testing.T) {
	live := relayoutRoute{"yb_servers()", "10.0.0.1\n10.0.0.2"}
	cases := []struct {
		name  string
		nodes []*routedRelayoutNode
		want  string
	}{
		{"all tservers present", []*routedRelayoutNode{tserverNode("yuga-1", "10.0.0.1 192.168.1.5"), tserverNode("yuga-2", "10.0.0.2")}, ""},
		{"live tserver missing from nodes", []*routedRelayoutNode{tserverNode("yuga-1", "10.0.0.1")}, "live tserver 10.0.0.2 is not among the relayout nodes"},
		{"node is not a live tserver", []*routedRelayoutNode{tserverNode("yuga-1", "10.0.0.1"), tserverNode("yuga-2", "10.0.0.2"), tserverNode("yuga-3", "10.0.0.9")}, "node yuga-3 is not a live tserver"},
		{"two nodes claim one tserver", []*routedRelayoutNode{tserverNode("yuga-1", "10.0.0.1"), tserverNode("yuga-2", "10.0.0.1 10.0.0.2")}, "matches several nodes"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.nodes[0].routes = append([]relayoutRoute{live}, tc.nodes[0].routes...)
			nodes := make([]YugabyteNode, len(tc.nodes))
			for i, node := range tc.nodes {
				nodes[i] = node
			}
			relayout := &YugabyteRelayout{Primary: tc.nodes[0], Nodes: nodes, Database: "purser"}
			err := relayout.requireLiveTopology(context.Background())
			switch {
			case tc.want == "" && err != nil:
				t.Fatalf("requireLiveTopology: %v", err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Fatalf("requireLiveTopology error = %v, want it to contain %q", err, tc.want)
			}
			for _, node := range tc.nodes {
				for _, script := range node.scripts {
					for _, line := range strings.Split(script, "\n") {
						if fields := strings.Fields(line); len(fields) > 0 && fields[0] == "hostname" && len(fields) > 1 && !strings.HasPrefix(fields[1], "-") && fields[1] != "2>/dev/null" {
							t.Fatalf("identity script can set the host name: %q", line)
						}
					}
				}
			}
		})
	}
}

func TestRequireReplaceableCopyRefusesUnmarkedDatabase(t *testing.T) {
	cases := []struct {
		name   string
		exists string
		marker string
		want   string
	}{
		{"absent", "0", "", ""},
		{"earlier shadow of this database", "1", relayoutCopyMarker(relayoutCopyShadow, "purser"), ""},
		{"unmarked database with the shadow name", "1", "", "was not created as the relayout shadow of purser"},
		{"shadow of another database", "1", relayoutCopyMarker(relayoutCopyShadow, "purser_eu"), "was not created as the relayout shadow of purser"},
		{"preflight copy under the shadow name", "1", relayoutCopyMarker(relayoutCopyPreflight, "purser"), "was not created as the relayout shadow of purser"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			node := &routedRelayoutNode{name: "yuga-1", routes: []relayoutRoute{
				{"SELECT count(*) FROM pg_database", tc.exists},
				{"shobj_description", tc.marker},
				{"to_regclass", "f"},
			}}
			relayout := &YugabyteRelayout{Primary: node, Database: "purser"}
			err := relayout.requireReplaceableCopy(context.Background(), relayout.ShadowName(), relayoutCopyShadow)
			switch {
			case tc.want == "" && err != nil:
				t.Fatalf("requireReplaceableCopy: %v", err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Fatalf("requireReplaceableCopy error = %v, want it to contain %q", err, tc.want)
			}
			if tc.want != "" {
				for _, script := range node.scripts {
					if strings.Contains(script, "DROP DATABASE") {
						t.Fatal("refused copy was dropped")
					}
				}
			}
		})
	}
}

func relayoutJournalRow(values ...string) string {
	encoded := make([]string, len(values))
	for i, value := range values {
		encoded[i] = base64.StdEncoding.EncodeToString([]byte(value))
	}
	return strings.Join(encoded, "|")
}

// rollbackNode is a single-tserver cluster for rollback tests: the journal row, the OID under each relayout name
// (empty when the name is free), and the ACL of the canonical database.
func rollbackNode(journal, canonicalOID, preOID, shadowOID, canonicalACL string, extra ...relayoutRoute) *sqlRecordingNode {
	marker := base64.StdEncoding.EncodeToString([]byte(relayoutCopyMarker(relayoutCopyShadow, "purser")))
	// A test's extra routes answer first, so they can override the defaults.
	routes := append(append([]relayoutRoute{}, extra...), []relayoutRoute{
		{"to_regclass", "t"},
		{"FROM public.frameworks_relayout_journal WHERE", journal},
		{"yb_servers()", "10.0.0.1"},
		{"SELECT oid::text || '|'", canonicalOID + "|" + marker},
		{"SELECT oid::text FROM pg_database WHERE datname = 'purser__pre_relayout'", preOID},
		{"SELECT oid::text FROM pg_database WHERE datname = 'purser__relayout'", shadowOID},
		{"SELECT oid::text FROM pg_database WHERE datname = 'purser'", canonicalOID},
		{"string_agg(rolname", canonicalACL + "|yugabyte"},
		{"datacl", canonicalACL},
		{"rolsuper", "yugabyte"},
	}...)
	return &sqlRecordingNode{routedRelayoutNode: *tserverNode("yuga-1", "10.0.0.1", routes...)}
}

func statementsContaining(statements []string, fragment string) []string {
	var matched []string
	for _, statement := range statements {
		if strings.Contains(statement, fragment) {
			matched = append(matched, statement)
		}
	}
	return matched
}

func TestRollbackRefusesOnceRelaidDatabaseAcceptsConnections(t *testing.T) {
	journal := relayoutJournalRow(string(RelayoutRenamedNew), "unit", "2099-01-01", "{purser=CTc/purser,purser_runtime=c/purser}", "", "", "", "16384", "", "", "")
	for _, tc := range []struct {
		name string
		acl  string
		want string
	}{
		{"access restored before the journal advanced", "{purser=CTc/purser,purser_runtime=c/purser}", "roll forward with cutover instead"},
		{"relaid database still fenced", "{}", "unexpected query"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			node := rollbackNode(journal, "20000", "16384", "", tc.acl)
			relayout := &YugabyteRelayout{Primary: node, Database: "purser", LeaseOwner: "unit"}
			err := relayout.Rollback(context.Background())
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Rollback error = %v, want it to contain %q", err, tc.want)
			}
			if tc.acl != "{}" {
				if renames := statementsContaining(node.statements, "RENAME"); len(renames) > 0 {
					t.Fatalf("rollback renamed a database before refusing: %v", renames)
				}
			}
		})
	}
}

// TestRollbackConvergesAfterAnInterruptedRollback covers a rollback that died after returning the original to its name
// but before restoring its access: the journal still says renamed_new, the canonical name holds the original with no
// grants, and neither copy name exists. The rerun must not rename the original away; it restores access and finishes.
func TestRollbackConvergesAfterAnInterruptedRollback(t *testing.T) {
	sourceACL := "{purser=CTc/purser,purser_runtime=c/purser}"
	journal := relayoutJournalRow(string(RelayoutRenamedNew), "unit", "2099-01-01", sourceACL, "", "", "", "16384", "", "", "")
	node := rollbackNode(journal, "16384", "", "", "{}",
		relayoutRoute{"SELECT count(*) FROM pg_database", "0"},
		relayoutRoute{"pg_get_userbyid(datdba)", "purser"},
		relayoutRoute{"RETURNING database_name", "purser"},
		relayoutRoute{"WITH recorded AS", "0"},
		relayoutRoute{"aclexplode", "purser|CREATE|t\npurser|CONNECT|f"},
		relayoutRoute{"GRANT", ""},
		relayoutRoute{"has_database_privilege", "t"},
		relayoutRoute{"RETURNING state", string(RelayoutRolledBack)},
	)
	relayout := &YugabyteRelayout{Primary: node, Database: "purser", LeaseOwner: "unit"}
	err := relayout.Rollback(context.Background())
	if renames := statementsContaining(node.statements, "RENAME"); len(renames) > 0 {
		t.Fatalf("rerun renamed the original away from its canonical name: %v (err %v)", renames, err)
	}
	if len(statementsContaining(node.statements, "GRANT")) == 0 {
		t.Fatalf("rerun did not restore access to the original (err %v):\n%s", err, strings.Join(node.statements, "\n---\n"))
	}
	if err != nil || len(statementsContaining(node.statements, "RETURNING state")) == 0 {
		t.Fatalf("rerun did not finish the rollback: %v", err)
	}
}

// TestRollbackReturnsAnOriginalLeftUnderTheShadowName recovers the state an earlier rollback left when it renamed the
// original to the shadow name: the original is found by its OID and renamed back.
func TestRollbackReturnsAnOriginalLeftUnderTheShadowName(t *testing.T) {
	journal := relayoutJournalRow(string(RelayoutRenamedNew), "unit", "2099-01-01", "{purser=CTc/purser}", "", "", "", "16384", "", "", "")
	node := rollbackNode(journal, "", "", "16384", "{}",
		relayoutRoute{"pg_terminate_backend", "0"},
		relayoutRoute{"FROM pg_stat_activity", ""},
		relayoutRoute{"RETURNING database_name", "purser"},
	)
	relayout := &YugabyteRelayout{Primary: node, Database: "purser", LeaseOwner: "unit", SettleDelay: time.Millisecond}
	_ = relayout.Rollback(context.Background())
	renames := statementsContaining(node.statements, "RENAME")
	if len(renames) == 0 || renames[0] != `ALTER DATABASE "purser__relayout" RENAME TO "purser"` {
		t.Fatalf("renames = %v, want the original renamed back from the shadow name", renames)
	}
}

func TestRollbackRefusesAWrongDumpDirBeforeChangingAnything(t *testing.T) {
	journal := relayoutJournalRow(string(RelayoutRenamedOld), "unit", "2099-01-01", "{purser=CTc/purser}", "/data/x/purser/relayout", "abc", "yuga-1", "16384", "", "", "")
	node := rollbackNode(journal, "", "16384", "20000", "{}")
	relayout := &YugabyteRelayout{Primary: node, Database: "purser", LeaseOwner: "unit", DumpDir: "/var/lib/frameworks/relayout"}
	err := relayout.Rollback(context.Background())
	if err == nil || !strings.Contains(err.Error(), "pass the --dump-dir prepare used") {
		t.Fatalf("Rollback = %v, want a refusal naming the dump directory", err)
	}
	for _, fragment := range []string{"RENAME", "DROP DATABASE", "GRANT", "yb_servers()"} {
		if hits := statementsContaining(node.statements, fragment); len(hits) > 0 {
			t.Fatalf("rollback reached %q before validating the dump location: %v", fragment, hits)
		}
	}
}

func TestRollbackRestoresAccessAfterAFenceThatCouldNotFinish(t *testing.T) {
	// Fence recorded the identity and revoked access, then failed before the state advanced.
	journal := relayoutJournalRow(string(RelayoutPrepared), "unit", "2099-01-01", "{purser=CTc/purser}", "", "", "", "16384", "", "", "")
	node := rollbackNode(journal, "16384", "", "", "{}",
		relayoutRoute{"SELECT count(*) FROM pg_database", "0"},
		relayoutRoute{"pg_get_userbyid(datdba)", "purser"},
		relayoutRoute{"RETURNING database_name", "purser"},
		relayoutRoute{"WITH recorded AS", "0"},
		relayoutRoute{"aclexplode", "purser|CREATE|t"},
		relayoutRoute{"GRANT", ""},
		relayoutRoute{"has_database_privilege", "t"},
		relayoutRoute{"RETURNING state", string(RelayoutRolledBack)},
	)
	relayout := &YugabyteRelayout{Primary: node, Database: "purser", LeaseOwner: "unit"}
	rollbackErr := relayout.Rollback(context.Background())
	if len(statementsContaining(node.statements, "GRANT")) == 0 {
		t.Fatalf("rollback of an interrupted fence did not restore access (err %v):\n%s", rollbackErr, strings.Join(node.statements, "\n---\n"))
	}
	advance := statementsContaining(node.statements, "RETURNING state")
	if len(advance) == 0 || !strings.Contains(advance[0], "state IN ('prepared')") || !strings.Contains(advance[0], "source_oid = NULL") {
		t.Fatalf("rollback advanced as %v (err %v), want prepared to rolled_back with the identity cleared", advance, rollbackErr)
	}
}

// TestRollbackOfAPreparedShadowLeavesTheSourceAlone covers a rollback before cutover began: services never lost
// access, so only the shadow is dropped, and the source is neither renamed nor granted anything.
func TestRollbackOfAPreparedShadowLeavesTheSourceAlone(t *testing.T) {
	journal := relayoutJournalRow(string(RelayoutPrepared), "unit", "2099-01-01", "", "", "", "", "", "", "", "")
	marker := relayoutCopyMarker(relayoutCopyShadow, "purser")
	node := rollbackNode(journal, "16384", "", "20000", "{purser=CTc/purser}",
		relayoutRoute{"SELECT count(*) FROM pg_database", "1"},
		relayoutRoute{"SELECT coalesce(shobj_description", marker},
		relayoutRoute{"pg_terminate_backend", "0"},
		relayoutRoute{"FROM pg_stat_activity", ""},
		relayoutRoute{"RETURNING database_name", "purser"},
		relayoutRoute{"DROP DATABASE", ""},
		relayoutRoute{"RETURNING state", string(RelayoutRolledBack)},
	)
	relayout := &YugabyteRelayout{Primary: node, Database: "purser", LeaseOwner: "unit", SettleDelay: time.Millisecond}
	if err := relayout.Rollback(context.Background()); err != nil {
		t.Fatalf("Rollback of a prepared shadow: %v\n%s", err, strings.Join(node.statements, "\n---\n"))
	}
	if drops := statementsContaining(node.statements, "DROP DATABASE"); len(drops) != 1 || drops[0] != `DROP DATABASE "purser__relayout"` {
		t.Fatalf("drops = %v, want only the shadow dropped", drops)
	}
	for _, fragment := range []string{"RENAME", "GRANT", "REVOKE"} {
		if hits := statementsContaining(node.statements, fragment); len(hits) > 0 {
			t.Fatalf("rollback of a prepared shadow touched the source with %q: %v", fragment, hits)
		}
	}
}

func TestRequireNoConnectionManagerRefusesPooledTservers(t *testing.T) {
	plain := tserverNode("yuga-1", "10.0.0.1")
	pooled := &routedRelayoutNode{name: "yuga-2", routes: []relayoutRoute{{"yb_is_client_ysqlconnmgr", "on"}}}
	relayout := &YugabyteRelayout{Primary: plain, Nodes: []YugabyteNode{plain, pooled}, Database: "purser"}
	err := relayout.requireNoConnectionManager(context.Background())
	if err == nil || !strings.Contains(err.Error(), "enabled on yuga-2") || !strings.Contains(err.Error(), "33018") {
		t.Fatalf("requireNoConnectionManager = %v, want a refusal naming yuga-2 and the upstream issue", err)
	}
	relayout.Nodes = []YugabyteNode{plain}
	if err = relayout.requireNoConnectionManager(context.Background()); err != nil {
		t.Fatalf("requireNoConnectionManager without the pooler: %v", err)
	}
}

func TestCutoverRefusesToRestoreAccessOnTheOriginal(t *testing.T) {
	journal := relayoutJournalRow(string(RelayoutRenamedNew), "unit", "2099-01-01", "{purser=CTc/purser}", "", "", "", "16384", "", "", "")
	node := rollbackNode(journal, "16384", "", "", "{}", relayoutRoute{"SELECT count(*) FROM pg_database", "1"})
	relayout := &YugabyteRelayout{Primary: node, Database: "purser", LeaseOwner: "unit"}
	err := relayout.Cutover(context.Background(), nil, "", nil)
	if err == nil || !strings.Contains(err.Error(), "is the original database") {
		t.Fatalf("Cutover = %v, want a refusal to treat the original as the relaid copy", err)
	}
	if grants := statementsContaining(node.statements, "GRANT"); len(grants) > 0 {
		t.Fatalf("cutover granted access to the original: %v", grants)
	}
}

func TestRenameRequiresALiveLease(t *testing.T) {
	node := &sqlRecordingNode{routedRelayoutNode: routedRelayoutNode{name: "yuga-1", routes: []relayoutRoute{
		{"pg_terminate_backend", "0"},
		{"FROM pg_stat_activity", ""},
		{"RETURNING database_name", ""},
	}}}
	relayout := &YugabyteRelayout{Primary: node, Database: "purser", LeaseOwner: "unit", SettleDelay: time.Millisecond}
	err := relayout.renameDatabase(context.Background(), "purser", "purser__pre_relayout")
	if err == nil || !strings.Contains(err.Error(), "lease") {
		t.Fatalf("renameDatabase without a lease = %v, want a lease error", err)
	}
	if renames := statementsContaining(node.statements, "RENAME"); len(renames) > 0 {
		t.Fatalf("renamed without a live lease: %v", renames)
	}
}

func TestRequireReplaceableCopyAcceptsAnInterruptedCreate(t *testing.T) {
	for _, tc := range []struct {
		name, pending, relations string
		accepted                 bool
	}{
		{"recorded and empty", "purser__relayout", "0", true},
		{"recorded but holds relations", "purser__relayout", "3", false},
		{"not recorded", "", "0", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			node := &routedRelayoutNode{name: "yuga-1", routes: []relayoutRoute{
				{"SELECT count(*) FROM pg_database", "1"},
				{"shobj_description", ""},
				{"to_regclass", "t"},
				{"FROM public.frameworks_relayout_journal WHERE", relayoutJournalRow(string(RelayoutFenced), "unit", "2099-01-01", "", "", "", "", "16384", "", tc.pending, "")},
				{"FROM pg_class c JOIN pg_namespace", tc.relations},
			}}
			relayout := &YugabyteRelayout{Primary: node, Database: "purser"}
			err := relayout.requireReplaceableCopy(context.Background(), relayout.ShadowName(), relayoutCopyShadow)
			if (err == nil) != tc.accepted {
				t.Fatalf("requireReplaceableCopy = %v, want accepted=%v", err, tc.accepted)
			}
		})
	}
}

// sqlRecordingNode answers queries through routes and records every statement it was sent.
type sqlRecordingNode struct {
	routedRelayoutNode
	statements []string
}

func (n *sqlRecordingNode) Query(ctx context.Context, database, sql string) (string, error) {
	n.statements = append(n.statements, sql)
	return n.routedRelayoutNode.Query(ctx, database, sql)
}

// failingRecordingNode records statements and fails any statement containing failOn, simulating a crash there.
type failingRecordingNode struct {
	sqlRecordingNode
	failOn string
}

func (n *failingRecordingNode) Query(ctx context.Context, database, sql string) (string, error) {
	if n.failOn != "" && strings.Contains(sql, n.failOn) {
		n.statements = append(n.statements, sql)
		return "", fmt.Errorf("simulated crash at %q", n.failOn)
	}
	return n.sqlRecordingNode.Query(ctx, database, sql)
}

func TestFenceRecordsSourceIdentityBeforeRevokingAccess(t *testing.T) {
	sourceACL := "{purser=CTc/purser,purser_runtime=c/purser}"
	identity := "16384|" + base64.StdEncoding.EncodeToString([]byte("billing database"))
	node := &failingRecordingNode{failOn: "pg_terminate_backend", sqlRecordingNode: sqlRecordingNode{routedRelayoutNode: *tserverNode("yuga-1", "10.0.0.1",
		relayoutRoute{"to_regclass", "t"},
		relayoutRoute{"FROM public.frameworks_relayout_journal WHERE", relayoutJournalRow(string(RelayoutPrepared), "unit", "2099-01-01", "", "", "", "", "", "", "", "")},
		relayoutRoute{"yb_servers()", "10.0.0.1"},
		relayoutRoute{"datacl", sourceACL},
		relayoutRoute{"SELECT oid::text || '|'", identity},
		relayoutRoute{"RETURNING database_name", "purser"},
		relayoutRoute{"REVOKE ALL", ""},
	)}}
	relayout := &YugabyteRelayout{Primary: node, Database: "purser", LeaseOwner: "unit", Roles: []string{"purser", "purser_runtime"}}
	if err := relayout.Fence(context.Background()); err == nil || !strings.Contains(err.Error(), "simulated crash") {
		t.Fatalf("Fence = %v, want the simulated crash after revocation", err)
	}
	recordAt, revokeAt := -1, -1
	for i, statement := range node.statements {
		if recordAt < 0 && strings.Contains(statement, "UPDATE public.frameworks_relayout_journal") && strings.Contains(statement, "source_acl = "+relayoutLiteral(sourceACL)) &&
			strings.Contains(statement, "source_oid = '16384'") && strings.Contains(statement, "source_comment = 'billing database'") {
			recordAt = i
		}
		if revokeAt < 0 && strings.Contains(statement, "REVOKE ALL") {
			revokeAt = i
		}
	}
	if recordAt < 0 || revokeAt < 0 || recordAt > revokeAt {
		t.Fatalf("source identity recorded at statement %d, access revoked at %d; the identity must be journaled first:\n%s", recordAt, revokeAt, strings.Join(node.statements, "\n---\n"))
	}
}

func TestFenceResumesFromTheRecordedACLAfterACrash(t *testing.T) {
	sourceACL := "{purser=CTc/purser,purser_runtime=c/purser}"
	node := &sqlRecordingNode{routedRelayoutNode: *tserverNode("yuga-1", "10.0.0.1",
		relayoutRoute{"to_regclass", "t"},
		// The crashed attempt recorded the identity and revoked access without advancing the state.
		relayoutRoute{"FROM public.frameworks_relayout_journal WHERE", relayoutJournalRow(string(RelayoutPrepared), "unit", "2099-01-01", sourceACL, "", "", "", "16384", "", "", "")},
		relayoutRoute{"yb_servers()", "10.0.0.1"},
		relayoutRoute{"SELECT oid::text FROM pg_database", "16384"},
		relayoutRoute{"string_agg(rolname", "{}|yugabyte"},
		relayoutRoute{"datacl", "{}"},
		relayoutRoute{"rolsuper", "yugabyte"},
		relayoutRoute{"pg_terminate_backend", "0"},
		relayoutRoute{"FROM pg_stat_activity", ""},
		relayoutRoute{"RETURNING state", string(RelayoutFenced)},
		relayoutRoute{"RETURNING database_name", "purser"},
		relayoutRoute{"REVOKE ALL", ""},
	)}
	relayout := &YugabyteRelayout{Primary: node, Database: "purser", LeaseOwner: "unit", Roles: []string{"purser", "purser_runtime"}, SettleDelay: time.Millisecond}
	if err := relayout.Fence(context.Background()); err != nil {
		t.Fatalf("Fence after a crash mid-fence = %v, want it to finish from the recorded ACL", err)
	}
	var advanced string
	for _, statement := range node.statements {
		if strings.Contains(statement, "RETURNING state") {
			advanced = statement
		}
	}
	if !strings.Contains(advanced, "source_acl = "+relayoutLiteral(sourceACL)) {
		t.Fatalf("fence advanced without the originally recorded ACL:\n%s", advanced)
	}
}

func TestFinishRefusesToDropADatabaseThatIsNotTheOriginal(t *testing.T) {
	journal := relayoutJournalRow(string(RelayoutAccessRestored), "unit", "2099-01-01", "{purser=CTc/purser}", "", "", "", "16384", "", "", "")
	for _, tc := range []struct {
		name, preOID, want string
	}{
		{"unrelated database under the pre-relayout name", "99999", "is not the original purser"},
		{"original database", "16384", "unexpected query"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			node := &sqlRecordingNode{routedRelayoutNode: routedRelayoutNode{name: "yuga-1", routes: []relayoutRoute{
				{"to_regclass", "t"},
				{"FROM public.frameworks_relayout_journal WHERE", journal},
				{"shobj_description(oid, 'pg_database') =", "0"},
				{"SELECT count(*) FROM pg_database WHERE datname = 'purser__pre_relayout'", "1"},
				{"SELECT oid::text FROM pg_database", tc.preOID},
			}}}
			relayout := &YugabyteRelayout{Primary: node, Database: "purser", LeaseOwner: "unit"}
			err := relayout.Finish(context.Background())
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Finish error = %v, want it to contain %q", err, tc.want)
			}
			if tc.preOID != "16384" {
				for _, statement := range node.statements {
					if strings.Contains(statement, "DROP DATABASE") || strings.Contains(statement, "pg_terminate_backend") {
						t.Fatalf("finish touched a database that is not the original: %s", statement)
					}
				}
			}
		})
	}
}

func TestRestoreSourceCommentReplacesOnlyTheCopyMarker(t *testing.T) {
	for _, tc := range []struct {
		name, marked, comment, want string
	}{
		{"marker replaced by the original comment", "1", "billing database", `COMMENT ON DATABASE "purser" IS 'billing database'`},
		{"marker cleared when the original had none", "1", "", `COMMENT ON DATABASE "purser" IS NULL`},
		{"already restored", "0", "billing database", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			node := &sqlRecordingNode{routedRelayoutNode: routedRelayoutNode{name: "yuga-1", routes: []relayoutRoute{
				{"shobj_description(oid, 'pg_database') =", tc.marked},
				{"COMMENT ON DATABASE", ""},
			}}}
			relayout := &YugabyteRelayout{Primary: node, Database: "purser"}
			if err := relayout.restoreSourceComment(context.Background(), &RelayoutRecord{SourceComment: tc.comment}); err != nil {
				t.Fatalf("restoreSourceComment: %v", err)
			}
			var comments []string
			for _, statement := range node.statements {
				if strings.HasPrefix(statement, "COMMENT ON DATABASE") {
					comments = append(comments, statement)
				}
			}
			switch {
			case tc.want == "" && len(comments) > 0:
				t.Fatalf("comment rewritten without a marker: %v", comments)
			case tc.want != "" && (len(comments) != 1 || comments[0] != tc.want):
				t.Fatalf("comment statements = %v, want %q", comments, tc.want)
			}
		})
	}
}

func TestRemoveStagedDumpRunsOnRecordedHost(t *testing.T) {
	primary := &routedRelayoutNode{name: "yuga-1"}
	holder := &routedRelayoutNode{name: "yuga-2"}
	relayout := &YugabyteRelayout{Primary: primary, Nodes: []YugabyteNode{primary, holder}, Database: "purser", DumpDir: "/var/lib/frameworks/relayout"}
	record := &RelayoutRecord{DumpPath: "/var/lib/frameworks/relayout/purser/relayout", DumpHost: "yuga-2"}
	if err := relayout.removeStagedDump(context.Background(), record); err != nil {
		t.Fatalf("removeStagedDump: %v", err)
	}
	if len(primary.scripts) != 0 || len(holder.scripts) != 1 || !strings.Contains(holder.scripts[0], "rm -rf '/var/lib/frameworks/relayout/purser/relayout'") {
		t.Fatalf("dump removal ran on the wrong node: primary=%q holder=%q", primary.scripts, holder.scripts)
	}

	record.DumpHost = "yuga-9"
	if err := relayout.removeStagedDump(context.Background(), record); err == nil || !strings.Contains(err.Error(), "yuga-9, which is not a relayout node") {
		t.Fatalf("removeStagedDump on an unknown host = %v", err)
	}
	for _, outside := range []string{
		"/etc",
		"/var/lib/frameworks/relayout/purser/../quartermaster",
		"/var/lib/frameworks/relayout/purser/relayout/../../quartermaster/relayout",
		"/var/lib/frameworks/relayout/purser/",
		"/var/lib/frameworks/relayout/purser/.",
		"var/lib/frameworks/relayout/purser/relayout",
	} {
		record.DumpHost, record.DumpPath = "yuga-2", outside
		if err := relayout.removeStagedDump(context.Background(), record); err == nil || !strings.Contains(err.Error(), "refusing to remove it") {
			t.Fatalf("removeStagedDump(%q) = %v, want a refusal", outside, err)
		}
	}
	if got := len(holder.scripts); got != 1 {
		t.Fatalf("refused removals ran %d scripts on the holder, want only the first valid removal", got)
	}
}

func TestRelayoutsInProgressListsFencedDatabases(t *testing.T) {
	node := &sqlRecordingNode{routedRelayoutNode: routedRelayoutNode{name: "yuga-1", routes: []relayoutRoute{
		{"to_regclass", "t"},
		{"FROM public.frameworks_relayout_journal", "purser (renamed_old)\nquartermaster (planned)"},
	}}}
	got, err := RelayoutsInProgress(context.Background(), node)
	if err != nil || strings.Join(got, ",") != "purser (renamed_old),quartermaster (planned)" {
		t.Fatalf("RelayoutsInProgress = %v, %v", got, err)
	}
	query := statementsContaining(node.statements, "FROM public.frameworks_relayout_journal")[0]
	if !strings.Contains(query, "state NOT IN ('planned', 'rolled_back', 'finished') OR source_oid IS NOT NULL") {
		t.Fatalf("in-progress query does not cover interrupted fences: %s", query)
	}
	none := &routedRelayoutNode{name: "yuga-1", routes: []relayoutRoute{{"to_regclass", "f"}}}
	if got, err := RelayoutsInProgress(context.Background(), none); err != nil || len(got) != 0 {
		t.Fatalf("no journal: %v, %v", got, err)
	}
}

// TestAcquireEndsTheExpiredOwnersSessionsBeforeTakingTheLease covers a relayout invocation that died with a restore
// still running on a tserver: its lease expires, but its client keeps working under its application name. The next
// owner must end those sessions and prove them gone before the journal names it, and must leave a live lease alone.
func TestAcquireEndsTheExpiredOwnersSessionsBeforeTakingTheLease(t *testing.T) {
	stale := RelayoutApplicationName("old-owner")
	for _, tc := range []struct {
		name, holder string
		takes        bool
	}{
		// A boolean cast to text reads true or false, not psql's t or f.
		{"expired lease", "old-owner|true", true},
		{"live lease", "old-owner|false", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			node := &sqlRecordingNode{routedRelayoutNode: *tserverNode("yuga-1", "10.0.0.1",
				relayoutRoute{"(lease_expires_at < now())::text", tc.holder},
				relayoutRoute{"to_regclass", "t"},
				relayoutRoute{"FROM public.frameworks_relayout_journal WHERE", relayoutJournalRow(string(RelayoutPrepared), "new-owner", "2099-01-01", "", "", "", "", "", "", "", "")},
				relayoutRoute{"yb_servers()", "10.0.0.1"},
				relayoutRoute{"pg_terminate_backend", "1"},
				relayoutRoute{"FROM pg_stat_activity", ""},
				relayoutRoute{"RETURNING database_name", "purser"},
			)}
			relayout := &YugabyteRelayout{Primary: node, Database: "purser", LeaseOwner: "new-owner", YSQLHost: "127.0.0.1", YSQLPort: 5433,
				DumpDir: "/var/lib/frameworks/relayout", SettleDelay: time.Millisecond}
			_, err := relayout.Acquire(context.Background())
			terminateAt, claimAt := -1, -1
			for i, statement := range node.statements {
				if terminateAt < 0 && strings.Contains(statement, "pg_terminate_backend") && strings.Contains(statement, "application_name = '"+stale+"'") {
					terminateAt = i
				}
				if claimAt < 0 && strings.Contains(statement, "SET lease_owner = 'new-owner'") {
					claimAt = i
				}
			}
			if !tc.takes {
				if err == nil || terminateAt >= 0 || claimAt >= 0 {
					t.Fatalf("Acquire of a live lease = %v, terminated at %d, claimed at %d; want a refusal that touches nothing", err, terminateAt, claimAt)
				}
				return
			}
			if err != nil {
				t.Fatalf("Acquire of an expired lease: %v", err)
			}
			if terminateAt < 0 || claimAt < 0 || terminateAt > claimAt {
				t.Fatalf("stale sessions ended at statement %d, lease claimed at %d; the old owner's work must end first:\n%s", terminateAt, claimAt, strings.Join(node.statements, "\n---\n"))
			}
			if !strings.Contains(node.statements[claimAt], "lease_owner = 'old-owner' AND (lease_expires_at < now())") {
				t.Fatalf("claim does not require the old owner's expired lease: %s", node.statements[claimAt])
			}
		})
	}
}

func TestRelayoutApplicationNameIsPerOwnerAndFitsPostgres(t *testing.T) {
	first, second := RelayoutApplicationName("host:1:aa"), RelayoutApplicationName("host:2:bb")
	if first == second || !strings.HasPrefix(first, "frameworks-relayout-") || len(first) > 63 {
		t.Fatalf("application names %q and %q must differ, carry the relayout prefix, and fit 63 bytes", first, second)
	}
}

// TestReleaseAfterFailureKeepsTheLeaseUntilItsWorkIsStopped covers a step that failed with a script still running on
// a tserver. The lease is released only once that work is proved stopped; otherwise it keeps naming this owner, so the
// next invocation's takeover ends the work before acting.
func TestReleaseAfterFailureKeepsTheLeaseUntilItsWorkIsStopped(t *testing.T) {
	for _, tc := range []struct {
		name      string
		remaining string
		releases  bool
	}{
		{"work stopped", "remaining=0", true},
		{"work still running", "remaining=1", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			node := &sqlRecordingNode{routedRelayoutNode: *tserverNode("yuga-1", "10.0.0.1",
				relayoutRoute{"yb_servers()", "10.0.0.1"},
				relayoutRoute{"pg_terminate_backend", "0"},
				relayoutRoute{"FROM pg_stat_activity", ""},
				relayoutRoute{"SET lease_owner = NULL", ""},
				relayoutRoute{"to_regclass", "t"},
				relayoutRoute{"FROM public.frameworks_relayout_journal WHERE", relayoutJournalRow(string(RelayoutFenced), "unit:release", "2099-01-01", "", "", "", "", "", "", "", "")},
				relayoutRoute{"RETURNING database_name", "purser"},
			)}
			node.shellOut = "yuga-1\n10.0.0.1\n" + tc.remaining + "\n"
			relayout := &YugabyteRelayout{Primary: node, Database: "purser", LeaseOwner: "unit", YSQLHost: "127.0.0.1", YSQLPort: 5433,
				DumpDir: "/var/lib/frameworks/relayout", SettleDelay: time.Millisecond}
			err := relayout.ReleaseAfterFailure(context.Background(), "unit:release")
			released := len(statementsContaining(node.statements, "SET lease_owner = NULL")) > 0
			if claims := statementsContaining(node.statements, "SET lease_owner = 'unit:release'"); tc.releases && (len(claims) == 0 || !strings.Contains(claims[0], "lease_owner = 'unit'")) {
				t.Fatalf("the successor did not take the lease over from the failed owner: %v", claims)
			}
			if released != tc.releases || (err == nil) != tc.releases {
				t.Fatalf("released=%v err=%v, want released=%v", released, err, tc.releases)
			}
			stopped := false
			for _, script := range node.scripts {
				if strings.Contains(script, relayoutWorkerRoot+"/"+RelayoutApplicationName("unit")) && strings.Contains(script, "kill -s") {
					stopped = true
				}
			}
			if !stopped {
				t.Fatal("the owner's registered workers were not stopped before releasing")
			}
		})
	}
}

// unreachableShellNode fails every shell script, as a host whose SSH does not answer.
type unreachableShellNode struct{ routedRelayoutNode }

func (n *unreachableShellNode) Shell(context.Context, string) (string, error) {
	return "", fmt.Errorf("%s: ssh: connect: no route to host", n.name)
}

// TestTakeoverStopsWorkersOnHostsWhoseTServerIsDown covers a host whose tserver stopped while a stale script (a
// restore checksumming its dump) still runs there. The takeover must stop workers on that host too, and must refuse
// when it cannot reach the host, because the script would connect once YSQL returns.
func TestTakeoverStopsWorkersOnHostsWhoseTServerIsDown(t *testing.T) {
	stale := relayoutWorkerRoot + "/" + RelayoutApplicationName("old-owner")
	journalRoutes := []relayoutRoute{
		{"yb_servers()", "10.0.0.1"},
		{"pg_terminate_backend", "0"},
		{"FROM pg_stat_activity", ""},
		{"to_regclass", "t"},
		{"FROM public.frameworks_relayout_journal WHERE", relayoutJournalRow(string(RelayoutFenced), "new-owner", "2099-01-01", "", "", "", "", "", "", "", "")},
		{"RETURNING database_name", "purser"},
	}
	for _, reachable := range []bool{true, false} {
		t.Run(fmt.Sprintf("reachable=%v", reachable), func(t *testing.T) {
			live := &sqlRecordingNode{routedRelayoutNode: *tserverNode("yuga-1", "10.0.0.1", journalRoutes...)}
			var down YugabyteNode
			downScripts := func() []string { return nil }
			if reachable {
				node := tserverNode("yuga-2", "10.0.0.2")
				down = node
				downScripts = func() []string { return node.scripts }
			} else {
				down = &unreachableShellNode{routedRelayoutNode: *tserverNode("yuga-2", "10.0.0.2")}
			}
			relayout := &YugabyteRelayout{Primary: live, Nodes: []YugabyteNode{live, down}, Database: "purser", LeaseOwner: "new-owner",
				YSQLHost: "127.0.0.1", YSQLPort: 5433, DumpDir: "/var/lib/frameworks/relayout", SettleDelay: time.Millisecond}
			_, err := relayout.TakeOver(context.Background(), "old-owner")
			claimed := len(statementsContaining(live.statements, "SET lease_owner = 'new-owner'")) > 0
			if !reachable {
				if err == nil || claimed || !strings.Contains(err.Error(), "no route to host") {
					t.Fatalf("takeover with an unreachable host = %v, claimed=%v; want a refusal that keeps the old owner", err, claimed)
				}
				return
			}
			if err != nil || !claimed {
				t.Fatalf("takeover = %v, claimed=%v", err, claimed)
			}
			stopped := false
			for _, script := range downScripts() {
				if strings.Contains(script, stale) && strings.Contains(script, "kill -s") {
					stopped = true
				}
			}
			if !stopped {
				t.Fatal("the takeover did not stop workers on the host whose tserver is down")
			}
		})
	}
}
