package config

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/servicedefs"
)

// portKeyPattern selects the keys whose default names a port or an address.
var portKeyPattern = regexp.MustCompile(`(PORT|ADDR|URL|UPSTREAM|BROKERS)`)

var numberToken = regexp.MustCompile(`\d+`)

// TestConfigDefaultsNameRegisteredPortsThroughServicedefs fails when a
// service's configuration default spells out a port servicedefs registers
// instead of naming it with an @port(<listener>) token, so a port number is
// declared once.
func TestConfigDefaultsNameRegisteredPortsThroughServicedefs(t *testing.T) {
	root := filepath.Join("..", "..")
	files, err := filepath.Glob(filepath.Join(root, "*", "internal", "appconfig", "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	blocks, err := filepath.Glob(filepath.Join(root, "pkg", "config", "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	files = append(files, blocks...)
	if len(files) < 10 {
		t.Fatalf("found only %d configuration files under %s", len(files), root)
	}
	registered := servicedefs.RegisteredPorts()
	fset := token.NewFileSet()
	var repeats []string
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(parsed, func(node ast.Node) bool {
			field, ok := node.(*ast.Field)
			if !ok || field.Tag == nil {
				return true
			}
			raw, err := strconv.Unquote(field.Tag.Value)
			if err != nil {
				return true
			}
			tag := reflect.StructTag(raw)
			def, hasDefault := tag.Lookup(TagDefault)
			if !hasDefault || !portKeyPattern.MatchString(tag.Get(TagEnv)) {
				return true
			}
			for _, number := range numberToken.FindAllString(portTokens.ReplaceAllString(def, ""), -1) {
				if port, err := strconv.Atoi(number); err == nil && registered[port] {
					repeats = append(repeats, fset.Position(field.Pos()).String()+": "+tag.Get(TagEnv)+" default "+strconv.Quote(def))
				}
			}
			return true
		})
	}
	for _, repeat := range repeats {
		t.Error(repeat)
	}
}

// TestPortTokensExpandToRegisteredListeners pins the @port token forms.
func TestPortTokensExpandToRegisteredListeners(t *testing.T) {
	for def, want := range map[string]string{
		"quartermaster:@port(quartermaster.grpc)":        "quartermaster:19002",
		":@port(foghorn.grpc)":                           ":18019",
		"http://localhost:@port(helmsman)":               "http://localhost:18007",
		"@port(helmsman.management)":                     "18017",
		"@port(decklog.metrics)":                         "18026",
		"kafka:@port(kafka)":                             "kafka:9092",
		"@port(mistserver.rtmp)":                         "1935",
		"http://skipper:@port(skipper)/mcp/spoke":        "http://skipper:18018/mcp/spoke",
		"@port(chatwoot.container)":                      "3000",
		"@port(network-observer.internal)":               "18034",
		"plain value without a token":                    "plain value without a token",
		"@servicedefs.http_port is only a whole default": "@servicedefs.http_port is only a whole default",
	} {
		got, err := ResolveDefault(FieldSpec{Env: "TEST_ADDR", Default: def, HasDefault: true}, "bridge")
		if err != nil || got != want {
			t.Errorf("ResolveDefault(%q) = %q, %v; want %q", def, got, err, want)
		}
	}
	if _, err := ResolveDefault(FieldSpec{Env: "TEST_ADDR", Default: "@port(nothing.here)", HasDefault: true}, "bridge"); err == nil {
		t.Error("an unknown listener resolved")
	}
}
