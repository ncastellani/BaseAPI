# BaseAPI

Small Go library for routing, parameter validation and request handling on
JSON / form-urlencoded HTTP APIs. Routes and result codes are declared
externally as JSON, the library validates the declarations at boot and
takes care of the request lifecycle so the application code only writes
business logic.

## Install

```sh
go get github.com/ncastellani/baseapi
```

Requires Go 1.26+ (uses the standard `slices` package).

## Quick start

```go
package main

import (
	"log"
	"net/http"
	"os"

	"github.com/ncastellani/baseapi"
)

func index(r *baseapi.Request) (any, string) {
	return map[string]string{"hello": "world"}, "OK"
}

func main() {
	methods := baseapi.Methods{
		"index": index,
	}

	// base logger — its writer, prefix and flags are preserved, and each
	// per-request logger appends a "[ID][Path] " suffix to the prefix. Use a
	// distinct prefix per API when several run in the same process.
	logger := log.New(os.Stdout, "[my-service] ", log.LstdFlags|log.Lmsgprefix)

	api, err := baseapi.NewAPI(
		"./config/routes.json", // route declarations
		"./config/codes.json",  // result code table
		methods,                // application dispatch table
		logger,                 // base logger
		false,                  // quietBoot: true hides the boot/config logs
		[]string{"my-service"}, // host data prefix for request IDs
	)
	if err != nil {
		panic(err)
	}

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		baseapi.HandleHTTPServerRequests(w, r, &api)
	})

	http.ListenAndServe(":8080", nil)
}
```

## Configuration files

### `routes.json`

Map of `path → method → resource`. Every resource must declare:

- `function` — key into the `Methods` map.
- `input_format` — `"json"` (associative JSON body) or `"form"`
  (`application/x-www-form-urlencoded`).
- `authentication` — when `true`, the request must carry an
  `Authorization: Bearer <token>` header.
- `setup_transaction` — advisory flag your `RequestPreMethod` middleware
  can use to decide when to open a DB transaction.
- `timeout` — deadline for the resource, in **milliseconds**. When greater
  than zero the request context is narrowed with `context.WithTimeout` before
  the middlewares and the resource function run. **The field is mandatory on
  every resource**: an absent `timeout` fails the boot, so opting out is an
  explicit `0` and never an oversight. A negative value is rejected too.
- `parameters` — list of declared parameters (may be empty).

Each parameter must declare:

- `name` — key the parser looks up.
- `kind` — `string`, `integer`, `float`, `enum`, `bool`, `array` or `map`.
- `get_from` — `"body"` or `"query"`.
- `required` — when `true`, an absent value is reported as missing.
- `max_length` — optional rune-count cap, only meaningful for `kind=string`.
- `options` — required (and only meaningful) for `kind=enum`.

The routes file **must** declare an `index` route with a `GET` method —
this is the route served at `/`.

#### Cross-field rules

These combinations are rejected at boot:

- `kind=map` with `get_from=query` (query strings have no native nesting).
- `kind=map` with `input_format=form` (form bodies have no native nesting).
- `kind=enum` with an empty `options` list.

#### Format / source matrix

| input_format | get_from | Behavior                                                     |
| ------------ | -------- | ------------------------------------------------------------ |
| json         | body     | Body parsed as JSON; value taken from the parsed map.        |
| json         | query    | Body still parsed as JSON; value taken from the query map.   |
| form         | body     | Body parsed as form-urlencoded; values are strings.          |
| form         | query    | Body parsed as form-urlencoded; value taken from the query.  |

When `input_format=form` and **no** parameter declares `get_from=body`,
the body is not parsed at all (skip the `url.ParseQuery` cost).

#### Type coercion for string sources

Form-urlencoded bodies and query strings are always strings. The validator
coerces them to the declared kind:

- `string`, `enum` — passed through.
- `integer` — `strconv.ParseInt(v, 10, 64)`.
- `float` — `strconv.ParseFloat(v, 64)`.
- `bool` — `true` / `false` / `1` / `0`, case-insensitive.
- `array` — repeated keys (`?tag=a&tag=b`) become `[]any` of strings;
  a single value becomes a one-element array.
- `map` — rejected at boot for these sources.

### `codes.json`

Map of `code → {status, message}`. Each entry has:

- `status` — HTTP status code returned to the client.
- `message` — map of language tag → localized message string.

The library reserves the following codes — they must all be declared in
`codes.json` or boot fails with `ErrNoRequiredCode`:

| Code   | When it is returned                                     |
| ------ | ------------------------------------------------------- |
| `OK`   | Successful response (default).                          |
| `I001` | Recovered panic in the lifecycle or in a resource func. |
| `I002` | Fallback when the resource returns an unknown code.     |
| `I003` | Resource function name not in the `Methods` map.        |
| `G001` | Path not found.                                         |
| `G002` | OPTIONS preflight on an unknown method.                 |
| `G003` | Method not declared on a known path.                    |
| `G004` | JSON body could not be decoded.                         |
| `G005` | One or more parameters missing or invalid.              |
| `G006` | `Authorization` header missing on a protected route.    |
| `G007` | `Authorization` header not in `Bearer <token>` format.  |
| `G008` | Form-urlencoded body could not be decoded.              |
| `G009` | Request cancelled — timed out or the client went away.  |

`G001`, `G002`, `G003`, `G006`, `G007` are emitted by the library but not
listed in `requiredCodes`; the framework still expects them when the
matching condition fires, so declare them too.

Sample files for both `routes.json` and `codes.json` live in
[`samples/`](samples/).

## Writing a resource function

```go
func sendEvent(r *baseapi.Request) (any, string) {
	queue := (*r.Parameters)["queue"].(string)
	payload := (*r.Parameters)["payload"].(map[string]any)

	// ... do the work ...

	return map[string]any{"queue": queue, "size": len(payload)}, "OK"
}
```

A resource function receives the fully-validated request and returns
`(data, code)`:

- `data` becomes the `data` field of the JSON response envelope.
- `code` is looked up in `codes.json` to determine the HTTP status and the
  localized message. Returning the empty string is treated as `"OK"`.

The validated parameters live in `*r.Parameters`. Their Go types follow
the declared `kind`:

- `string`, `enum` → `string`
- `integer` → `int64` (or any int / float that came clean from JSON)
- `float` → `float64`
- `bool` → `bool`
- `array` → `[]any`
- `map` → `map[string]any`

### Other request fields

Besides the parameters, the `Request` carries the raw call data the
lifecycle filled in — `r.ID` (correlation ID), `r.IP`, `r.Path`,
`r.Method`, `r.Headers`, `r.Query`, `r.Input` (the unparsed body),
`r.Token` (the bearer token, on authenticated resources) and `r.Agent`.

`r.Agent` is a plain `string` holding the `User-Agent` header, empty when
the client sent none. A client that sends the header with an empty value
is indistinguishable from one that omits it; on the rare occasion that
difference matters, read the header yourself — the raw map is right there:

```go
agent, sent := r.Headers["User-Agent"]
```

## Request context

Every request carries a `context.Context`. Read it with `r.Context()` — it is
never `nil` — and hand it to every call that takes one:

```go
func getWidget(r *baseapi.Request) (any, string) {
	var w Widget

	// guregu/dynamo v2, aws-sdk-go-v2, database/sql, net/http clients —
	// they all want the request context
	if err := table.Get("ID", (*r.Parameters)["id"]).One(r.Context(), &w); err != nil {
		// no cancellation check needed — see "Cancelled requests" below
		r.Logger.Printf("failed to fetch the widget [err: %v]", err)
		return nil, "I001"
	}

	return w, "OK"
}
```

Where the context comes from:

| Transport                         | Context source                                      |
| --------------------------------- | --------------------------------------------------- |
| `HandleHTTPServerRequests`        | the incoming `*http.Request` context — cancelled when the client disconnects |
| `HandleLambdaAPIGatewayRequests`  | the invocation context — carries the Lambda deadline |
| your own adapter                  | whatever you pass to `r.SetContext(ctx)`             |

The resource's `timeout` is then applied on top of it with
`context.WithTimeout`, so the effective deadline is always the **earlier** of
the two: a `timeout` longer than the remaining Lambda invocation time cannot
outlive the invocation, and a shorter one wins over it.

For AWS Lambda:

```go
lambda.Start(func(ctx context.Context, e events.APIGatewayProxyRequest) (events.APIGatewayProxyResponse, error) {
	return baseapi.HandleLambdaAPIGatewayRequests(ctx, e, &api)
})
```

### Cancelled requests

Cancellation is handled by the lifecycle, not by each resource function. As
long as you pass `r.Context()` down to the calls you make, you never have to
test for it:

- **Before the dispatch** — if the context is already done when the resource
  function is about to be called (the client gave up while the body was being
  parsed, or `RequestPreMethod` burned the budget), the function is **not
  called** and the request short-circuits with `G009`.
- **After the dispatch** — if the context is done and the resource function
  returned a **failure** code, that code is **relabelled `G009`**. The driver
  fails the call on a dead context, your function returns whatever error code
  it normally would, and the envelope reports the timeout it actually was —
  so cancellations never pollute your error codes or the metrics built on
  them.
- **Success is never relabelled** — a function that finished in time (or that
  deliberately ignored the context) keeps its `"OK"`.
- `RequestPostMethod` runs in every one of these cases, with the final code
  already in `r.ResultCode`, so it rolls back instead of committing and no
  transaction is left dangling.

`r.Canceled()` is there for the rare function that wants to branch on
cancellation itself — emit a different metric, skip a retry — not for
routine error handling.

Writing your own transport adapter? Build the `Request` and attach the
context before calling `HandleRequest`:

```go
r := baseapi.Request{ /* ID, IP, Headers, Query, Path, Method, Input... */ }
r.SetContext(ctx)

code, content, headers := r.HandleRequest(&api)
```

### Request-scoped values

`r.Values` (a `map[string]any`) is the free-form slot for request-scoped
application state — it is *not* a context. Prefer it for data your own
middlewares and resource functions share, and reserve `r.SetContext` for
values that have to travel into libraries that only accept a
`context.Context` (tracing spans, SDK middlewares).

## Application middlewares

`API` exposes two hooks:

- `RequestPreMethod(r *Request)` — runs after parameter parsing and before
  the resource function, but only when `r.ResultCode` is still `"OK"`.
  Use it to load the authenticated user from `r.Token`, open a DB
  transaction (and stash it in `r.DB`), inflate request-scoped state into
  `r.Values`, enrich the context through `r.SetContext`, etc.
- `RequestPostMethod(r *Request)` — runs unconditionally after the
  resource function, even after a recovered panic. Use it to commit /
  rollback the transaction based on `r.ResultCode`, emit metrics, etc.

Both default to no-ops; assign your own functions after `NewAPI` returns.

`r.DB` and `r.User` are both `any`: this library never reads them, never
opens a transaction and has no database dependency of its own. Your
middleware puts whatever it likes in there — a `*sql.Tx`, an ORM
transaction, a repository bundle — and asserts it back out on the way in.
The `setup_transaction` flag on a resource is advisory for exactly this.

`RequestPostMethod` runs while the request context is still in scope, which
means a cancelled request hands it a dead context. Cleanup work must not
inherit that cancellation — use `r.CleanupContext` for it:

```go
api.RequestPostMethod = func(r *baseapi.Request) {
	// detached from the request cancellation, with a deadline of its own
	ctx, cancel := r.CleanupContext(5 * time.Second)
	defer cancel()

	// r.DB is `any` — assert it back to whatever RequestPreMethod stored
	// there. *sql.Tx here, but the library does not care which type it is.
	tx, ok := r.DB.(*sql.Tx)
	if !ok {
		return // no transaction was opened for this resource
	}

	if r.ResultCode == "OK" {
		commit(ctx, tx)
	} else {
		rollback(ctx, tx)
	}
}
```

## Response envelope

Every response — including errors — has the same shape:

```json
{
  "id": "<base64 correlation id>",
  "code": "OK",
  "time": "2026-04-25T12:34:56.789Z",
  "message": { "en-us": "...", "pt-br": "..." },
  "data": { /* whatever the resource returned */ }
}
```

For `G005` (validation failure), `data` is `{ "missing": [...], "invalid": [...] }`
where each entry is the original `ResourceParameter` declaration, so the
client can render exactly which fields failed.

## Boot-time validation

`NewAPI` refuses to start if anything is off:

| Failure                                              | Error returned             |
| ---------------------------------------------------- | -------------------------- |
| Routes JSON does not parse                           | `ErrFailedToImportRoutes`  |
| Codes JSON does not parse                            | `ErrFailedToImportCodes`   |
| Missing `index` / `GET` route                        | `ErrNoIndexRoute`          |
| Required code missing in codes file                  | `ErrNoRequiredCode`        |
| Invalid `input_format`, HTTP method or function; missing or negative `timeout` | `ErrInvalidRoute` |
| Invalid parameter (kind, get_from, cross-field rule) | `ErrInvalidParameter`      |

This is by design: misconfigured routes should crash the service at boot,
not silently misbehave at request time.
