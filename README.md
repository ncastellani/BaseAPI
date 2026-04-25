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

	api, err := baseapi.NewAPI(
		"./config/routes.json", // route declarations
		"./config/codes.json",  // result code table
		methods,                // application dispatch table
		os.Stdout,              // log writer
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

## Application middlewares

`API` exposes two hooks:

- `RequestPreMethod(r *Request)` — runs after parameter parsing and before
  the resource function, but only when `r.ResultCode` is still `"OK"`.
  Use it to load the authenticated user from `r.Token`, open a DB
  transaction (and stash it in `r.DB`), inflate request-scoped state into
  `r.Context`, etc.
- `RequestPostMethod(r *Request)` — runs unconditionally after the
  resource function, even after a recovered panic. Use it to commit /
  rollback the transaction based on `r.ResultCode`, emit metrics, etc.

Both default to no-ops; assign your own functions after `NewAPI` returns.

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
| Invalid `input_format`, HTTP method, or function     | `ErrInvalidRoute`          |
| Invalid parameter (kind, get_from, cross-field rule) | `ErrInvalidParameter`      |

This is by design: misconfigured routes should crash the service at boot,
not silently misbehave at request time.
