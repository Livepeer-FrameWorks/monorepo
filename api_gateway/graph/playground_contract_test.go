package graph

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/99designs/gqlgen/graphql"
	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/99designs/gqlgen/graphql/handler/transport"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/globalid"
	"github.com/sirupsen/logrus"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/parser"
	"github.com/vektah/gqlparser/v2/validator"

	"frameworks/api_gateway/graph/generated"
	"frameworks/api_gateway/internal/clients"
	"frameworks/api_gateway/internal/demo"
	"frameworks/api_gateway/internal/middleware"
	"frameworks/api_gateway/internal/resolvers"
)

const (
	demoStreamGlobalID = "U3RyZWFtOjVlZWRmZWVkLTExZmUtY2E1Ny1mZWVkLTExZmVjYTU3MDAwMQ=="
)

var (
	demoClipGlobalID    = globalid.Encode(globalid.TypeClip, "a1b2c3d4e5f6789012345678901234ab")
	demoClusterGlobalID = globalid.Encode(globalid.TypeCluster, demo.DemoMediaClusterID)
	demoNodeGlobalID    = globalid.Encode(globalid.TypeInfrastructureNode, "node_demo_us_west_01")
)

type gqlResponse struct {
	Data   json.RawMessage `json:"data"`
	Errors []struct {
		Message string          `json:"message"`
		Path    json.RawMessage `json:"path"`
	} `json:"errors"`
}

type playgroundTestHarness struct {
	handler http.Handler
	schema  *ast.Schema
}

func TestPlaygroundDemoFixtureIDsResolve(t *testing.T) {
	raw, err := globalid.DecodeExpected(demoStreamGlobalID, globalid.TypeStream)
	if err != nil {
		t.Fatalf("decode demo stream global ID: %v", err)
	}
	if raw != demo.DemoStreamID {
		t.Fatalf("demo stream global ID decodes to %q, want %q", raw, demo.DemoStreamID)
	}
	if got := base64.StdEncoding.EncodeToString([]byte(globalid.TypeStream + ":" + demo.DemoStreamID)); got != demoStreamGlobalID {
		t.Fatalf("hard-coded demo stream global ID drifted: got %q, want %q", got, demoStreamGlobalID)
	}
}

func TestPlaygroundCuratedTemplatesExecuteInDemoMode(t *testing.T) {
	repoRoot := findRepoRoot(t)
	operationsRoot := filepath.Join(repoRoot, "pkg", "graphql", "operations")
	fragments := readFragments(t, filepath.Join(operationsRoot, "fragments"))
	templates := readCatalogTemplatePaths(t, filepath.Join(repoRoot, "website_application", "src", "lib", "graphql", "services", "explorerCatalog.ts"))
	srv := newPlaygroundTestServer()

	for _, templatePath := range templates {
		file := filepath.Join(repoRoot, "pkg", "graphql", filepath.FromSlash(templatePath))
		source := stripClientDirectives(readFile(t, file))
		doc := parseQueryDocument(t, source, file)
		op := firstOperation(t, doc, file)
		query := appendRequiredFragments(t, source, fragments)
		vars := defaultVariables(op)

		t.Run(templatePath, func(t *testing.T) {
			if op.Operation == ast.Subscription {
				validateWithBridgeSchema(t, srv, query, vars, templatePath)
				return
			}

			resp := executeGraphQL(t, srv, query, vars)
			if len(resp.Errors) > 0 {
				t.Fatalf("demo execution returned GraphQL errors for %s:\n%s", templatePath, formatGraphQLErrors(resp.Errors))
			}
			if len(resp.Data) == 0 || bytes.Equal(resp.Data, []byte("null")) {
				t.Fatalf("demo execution returned no data for %s", templatePath)
			}
		})
	}
}

func readCatalogTemplatePaths(t *testing.T, catalogPath string) []string {
	t.Helper()
	source := readFile(t, catalogPath)
	matches := regexp.MustCompile(`templatePath:\s*"([^"]+\.gql)"`).FindAllStringSubmatch(source, -1)
	seen := make(map[string]bool)
	var paths []string
	for _, match := range matches {
		if len(match) < 2 || seen[match[1]] {
			continue
		}
		seen[match[1]] = true
		paths = append(paths, match[1])
	}
	if len(paths) == 0 {
		t.Fatalf("no catalog template paths found in %s", catalogPath)
	}
	sort.Strings(paths)
	return paths
}

func newPlaygroundTestServer() playgroundTestHarness {
	logger := logrus.New()
	logger.SetOutput(io.Discard)

	var complexity generated.ComplexityRoot
	SetupComplexity(&complexity)

	root := &Resolver{
		Resolver: &resolvers.Resolver{
			Clients: &clients.ServiceClients{},
			Logger:  logger,
		},
	}
	executable := generated.NewExecutableSchema(generated.Config{
		Resolvers:  root,
		Complexity: complexity,
	})
	srv := handler.New(executable)
	srv.AddTransport(transport.POST{})
	srv.SetRecoverFunc(func(ctx context.Context, err any) error {
		return fmt.Errorf("internal error: %v", err)
	})
	srv.AroundOperations(func(ctx context.Context, next graphql.OperationHandler) graphql.ResponseHandler {
		return next(resolvers.WithNodeHealthCache(ctx))
	})
	srv.AroundOperations(func(ctx context.Context, next graphql.OperationHandler) graphql.ResponseHandler {
		return next(resolvers.WithStoragePricingCache(ctx))
	})

	return playgroundTestHarness{handler: srv, schema: executable.Schema()}
}

func validateWithBridgeSchema(t *testing.T, srv playgroundTestHarness, query string, _ map[string]any, name string) {
	t.Helper()
	doc := parseQueryDocument(t, query, name)
	if errs := validator.ValidateWithRules(srv.schema, doc, nil); len(errs) > 0 {
		t.Fatalf("subscription validation returned unexpected errors for %s:\n%s", name, errs.Error())
	}
}

func executeGraphQL(t *testing.T, srv playgroundTestHarness, query string, vars map[string]any) gqlResponse {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"query":     query,
		"variables": vars,
	})
	if err != nil {
		t.Fatalf("marshal GraphQL request: %v", err)
	}
	ctx := demoPlaygroundContext(context.Background())
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/graphql", bytes.NewReader(body))
	ctx = context.WithValue(ctx, ctxkeys.KeyHTTPRequest, req)
	req = req.WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Demo-Mode", "true")

	rec := httptest.NewRecorder()
	srv.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GraphQL HTTP status %d: %s", rec.Code, rec.Body.String())
	}

	var resp gqlResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal GraphQL response: %v\n%s", err, rec.Body.String())
	}
	return resp
}

func demoPlaygroundContext(ctx context.Context) context.Context {
	user := &middleware.UserContext{
		UserID:      demo.DemoUserID,
		TenantID:    demo.DemoTenantID,
		Email:       "demo@example.test",
		Role:        "owner",
		Permissions: []string{"streams:read", "streams:write", "analytics:read", "billing:read", "billing:write", "infrastructure:write"},
	}
	ctx = context.WithValue(ctx, ctxkeys.KeyDemoMode, true)
	ctx = context.WithValue(ctx, ctxkeys.KeyDemoTenantID, demo.DemoTenantID)
	ctx = context.WithValue(ctx, ctxkeys.KeyDemoUserID, demo.DemoUserID)
	ctx = context.WithValue(ctx, ctxkeys.KeyReadOnly, true)
	ctx = context.WithValue(ctx, ctxkeys.KeyAuthType, "jwt")
	ctx = context.WithValue(ctx, ctxkeys.KeyUser, user)
	ctx = context.WithValue(ctx, ctxkeys.KeyUserID, demo.DemoUserID)
	ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, demo.DemoTenantID)
	ctx = context.WithValue(ctx, ctxkeys.KeyEmail, user.Email)
	ctx = context.WithValue(ctx, ctxkeys.KeyRole, user.Role)
	ctx = context.WithValue(ctx, ctxkeys.KeyPermissions, user.Permissions)
	return ctx
}

func findRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("get wd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "pkg", "graphql", "operations")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not locate repo root containing pkg/graphql/operations")
		}
		dir = parent
	}
}

func walkGraphQLFiles(t *testing.T, root string) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if strings.HasSuffix(entry.Name(), ".gql") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	sort.Strings(files)
	return files
}

func readFragments(t *testing.T, root string) map[string]string {
	t.Helper()
	fragments := make(map[string]string)
	for _, file := range walkGraphQLFiles(t, root) {
		source := stripClientDirectives(readFile(t, file))
		doc := parseQueryDocument(t, source, file)
		for _, def := range doc.Fragments {
			fragments[def.Name] = source
		}
	}
	return fragments
}

func appendRequiredFragments(t *testing.T, source string, fragments map[string]string) string {
	t.Helper()
	required := make(map[string]bool)
	var ordered []string
	var visit func(string)
	visit = func(fragmentName string) {
		if required[fragmentName] {
			return
		}
		fragmentSource, ok := fragments[fragmentName]
		if !ok {
			return
		}
		required[fragmentName] = true
		ordered = append(ordered, fragmentName)
		for _, nested := range fragmentSpreads(fragmentSource) {
			visit(nested)
		}
	}

	for _, name := range fragmentSpreads(source) {
		visit(name)
	}
	if len(ordered) == 0 {
		return source
	}

	var b strings.Builder
	b.WriteString(source)
	for _, name := range ordered {
		b.WriteString("\n\n")
		b.WriteString(fragments[name])
	}
	return b.String()
}

var fragmentSpreadRE = regexp.MustCompile(`\.\.\.(\w+)`)

func fragmentSpreads(source string) []string {
	matches := fragmentSpreadRE.FindAllStringSubmatch(source, -1)
	seen := make(map[string]bool)
	var names []string
	for _, match := range matches {
		if len(match) < 2 || seen[match[1]] {
			continue
		}
		seen[match[1]] = true
		names = append(names, match[1])
	}
	return names
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(source)
}

func stripClientDirectives(query string) string {
	result := query
	for _, directive := range []string{
		"paginate",
		"list",
		"prepend",
		"append",
		"allLists",
		"parentID",
		"loading",
		"required",
		"optimisticKey",
		"blocking",
		"cache",
		"mask_disable",
		"mask",
	} {
		pattern := regexp.MustCompile(`\s*@` + directive + `\b(?:\([^)]*\))?`)
		result = pattern.ReplaceAllString(result, "")
	}
	return result
}

func parseQueryDocument(t *testing.T, source string, name string) *ast.QueryDocument {
	t.Helper()
	doc, err := parser.ParseQuery(&ast.Source{Name: name, Input: source})
	if err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	return doc
}

func firstOperation(t *testing.T, doc *ast.QueryDocument, name string) *ast.OperationDefinition {
	t.Helper()
	for _, op := range doc.Operations {
		return op
	}
	t.Fatalf("no operation in %s", name)
	return nil
}

func defaultVariables(op *ast.OperationDefinition) map[string]any {
	vars := make(map[string]any)
	for _, def := range op.VariableDefinitions {
		vars[def.Variable] = defaultVariableValue(op.Name, def.Variable, def.Type)
	}
	return vars
}

func defaultVariableValue(opName string, name string, typ *ast.Type) any {
	typeName := typeName(typ)
	switch name {
	case "id":
		return defaultIDForOperation(opName)
	case "streamId":
		return demoStreamGlobalID
	case "stream":
		return demoStreamGlobalID
	case "contentId":
		return demo.DemoPlaybackID
	case "playbackId":
		return demo.DemoPlaybackID
	case "clusterId":
		return demoClusterGlobalID
	case "nodeId":
		return demoNodeGlobalID
	case "orchAddr":
		return "0x0000000000000000000000000000000000000001"
	case "conversationId":
		return "demo-conversation-1"
	case "uploadId":
		return "upload_demo_001"
	case "keyId":
		return "key_demo_001"
	case "dvrHash":
		return "dvr_demo_001"
	case "topupId":
		return "topup_demo_001"
	case "walletId":
		return "wallet_demo_001"
	case "tierId":
		return "starter"
	case "inviteToken":
		return "invite_demo_001"
	case "subscriptionId":
		return "sub_demo_001"
	case "title":
		return "Demo title"
	case "reason":
		return "Demo reason"
	case "limit":
		return 20
	case "offset":
		return 0
	case "first":
		return 50
	case "after":
		return nil
	case "before":
		return nil
	case "last":
		return nil
	case "days":
		return 7
	case "topN":
		return 10
	case "status":
		return nil
	case "eventType":
		return nil
	case "currency":
		return "EUR"
	case "transactionType":
		return nil
	case "serviceId":
		return nil
	case "artifactHash":
		return demo.DemoVodHash
	case "noCache":
		return false
	case "timeRange":
		return defaultTimeRange()
	case "page":
		return map[string]any{"first": 50}
	case "input":
		return defaultInputValue(typeName)
	case "ids":
		return []any{"report_demo_001"}
	default:
		return defaultValueForType(typ)
	}
}

func defaultIDForOperation(opName string) string {
	switch {
	case strings.Contains(opName, "Stream"):
		return demoStreamGlobalID
	case strings.Contains(opName, "Clip"):
		return demoClipGlobalID
	case strings.Contains(opName, "Vod"):
		return demo.DemoVodHash
	case strings.Contains(opName, "InfrastructureNode"):
		return demoNodeGlobalID
	case strings.Contains(opName, "Cluster"):
		return demoClusterGlobalID
	case strings.Contains(opName, "SigningKey"):
		return "signing_key_demo_001"
	case strings.Contains(opName, "APIToken"):
		return "api_token_demo_001"
	case strings.Contains(opName, "Conversation"):
		return "demo-conversation-1"
	case strings.Contains(opName, "SkipperReport"):
		return "report_demo_001"
	default:
		return demoStreamGlobalID
	}
}

func defaultInputValue(typeName string) any {
	switch typeName {
	case "CreateStreamInput":
		return map[string]any{
			"name":       "example-live-stream",
			"record":     false,
			"ingestMode": "PUSH",
		}
	case "UpdateStreamInput":
		return map[string]any{
			"name":       "example-live-stream-updated",
			"record":     false,
			"ingestMode": "PUSH",
		}
	case "CreateClipInput":
		return map[string]any{
			"streamId":    demoStreamGlobalID,
			"title":       "Example Clip",
			"description": "Example clip description",
			"mode":        "ABSOLUTE",
			"startUnix":   0,
			"stopUnix":    30,
		}
	case "CreateVodUploadInput":
		return map[string]any{
			"filename":    "example.mp4",
			"sizeBytes":   1048576,
			"contentType": "video/mp4",
			"title":       "Example VOD",
			"description": "Example VOD upload",
		}
	case "CompleteVodUploadInput":
		return map[string]any{
			"uploadId": "upload_demo_001",
			"parts": []any{
				map[string]any{"partNumber": 1, "etag": "etag-value"},
			},
		}
	case "CreateStreamKeyInput":
		return map[string]any{"name": "primary-key"}
	case "CreatePushTargetInput":
		return map[string]any{"platform": "custom", "name": "Backup CDN", "targetUri": "rtmp://rtmp.example.test/live/stream-key"}
	case "UpdatePushTargetInput":
		return map[string]any{"name": "Backup CDN", "targetUri": "rtmp://rtmp.example.test/live/stream-key", "isEnabled": true}
	case "CreateSigningKeyInput":
		return map[string]any{"name": "demo-player"}
	case "CreateDeveloperTokenInput":
		return map[string]any{"name": "example-api-token", "permissions": "read", "expiresIn": nil}
	case "CreateBootstrapTokenInput":
		return map[string]any{"name": "bootstrap-token", "kind": "cluster", "expiresIn": nil}
	case "SetPlaybackPolicyInput":
		return map[string]any{
			"streamId": demoStreamGlobalID,
			"policy": map[string]any{
				"type": "JWT",
				"jwt": map[string]any{
					"allowedKids":        []any{"kid_example"},
					"requiredAudience":   []any{"web-player"},
					"requiredClaimsJson": []any{},
				},
			},
		}
	case "TestPlaybackAccessInput":
		return map[string]any{
			"playbackId":  demo.DemoPlaybackID,
			"viewerToken": "",
			"viewerIp":    "203.0.113.10",
			"requestUrl":  "https://viewer.example.test/watch",
			"connector":   "hls",
			"sessionId":   "session_demo_001",
			"fireWebhook": false,
		}
	case "CreatePaymentInput":
		return map[string]any{"invoiceId": "inv_demo_current_001", "method": "CARD"}
	case "CreateCardTopupInput":
		return map[string]any{"amountCents": 2500, "currency": "EUR", "provider": "STRIPE", "successUrl": "https://example.test/success", "cancelUrl": "https://example.test/cancel"}
	case "CreateCryptoTopupInput":
		return map[string]any{"amountCents": 2500, "asset": "USDC", "currency": "EUR"}
	case "UpdateBillingDetailsInput":
		return map[string]any{"company": "Demo Company", "email": "billing@example.test"}
	case "CreateEdgeClusterInput":
		return map[string]any{"clusterName": "Amsterdam Edge", "shortDescription": "Self-hosted edge cluster"}
	case "UpdateClusterMarketplaceInput":
		return map[string]any{"shortDescription": "Low latency premium cluster", "pricingModel": "SUBSCRIPTION", "monthlyPriceCents": 5000}
	case "CreateClusterInviteInput":
		return map[string]any{"email": "operator@example.test"}
	case "SetMediaRetentionPolicyInput":
		return map[string]any{"targetType": "DVR", "days": 30, "clear": false}
	case "SetStreamRetentionOverridesInput":
		return map[string]any{"streamId": demoStreamGlobalID, "dvrRetentionDaysOverride": 30, "clipRetentionDaysOverride": 30}
	case "UpdateMediaRetentionInput":
		return map[string]any{"targetId": demo.DemoVodHash, "targetType": "VOD", "retentionDays": 30}
	case "ResetMediaRetentionOverrideInput":
		return map[string]any{"targetId": demo.DemoVodHash, "targetType": "VOD"}
	case "SetNodeModeInput":
		return map[string]any{"nodeId": demoNodeGlobalID, "mode": "NORMAL"}
	case "OpenMistAdminSessionInput":
		return map[string]any{"nodeId": demoNodeGlobalID}
	case "WalletLoginInput":
		return map[string]any{"address": "0x0000000000000000000000000000000000000001", "signature": "demo", "message": "demo"}
	case "LinkEmailInput":
		return map[string]any{"email": "demo@example.test", "password": "demo-password"}
	case "CreateConversationInput":
		return map[string]any{"subject": "Demo conversation", "message": "Hello from demo mode"}
	case "SendMessageInput":
		return map[string]any{"conversationId": "demo-conversation-1", "content": "Hello from demo mode"}
	case "SkipperChatInput":
		return map[string]any{"message": "Summarize stream health", "conversationId": nil}
	default:
		if strings.HasSuffix(typeName, "Input") {
			return map[string]any{}
		}
		return nil
	}
}

func defaultValueForType(typ *ast.Type) any {
	if typ == nil {
		return nil
	}
	if typ.Elem != nil {
		return []any{}
	}
	switch typ.NamedType {
	case "ID":
		return demoStreamGlobalID
	case "String":
		return ""
	case "Int":
		return 0
	case "Float":
		return 0.0
	case "Boolean":
		return false
	case "Time", "DateTime":
		return time.Now().UTC().Format(time.RFC3339)
	default:
		return defaultInputValue(typ.NamedType)
	}
}

func defaultTimeRange() map[string]any {
	now := time.Now().UTC()
	return map[string]any{
		"start": now.Add(-24 * time.Hour).Format(time.RFC3339),
		"end":   now.Format(time.RFC3339),
	}
}

func typeName(typ *ast.Type) string {
	if typ == nil {
		return ""
	}
	if typ.NamedType != "" {
		return typ.NamedType
	}
	return typeName(typ.Elem)
}

func mustRel(t *testing.T, base string, target string) string {
	t.Helper()
	rel, err := filepath.Rel(base, target)
	if err != nil {
		t.Fatalf("rel %s from %s: %v", target, base, err)
	}
	return rel
}

func formatGraphQLErrors(errors []struct {
	Message string          `json:"message"`
	Path    json.RawMessage `json:"path"`
}) string {
	var b strings.Builder
	for i, err := range errors {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString("- ")
		b.WriteString(strconv.Quote(err.Message))
		if len(err.Path) > 0 {
			b.WriteString(" path=")
			b.Write(err.Path)
		}
	}
	return b.String()
}
