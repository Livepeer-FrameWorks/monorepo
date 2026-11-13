//go:build ingress_verify

package agent

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	dnspb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/dns"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
)

func TestTenantAliasPrivateerDockerIngressLifecycle(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Fatal("hosted ingress qualification requires Linux on remotedev")
	}
	if _, err := exec.LookPath("flock"); err != nil {
		t.Fatal("reload helper requires flock")
	}
	image := os.Getenv("FRAMEWORKS_NGINX_TEST_IMAGE")
	if image == "" {
		t.Fatal("run through make verify-hosted-alias-ingress for the pinned image")
	}
	acme := aliasTestBundle(t, "t-acme", "acme", "v1")
	globex := aliasTestBundle(t, "t-globex", "globex", "v1")
	navigator := &fakeCertificateClient{tlsBundles: map[string]*dnspb.GetTLSBundleResponse{"tenant:t-acme": acme, "tenant:t-globex": globex}}
	aliases := &fakeAliasedTenantsClient{resp: &quartermasterpb.ListAliasedTenantsForClusterResponse{Tenants: []*quartermasterpb.AliasedTenantRef{
		{TenantId: "t-acme", Subdomain: "acme"}, {TenantId: "t-globex", Subdomain: "globex"},
	}}}
	agent, root, _ := newAliasTestAgent(t, foghornRegistry(), aliases, navigator)
	agent.registryClient = stableAliasRegistry()
	agent.aliasAppliedPath = filepath.Join(filepath.Dir(root), "applied")
	pending := func() {
		t.Helper()
		if err := agent.syncTenantAliasCertificates(); !errors.Is(err, ErrTenantAliasIngressPending) || agent.IsTenantAliasIngressApplied() {
			t.Fatalf("unapplied inventory was accepted: %v", err)
		}
	}
	pending()
	repo := aliasIngressRepository(t)
	container := fmt.Sprintf("fw-alias-ingress-%d", time.Now().UnixNano())
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(address)
	user := ""
	if os.Geteuid() == 0 {
		user = "user nobody root;\n"
	}
	config := user + fmt.Sprintf(`pid /tmp/nginx.pid;
error_log /dev/stderr warn;
events {}
http {
 access_log off;
 client_body_temp_path /tmp/client;
 proxy_temp_path /tmp/proxy;
 fastcgi_temp_path /tmp/fastcgi;
 uwsgi_temp_path /tmp/uwsgi;
 scgi_temp_path /tmp/scgi;
 map $ssl_server_name $fw_tenant_alias_bundle {
  default "";
  include /tls/tenant-alias/nginx.map;
 }
 server {
  listen 127.0.0.1:%s ssl;
  ssl_certificate /tls/tenant-alias/$fw_tenant_alias_bundle/tls.crt;
  ssl_certificate_key /tls/tenant-alias/$fw_tenant_alias_bundle/tls.key;
  location / { return 200 "playback ingress ready\n"; }
 }
}
`, port)
	configPath := filepath.Join(filepath.Dir(root), "nginx.conf")
	if err := os.WriteFile(configPath, []byte(config), 0644); err != nil {
		t.Fatal(err)
	}
	network := "host"
	if parent := os.Getenv("FRAMEWORKS_INGRESS_TEST_NETWORK_PARENT"); parent != "" {
		network = "container:" + parent
	}
	args := []string{"run", "-d", "--name", container, "--network", network, "--user", fmt.Sprintf("%d:%d", os.Geteuid(), os.Getegid()), "--cpus", "1", "--memory", "128m", "--pids-limit", "64", "--read-only", "--tmpfs", "/tmp", "--tmpfs", "/var/cache/nginx", "-v", aliasIngressHostPath(t, repo, root) + ":/tls:ro", "-v", aliasIngressHostPath(t, repo, configPath) + ":/etc/nginx/nginx.conf:ro", image, "nginx", "-g", "daemon off;"}
	t.Cleanup(func() { _, _ = dockerpg.CLI("rm", "-fv", container) })
	if output, err := dockerpg.CLI(args...); err != nil {
		t.Fatalf("start pinned ingress: %v %s", err, output)
	}
	helper := aliasIngressReloadHelper(t, repo, container, agent.aliasMapPath(), agent.aliasAppliedPath)
	apply := func() {
		t.Helper()
		if output, err := exec.CommandContext(t.Context(), "sh", helper).CombinedOutput(); err != nil {
			t.Fatalf("real ingress reload helper: %v %s", err, output)
		}
		if err := agent.syncTenantAliasCertificates(); err != nil || !agent.IsTenantAliasIngressApplied() {
			t.Fatalf("matching reload receipt not accepted: %v", err)
		}
	}
	roots := x509.NewCertPool()
	addTrust := func(bundle *dnspb.GetTLSBundleResponse) []byte {
		t.Helper()
		block, _ := pem.Decode([]byte(bundle.CertPem))
		if block == nil {
			t.Fatal("invalid fixture certificate")
		}
		leaf, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			t.Fatal(err)
		}
		roots.AddCert(leaf)
		return leaf.Raw
	}
	acmeLeaf, globexLeaf := addTrust(acme), addTrust(globex)
	served := func(sub string, expected []byte) error {
		conn, err := tls.DialWithDialer(&net.Dialer{Timeout: time.Second}, "tcp", address, &tls.Config{ServerName: "foghorn." + sub + ".cdn.example.test", RootCAs: roots, MinVersion: tls.VersionTLS12})
		if err != nil {
			return err
		}
		defer conn.Close()
		peers := conn.ConnectionState().PeerCertificates
		if len(peers) == 0 || !bytes.Equal(peers[0].Raw, expected) {
			return errors.New("wrong certificate generation served")
		}
		return nil
	}
	await := func(sub string, expected []byte) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for {
			err := served(sub, expected)
			if err == nil {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("verified fixture-CA SNI handshake %s: %v", sub, err)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	await("acme", acmeLeaf)
	apply()
	rotated := aliasTestBundle(t, "t-acme", "acme", "v2")
	navigator.tlsBundles["tenant:t-acme"] = rotated
	rotatedLeaf := addTrust(rotated)
	pending()
	await("acme", acmeLeaf)
	receipt, err := os.ReadFile(agent.aliasAppliedPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte("invalid_nginx_directive;\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.CommandContext(t.Context(), "sh", helper).CombinedOutput(); err == nil {
		t.Fatalf("failed nginx validation was acknowledged: %s", output)
	}
	after, err := os.ReadFile(agent.aliasAppliedPath)
	if err != nil || !bytes.Equal(receipt, after) {
		t.Fatalf("failed reload changed its receipt: %v", err)
	}
	pending()
	await("acme", acmeLeaf)
	if err := os.WriteFile(configPath, []byte(config), 0644); err != nil {
		t.Fatal(err)
	}
	apply()
	await("acme", rotatedLeaf)
	aliases.resp.Tenants = aliases.resp.Tenants[1:]
	pending()
	apply()
	await("globex", globexLeaf)
	deadline := time.Now().Add(10 * time.Second)
	for served("acme", rotatedLeaf) == nil {
		if time.Now().After(deadline) {
			t.Fatal("withdrawn alias still accepted TLS")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func aliasIngressRepository(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "config/infrastructure.yaml")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("repository root not found")
		}
		dir = parent
	}
}

func aliasIngressHostPath(t *testing.T, repo, path string) string {
	t.Helper()
	host := os.Getenv("FRAMEWORKS_INGRESS_TEST_HOST_REPO")
	if host == "" {
		return path
	}
	relative, err := filepath.Rel(repo, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		t.Fatalf("container fixture path is outside the shared repository: %s", path)
	}
	return filepath.Join(host, relative)
}

func aliasIngressReloadHelper(t *testing.T, repo, container, inventory, applied string) string {
	t.Helper()
	path := filepath.Join(repo, "ansible/collections/ansible_collections/frameworks/infra/roles/nginx/templates/frameworks-nginx-reload.sh.j2")
	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	before, branch, ok := strings.Cut(string(source), "{% if nginx_ingress_reload_mode == 'docker' %}")
	if !ok {
		t.Fatal("Docker ingress helper branch missing")
	}
	docker, rest, ok := strings.Cut(branch, "{% else %}")
	if !ok {
		t.Fatal("native helper branch missing")
	}
	_, after, ok := strings.Cut(rest, "{% endif %}")
	if !ok {
		t.Fatal("ingress helper branch end missing")
	}
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }
	rendered := strings.NewReplacer(
		"{{ (nginx_ingress_tls_root ~ '/tenant-alias/nginx.map') | quote }}", quote(inventory),
		"{{ nginx_tenant_alias_applied_path | quote }}", quote(applied),
		"{{ nginx_ingress_container_name | quote }}", quote(container),
	).Replace(before + docker + after)
	if strings.Contains(rendered, "{{") || strings.Contains(rendered, "{%") {
		t.Fatal("unrendered reload helper input")
	}
	destination := filepath.Join(filepath.Dir(applied), "reload-helper.sh")
	if err := os.WriteFile(destination, []byte(rendered), 0700); err != nil {
		t.Fatal(err)
	}
	return destination
}
