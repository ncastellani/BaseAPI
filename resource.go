package baseapi

import (
	"log"

	"github.com/pocketbase/dbx"
	"gopkg.in/guregu/null.v4"
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
//   - Agent: set by HandleRequest from the User-Agent header.
//   - Resource, Parameters: set by determineResource / parsePayload.
//   - DB, User, Context: free-form slots for application middlewares.
//   - ResultData, ResultCode: written by the resource method (or by an
//     earlier failing stage). Initialize ResultCode to "OK" when building
//     the request.
type Request struct {
	api    *API
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
	Agent   null.String

	// asserted data
	Resource   Resource
	Parameters *map[string]any

	// application data
	DB      *dbx.Tx
	User    any
	Context map[string]any

	// method response
	ResultData any
	ResultCode string
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
//   - Parameters: list of declared parameters; validated at boot.
type Resource struct {
	ResourceMethod   string              `json:"function"`          // application map into a API function
	InputFormat      string              `json:"input_format"`      // body parser to use (json/form)
	Authentication   bool                `json:"authentication"`    // if a Authorization header (bearer token) should be at the request
	SetupTransaction bool                `json:"setup_transaction"` // if a DB transaction must be open for requests on this resource
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
