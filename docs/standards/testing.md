# Testing Standards

This document defines the testing philosophy and practices for FrameWorks.

## Core Principle: Quality Over Quantity

**Do not chase line coverage numbers.** A line executed once is not a line tested. Focus on:

1. **Branch coverage** - test all decision paths (if/else, switch cases)
2. **Mutation score** - would your tests catch an introduced bug?
3. **Edge cases** - empty inputs, nil values, boundary conditions

## Testing Pyramid

```
        /\
       /  \  E2E (few)
      /----\
     /      \ Integration (some)
    /--------\
   /          \ Unit (many)
  /------------\
```

| Layer           | Purpose                                | Speed      | When to Use                                        |
| --------------- | -------------------------------------- | ---------- | -------------------------------------------------- |
| **Unit**        | Test individual functions in isolation | Fast (ms)  | Pure functions, business logic, calculations       |
| **Integration** | Test component interactions            | Medium (s) | Handler→gRPC flow, resolver wiring, query building |
| **E2E**         | Test full user flows                   | Slow (min) | Critical paths only (billing, auth)                |

## What to Test

### High Priority (unit + mutation testing)

- **Security-critical code**: `pkg/auth/`, error sanitization, JWT validation
- **Money-critical code**: `pkg/x402/`, billing calculations, usage metering
- **Business logic**: Stream routing, artifact lifecycle, rate limiting

### Medium Priority (unit tests)

- **Pure helper functions**: Formatters, validators, parsers
- **SQL/query builders**: Pagination cursors, keyset conditions
- **Data transformations**: Proto→model, model→response

### Low Priority (integration only)

- **Generated code**: Don't test `.pb.go` or `models_gen.go`
- **Simple wrappers**: Thin gRPC client methods with no logic
- **Demo mode generators**: Test fixtures, not production code

## What to Avoid

### Anti-Patterns

| Pattern                    | Problem                           | Fix                                           |
| -------------------------- | --------------------------------- | --------------------------------------------- |
| **Assertion-free tests**   | Code runs but nothing is verified | Add explicit assertions for expected behavior |
| **Line coverage chasing**  | One execution path ≠ tested       | Test all branches and edge cases              |
| **Mocking everything**     | Mocks can hide real bugs          | Use real types where practical                |
| **Testing implementation** | Brittle, breaks on refactor       | Test behavior/contracts instead               |

### Bad Example

```go
// BAD: Executes code but doesn't verify anything
func TestHandler(t *testing.T) {
    handler := NewHandler(mockDB)
    handler.Process(input) // no assertion!
}
```

### Good Example

```go
// GOOD: Tests behavior with assertions
func TestHandler_Process(t *testing.T) {
    tests := []struct {
        name    string
        input   Input
        want    Output
        wantErr bool
    }{
        {"valid input", validInput, expectedOutput, false},
        {"empty input", Input{}, Output{}, true},
        {"nil field", Input{Field: nil}, Output{}, true},
    }
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            got, err := handler.Process(tt.input)
            if (err != nil) != tt.wantErr {
                t.Errorf("Process() error = %v, wantErr %v", err, tt.wantErr)
            }
            if !reflect.DeepEqual(got, tt.want) {
                t.Errorf("Process() = %v, want %v", got, tt.want)
            }
        })
    }
}
```

## Mutation Testing

Mutation testing validates test quality by asking: "If I introduce a bug, will my tests catch it?"

### How It Works

1. Tool mutates your code (changes `>` to `>=`, removes a line, etc.)
2. Runs your tests against the mutant
3. Reports whether tests caught the mutation

| Result          | Meaning                        | Action                        |
| --------------- | ------------------------------ | ----------------------------- |
| **KILLED**      | Tests caught the mutation      | Good - tests are effective    |
| **LIVED**       | Tests missed the mutation      | Bad - add assertions or tests |
| **NOT_COVERED** | No tests ran against this code | Add test coverage             |

### Running Mutation Tests

```bash
# Single package
./scripts/mutation-test.sh pkg/auth/

# All critical modules (auth, x402, errors, webhooks, middleware)
./scripts/mutation-test.sh --all

# Only packages changed vs main branch
./scripts/mutation-test.sh --changed

# Only packages changed in last 5 commits
./scripts/mutation-test.sh --changed HEAD~5

# Show help
./scripts/mutation-test.sh --help
```

**CI Integration:** Mutation tests run nightly on changed packages. Manual runs available via GitHub Actions with scope options: `changed`, `critical`, or `all`.

### Target Mutation Score

| Code Category                    | Target Score |
| -------------------------------- | ------------ |
| Security-critical (auth, errors) | > 80%        |
| Money-critical (billing, x402)   | > 80%        |
| Business logic                   | > 60%        |
| Utilities/helpers                | > 50%        |

## Test Organization

### File Naming

- `foo.go` → `foo_test.go` (same package)
- Use `_test` package suffix for black-box testing when appropriate

### Test Naming

```go
// Function tests
func TestFunctionName(t *testing.T) {}

// Method tests
func TestTypeName_MethodName(t *testing.T) {}

// Subtests for cases
func TestValidateInput(t *testing.T) {
    t.Run("empty string", func(t *testing.T) { ... })
    t.Run("valid input", func(t *testing.T) { ... })
}
```

### Table-Driven Tests

Prefer table-driven tests for functions with multiple cases:

```go
func TestSanitizeMessage(t *testing.T) {
    tests := []struct {
        name     string
        message  string
        fallback string
        allowed  []string
        want     string
    }{
        {"empty returns fallback", "", "default", nil, "default"},
        {"allowed passes through", "invalid email", "default", []string{"email"}, "invalid email"},
        {"disallowed returns fallback", "db error", "default", []string{"email"}, "default"},
    }
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            got := SanitizeMessage(tt.message, tt.fallback, tt.allowed)
            if got != tt.want {
                t.Errorf("got %q, want %q", got, tt.want)
            }
        })
    }
}
```

## Integration Tests

### When to Use

- Testing handler→gRPC client wiring
- Verifying SQL query building (not execution)
- Testing error propagation across layers

### Mock Patterns

Use interface-based mocking for dependencies:

```go
// Define interface for dependency
type BillingClient interface {
    GetTenantLimits(ctx context.Context, tenantID string) (*Limits, error)
}

// Production implementation
type grpcBillingClient struct { ... }

// Test mock
type mockBillingClient struct {
    limits *Limits
    err    error
}

func (m *mockBillingClient) GetTenantLimits(ctx context.Context, tenantID string) (*Limits, error) {
    return m.limits, m.err
}
```

## CI Integration

### Required Checks

- `make test` - All unit tests pass
- `make lint` - Go + frontend lint checks pass

### Local CI Parity

- `make ci-local` - Run the main CI checks locally before pushing

### Optional Checks

- Mutation testing on changed files (PR gate)
- Full mutation testing (nightly)
- Branch coverage reports

## References

- [Martin Fowler: Practical Test Pyramid](https://martinfowler.com/articles/practical-test-pyramid.html)
- [Mutation Testing Guide](https://gremlins.dev/)
- [Go Testing Best Practices](https://go.dev/doc/tutorial/add-a-test)
