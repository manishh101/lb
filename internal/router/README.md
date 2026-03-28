# Package `router`

**Import path:** `intelligent-lb/internal/router`

The `router` package implements declarative HTTP request routing using a rule-based DSL. It evaluates incoming requests against priority-ordered routes and dispatches them to the matching service handler. The rule language is a Traefik-inspired expression parser that supports composable matchers.

---

## File Structure

```
router/
├── router.go       — Manager and Route structs, AddRoute, Route(req)
├── rule.go         — Lexer, parser, rule compiler (ParseRule)
├── matcher.go      — Matcher primitives: MatchPathPrefix, MatchPath, MatchMethod, MatchHeader, MatchClientIP, MatchAnd, MatchOr
├── matcher_test.go — Tests for each matcher type
├── router_test.go  — Integration tests for route matching and priority ordering
└── rule_test.go    — Tests for the rule expression parser
```

---

## `matcher.go` — Matcher Primitives

### `MatcherFunc` type
```go
type MatcherFunc func(req *http.Request) bool
```
The fundamental unit of routing. A `MatcherFunc` takes an HTTP request and returns `true` if the request satisfies the condition. All matchers, and their logical combinations, are represented as `MatcherFunc` values.

---

### Primitive Matchers

#### `MatchPathPrefix(prefix string) MatcherFunc`
```go
return func(req *http.Request) bool {
    return strings.HasPrefix(req.URL.Path, prefix)
}
```
Matches requests where the URL path **starts with** `prefix`. Example: `MatchPathPrefix("/api")` matches `/api`, `/api/users`, `/api/orders/1`, but not `/app`.

#### `MatchPath(path string) MatcherFunc`
Exact path match: `req.URL.Path == path`. Matches only requests with precisely that path.

#### `MatchMethod(method string) MatcherFunc`
HTTP method match. The method argument is uppercased at construction time (`strings.ToUpper(method)`) so `MatchMethod("get")` equals `MatchMethod("GET")`.

#### `MatchHeader(key, value string) MatcherFunc`
```go
return func(req *http.Request) bool {
    return req.Header.Get(key) == value
}
```
Matches requests where header `key` **exactly equals** `value`. Example: `MatchHeader("X-Priority", "HIGH")` routes high-priority requests to a different service.

#### `MatchClientIP(ipRange string) MatcherFunc`
Handles two formats:
1. **CIDR notation** (e.g., `"10.0.0.0/8"`): uses `net.ParseCIDR` to parse the network, then `ipNet.Contains(parsedIP)` to check membership.
2. **Exact IP** (e.g., `"192.168.1.5"`): uses `net.ParseIP` and `expectedIP.Equal(net.ParseIP(ip))`.
3. **Invalid input**: returns a matcher that always returns `false` (never panics).

Client IP is extracted via `getClientIP(req)`:
```go
func getClientIP(req *http.Request) string {
    if realIP := req.Header.Get("X-Real-IP"); realIP != "" { return realIP }
    if host, _, err := net.SplitHostPort(req.RemoteAddr); err == nil { return host }
    return req.RemoteAddr
}
```
Respects `X-Real-IP` set by the `RequestHeaders` middleware (the true client IP after proxy traversal).

---

### Composite Matchers

#### `MatchAnd(left, right MatcherFunc) MatcherFunc`
```go
return func(req *http.Request) bool { return left(req) && right(req) }
```
Short-circuits on `left == false` (doesn't evaluate `right`).

#### `MatchOr(left, right MatcherFunc) MatcherFunc`
```go
return func(req *http.Request) bool { return left(req) || right(req) }
```
Short-circuits on `left == true`.

These are used by the rule parser to build matcher trees from parsed expressions.

---

## `rule.go` — Rule Expression Parser

### Overview
`ParseRule(rule string) (MatcherFunc, error)` takes a rule expression string and compiles it into a `MatcherFunc` tree. The parser is a **hand-written recursive descent parser** with a custom lexer.

### Grammar
```
Expression := Term ( "||" Term )*
Term       := Factor ( "&&" Factor )*
Factor     := Matcher | "(" Expression ")"
Matcher    := Name "(" args ")"
```
This grammar correctly handles operator precedence: `&&` binds tighter than `||`, and parentheses can override precedence.

---

### Lexer

```go
type lexer struct {
    input string
    pos   int
}
```

#### Token Types
```go
const (
    TokenEOF      // end of input
    TokenAnd      // "&&"
    TokenOr       // "||"
    TokenLParen   // "("
    TokenRParen   // ")"
    TokenMatcher  // e.g., "PathPrefix('/api')"
)
```

#### `nextToken() (Token, error)`
The core of the lexer. Skips whitespace, then:
- Returns `TokenLParen` for `(`, `TokenRParen` for `)`.
- Returns `TokenAnd` for `&&`, `TokenOr` for `||`.
- If the current character is a letter (start of a matcher name), calls `readMatcher()`.

#### `readMatcher() (Token, error)`
Reads the full matcher token including its argument list. Handles **quoted strings** (`'` or `"`) so commas inside strings don't split the argument list:
- Tracks `inQuotes` state.
- Stops at `)` when not inside quotes — the closing paren of the matcher call.
- Returns the entire string `"PathPrefix('/api/users')"` as a `TokenMatcher`.

---

### Parser

```go
type parser struct {
    lex    *lexer
    curTok Token
}
```

Uses single-token lookahead (`curTok`). The `next()` method advances to the next token.

#### `parseExpression() (MatcherFunc, error)`
```go
m, _ := p.parseTerm()
for p.curTok.Type == TokenOr {
    p.next()
    right, _ := p.parseTerm()
    m = MatchOr(m, right)  // left-associative
}
return m, nil
```

#### `parseTerm() (MatcherFunc, error)`
```go
m, _ := p.parseFactor()
for p.curTok.Type == TokenAnd {
    p.next()
    right, _ := p.parseFactor()
    m = MatchAnd(m, right)  // left-associative
}
return m, nil
```

#### `parseFactor() (MatcherFunc, error)`
Handles either a grouped expression `(E)` or a single matcher token.

---

### Argument Parser: `parseArgs(s string) ([]string, error)`

Parses a comma-separated argument list with support for quoted strings:
- `'single quoted'` and `"double quoted"` strings are supported.
- Commas inside quotes do not split.
- Strips surrounding quotes from parsed arguments.
- Returns `nil` for empty input.

Example: `parseArgs("'/api', 'GET'")` → `["/api", "GET"]`

---

### Matcher Compiler: `createMatcher(s string) (MatcherFunc, error)`

Takes a full matcher token like `"PathPrefix('/api')"` and:
1. Finds the `(` to split name from args.
2. Calls `parseArgs()` on the args string.
3. Dispatches to the appropriate primitive matcher based on name.

| Matcher Name | Args Required | Maps To |
|---|---|---|
| `PathPrefix` | 1 | `MatchPathPrefix(args[0])` |
| `Path` | 1 | `MatchPath(args[0])` |
| `Method` | 1 | `MatchMethod(args[0])` |
| `Header` | 2 | `MatchHeader(args[0], args[1])` |
| `ClientIP` | 1 | `MatchClientIP(args[0])` |

Returns an error for unknown matcher names or wrong argument counts.

---

## `router.go` — Route Manager

### `Route` struct
```go
type Route struct {
    Name        string
    RuleString  string
    Priority    int
    Middlewares []string      // names of middlewares applied to this route
    Service     string        // target service name
    Match       MatcherFunc   // compiled rule expression
    Handler     http.Handler  // final composed handler (middlewares + proxy)
}
```

### `Manager` struct
```go
type Manager struct {
    routes []*Route
}
```
A sorted slice of `*Route`. Re-sorted after every `AddRoute`.

### `AddRoute(name, ruleStr, priority, middlewares, service, handler) error`

1. Calls `ParseRule(ruleStr)` to compile the rule string into a `MatcherFunc`. Returns an error if the rule is invalid.
2. Appends the new route to `m.routes`.
3. **Re-sorts** the entire slice by priority (descending). Tie-breaking by rule string length (longer string = higher specificity = evaluated first). This mirrors Traefik's deterministic route ordering.

```go
sort.Slice(m.routes, func(i, j int) bool {
    if m.routes[i].Priority == m.routes[j].Priority {
        return len(m.routes[i].RuleString) > len(m.routes[j].RuleString)
    }
    return m.routes[i].Priority > m.routes[j].Priority
})
```

### `Route(req *http.Request) *Route`
Linear scan of the sorted `routes` slice. Returns the **first** matching route or `nil`:
```go
for _, r := range m.routes {
    if r.Match(req) { return r }
}
return nil
```
Since routes are sorted by priority, the highest-priority match is always returned first.

---

## Complete Example: How a Request Gets Routed

Given config:
```json
"routers": {
  "admin": { "rule": "PathPrefix('/admin')", "priority": 100, "service": "admin-svc" },
  "api":   { "rule": "PathPrefix('/api')",   "priority": 50,  "service": "api-svc" },
  "catch": { "rule": "PathPrefix('/')",      "priority": 1,   "service": "default-svc" }
}
```

Request: `GET /admin/settings`
1. Routes sorted by priority: `admin(100) → api(50) → catch(1)`.
2. `admin.Match` → `PathPrefix("/admin")` → `true`.
3. Returns `admin` route — request goes to `admin-svc` handler.

Request: `GET /api/users`
1. `admin.Match` → `PathPrefix("/admin")` on `/api/users` → `false`.
2. `api.Match` → `PathPrefix("/api")` → `true`.
3. Returns `api` route.

---

## Dependencies

| Package | Role |
|---|---|
| `net/http` | `http.Request` for matching, `http.Handler` for dispatch |
| `net` | `net.ParseCIDR`, `net.ParseIP`, `net.SplitHostPort` for ClientIP matcher |
| `strings` | `strings.HasPrefix`, `strings.TrimSpace`, `strings.ToUpper` |
| `unicode` | `unicode.IsSpace`, `unicode.IsLetter` in lexer |
| `sort` | Re-sorting routes after each `AddRoute` |
| `fmt` | Error message formatting |
