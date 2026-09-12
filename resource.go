package baseapi

import (
	"context"
	"log"
	"time"
)

// Methods is the application-supplied dispatch table. The key is the
// `function` field of a resource declaration in routes.json; the value is
// the Go function the dispatcher invokes for that resource. Each function
// returns (data, code): the data is attached to the response envelope and
// the code is looked up in codes.json to determine the HTTP status and the
// localized message.
type Methods map[string]func(r *Request) (any, string)

// Code describes a single application result code as declared in
// codes.json. HTTPCode is the HTTP status returned to the client and
// Message holds localized strings keyed by language tag (e.g. "en-us",
// "pt-br").
type Code struct {
	HTTPCode int               `json:"status"`  // HTTP return code
	Message  map[string]string `json:"message"` // messages from the code
}

// Request is the per-call mutable state. Transport adapters build it,
// HandleRequest fills in the rest, and resource methods read the parsed
// data through it.
//
// Lifecycle ownership of fields:
//
//   - api, Logger, ID (final form): set by HandleRequest.
//   - IP, Headers, Query, Path, Method, Input: set by the transport adapter.
//   - Token: set by parseAuthentication when the resource requires auth.
//   - Agent: set by HandleRequest from the User-Agent header; empty when
//     the client sent none. r.Headers keeps the raw value either way.
//   - ctx: set by the transport adapter through SetContext and narrowed by
//     HandleRequest when the resource declares a timeout. Always read it
//     through the Context method, never assume the field is non-nil.
//   - Resource, Parameters: set by determineResource / parsePayload.
//   - DB, User, Values: free-form slots for application middlewares. DB
//     and User are untyped on purpose — this package never reads them,
//     so the application picks its own types and asserts them back out.
//   - ResultData, ResultCode: written by the resource method (or by an
//     earlier failing stage). Initialize ResultCode to "OK" when building
//     the request.
type Request struct {
	api    *API
	ctx    context.Context
	Logger *log.Logger

	// general request data
	ID      string
	IP      string
	Headers map[string]string
	Query   map[string]string
	Path    string
	Method  string
	Input   []byte
	Token   string
	Agent   string

	// asserted data
	Resource   Resource
	Parameters *map[string]any

	// application data
	DB     any
	User   any
	Values map[string]any

	// method response
	ResultData any
	ResultCode string
}

// Context returns the context that bounds this request. It is never nil:
// when no transport supplied one it falls back to context.Background().
//
// Pass it down to every call that accepts a context.Context — database
// drivers, HTTP clients, SDKs. It is cancelled when the client goes away
// (for transports that report it) and when the deadline declared by the
// resource's `timeout` elapses, whichever comes first.
func (r *Request) Context() context.Context {
	if r.ctx == nil {
		return context.Background()
	}

	return r.ctx
}

// SetContext replaces the context that bounds this request. Nil contexts are
// ignored so a careless caller can never strip the request of its context.
//
// Transport adapters call it while assembling the Request (this is the only
// way an adapter living outside this package can attach its context), and
// middlewares call it to enrich the context with request-scoped values such
// as the authenticated user or a tracing span.
func (r *Request) SetContext(ctx context.Context) {
	if ctx != nil {
		r.ctx = ctx
	}
}

// Canceled reports whether the request context is already done — the client
// disconnected or the resource's timeout elapsed. Resource methods can use
// it to tell "the database refused the write" from "we ran out of time"
// before deciding which result code to return.
func (r *Request) Canceled() bool {
	return r.Context().Err() != nil
}

// CleanupContext derives a context that is detached from the request's
// cancellation and carries its own deadline. Use it for the work that must
// run to completion even when the request itself was cancelled — committing
// or rolling back a transaction, releasing a lock, flushing an audit record
// — typically from RequestPostMethod.
//
// The returned cancel function must always be called; the request context's
// values (trace IDs and the like) are preserved.
func (r *Request) CleanupContext(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(r.Context()), d)
}

// Resource is one declared entry in routes.json — a (path, method) pair
// with all the metadata the dispatcher needs to validate the request and
// invoke the right application function.
//
// Field semantics:
//
//   - ResourceMethod: key into the application's Methods map; must be set.
//   - InputFormat: "json" (associative JSON body) or "form"
//     (application/x-www-form-urlencoded body). Required.
//   - Authentication: when true, parseAuthentication enforces the
//     `Authorization: Bearer <token>` header.
//   - SetupTransaction: advisory flag for the application's RequestPreMethod
//     middleware — the library itself does not open a transaction.
//   - Timeout: mandatory per-resource budget in milliseconds. It is a pointer
//     so boot can tell an absent declaration from an explicit zero: every
//     resource must spell it out, and 0 is the explicit "no deadline of our
//     own" (the transport's deadline, if any, still applies). When greater
//     than zero, HandleRequest narrows the request context with
//     context.WithTimeout before running the middlewares and the resource
//     method. Read it through TimeoutDuration instead of dereferencing it.
//   - Parameters: list of declared parameters; validated at boot.
type Resource struct {
	ResourceMethod   string              `json:"function"`          // application map into a API function
	InputFormat      string              `json:"input_format"`      // body parser to use (json/form)
	Authentication   bool                `json:"authentication"`    // if a Authorization header (bearer token) should be at the request
	SetupTransaction bool                `json:"setup_transaction"` // if a DB transaction must be open for requests on this resource
	Timeout          *int                `json:"timeout"`           // required context deadline for this resource, in milliseconds (0 = none)
	Parameters       []ResourceParameter `json:"parameters"`        // acceptable parameters for this action
}

// ResourceParameter is one declared parameter on a resource.
//
// Field semantics:
//
//   - Name: key the parser looks up in the body or query.
//   - Kind: target Go type. One of string/integer/float/enum/bool/array/map.
//   - GetFrom: source — "body" or "query". Each parameter has a single
//     source; precedence is body→ignored when GetFrom is "query".
//   - Required: when true, an absent value yields a "G005" failure.
//   - MaxLength: optional rune-count cap for kind=string.
//   - Options: required (and only meaningful) for kind=enum.
//
// Cross-field rules enforced at boot by validateResource:
//
//   - kind=map cannot be in the query string (no native nesting).
//   - kind=map cannot be in a form-urlencoded body (no native nesting).
//   - kind=enum must declare at least one option.
type ResourceParameter struct {
	Name      string   `json:"name"`       // parameter name
	Kind      string   `json:"kind"`       // parameter type (string/integer/float/enum/bool/array/map)
	GetFrom   string   `json:"get_from"`   // where to read this parameter from (body/query)
	Required  bool     `json:"required"`   // is required
	MaxLength int      `json:"max_length"` // if type STRING, validate its length
	Options   []string `json:"options"`    // if type ENUM, this is a list of the available options
}

// TimeoutDuration returns the resource's declared timeout as a duration, or
// zero when the resource opted out of having a deadline of its own.
//
// Boot rejects a resource whose timeout is absent or negative, so by the time
// a request is served the pointer is always set and non-negative; the guards
// here only keep a hand-built Resource from panicking.
func (r Resource) TimeoutDuration() time.Duration {
	if r.Timeout == nil || *r.Timeout <= 0 {
		return 0
	}

	return time.Duration(*r.Timeout) * time.Millisecond
}
