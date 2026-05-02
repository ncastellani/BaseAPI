package baseapi

import (
	"encoding/json"
	"io"
	"log"
	"os"
	"slices"
)

// API is the immutable runtime handle returned by NewAPI. It bundles the
// parsed route table, the result-code table, the application's method map
// and the I/O writer used by the per-request loggers.
//
// The two middleware fields (RequestPreMethod and RequestPostMethod) are
// the only fields that callers are expected to reassign after construction:
//
//   - RequestPreMethod runs after parameter parsing and before the resource
//     method, but only when the request still has ResultCode == "OK".
//     Typical use: load the authenticated user, open a DB transaction.
//   - RequestPostMethod runs unconditionally after the resource method
//     returns (or panics). Typical use: commit/rollback a transaction.
type API struct {
	writer   io.Writer
	methods  Methods
	hostData []string
	codes    map[string]Code
	routes   map[string]map[string]Resource

	// application middlewares
	RequestPreMethod  func(r *Request)
	RequestPostMethod func(r *Request)
}

// NewAPI parses the routes and codes JSON files at the given paths, wires up
// the application method map and returns a ready-to-serve API.
//
// This is a thin wrapper around NewAPIFromBytes — it reads both files from
// disk and forwards their contents. See NewAPIFromBytes for the full list
// of boot-time invariants and error semantics.
//
// Parameters:
//   - routes, codes: filesystem paths to the JSON config files.
//   - methods: map from resource function name to the Go function the
//     dispatcher should invoke for that resource.
//   - writer: destination for the boot logger and every per-request logger.
//   - hostData: prefix strings that get joined with a Unix timestamp and the
//     incoming request ID to form the per-request correlation identifier.
func NewAPI(routes, codes string, methods Methods, writer io.Writer, hostData []string) (API, error) {
	l := log.New(writer, "", log.LstdFlags|log.Lmsgprefix)

	l.Printf("reading routes JSON file from path [path: %v]", routes)
	routesJSON, err := os.ReadFile(routes)
	if err != nil {
		l.Printf("failed to read routes JSON file [err: %v]", err)
		return API{}, ErrFailedToImportRoutes
	}

	l.Printf("reading codes JSON file from path [path: %v]", codes)
	codesJSON, err := os.ReadFile(codes)
	if err != nil {
		l.Printf("failed to read codes JSON file [err: %v]", err)
		return API{}, ErrFailedToImportCodes
	}

	return NewAPIFromBytes(routesJSON, codesJSON, methods, writer, hostData)
}

// NewAPIFromBytes is the in-memory counterpart of NewAPI. It accepts the
// routes and codes JSON already loaded as byte slices, which is convenient
// for callers that embed the JSON files into the binary via the standard
// "embed" package.
//
// Boot-time invariants — any failure aborts construction with a non-nil err:
//
//  1. Both JSON payloads must parse (ErrFailedToImportRoutes /
//     ErrFailedToImportCodes).
//  2. The routes payload must declare an `index` route with a `GET` method
//     (ErrNoIndexRoute).
//  3. The codes payload must define every code in requiredCodes
//     (ErrNoRequiredCode).
//  4. Every route declaration must pass validateResource — well-formed
//     input_format, function name, parameter list and cross-field rules
//     (ErrInvalidRoute / ErrInvalidParameter).
func NewAPIFromBytes(routesJSON, codesJSON []byte, methods Methods, writer io.Writer, hostData []string) (api API, err error) {

	api.writer = writer
	api.methods = methods
	api.hostData = hostData

	// setup a boot debug logger
	l := log.New(writer, "", log.LstdFlags|log.Lmsgprefix)

	l.Println("setting up a new API handler...")

	// parse the routes JSON payload
	l.Println("parsing the routes JSON payload...")

	if err = json.Unmarshal(routesJSON, &api.routes); err != nil {
		l.Printf("failed to parse routes JSON [err: %v]", err)

		err = ErrFailedToImportRoutes
		return
	}

	// parse the codes JSON payload
	l.Println("parsing the codes JSON payload...")

	if err = json.Unmarshal(codesJSON, &api.codes); err != nil {
		l.Printf("failed to parse codes JSON [err: %v]", err)

		err = ErrFailedToImportCodes
		return
	}

	l.Println("configuration parsed and imported! validating minimum requirements...")

	// check if there is a index route and if it has a GET method
	if v, ok := api.routes["index"]; !ok {
		l.Println("no index route")

		err = ErrNoIndexRoute
		return
	} else {
		if _, ok := v["GET"]; !ok {
			l.Println("no index route")

			err = ErrNoIndexRoute
			return
		}
	}

	// check for the codes used at the lib
	for _, code := range requiredCodes {
		if _, ok := api.codes[code]; !ok {
			l.Printf("a required application code does not exist [code: %v]", code)

			err = ErrNoRequiredCode
			return
		}
	}

	l.Println("required index route and codes are available!")

	// validate every route declaration
	l.Println("validating route declarations...")

	for path, methods := range api.routes {
		for method, resource := range methods {
			if vErr := validateResource(l, path, method, resource); vErr != nil {
				err = vErr
				return
			}
		}
	}

	l.Println("all routes validated successfully!")

	// set defaults pre and post request method middlewares
	api.RequestPreMethod = func(r *Request) {}
	api.RequestPostMethod = func(r *Request) {}

	l.Println("successfully setted up this API handler!")

	return
}

// validateResource ensures a single resource declaration is well-formed.
//
// It checks the resource-level fields (input_format, HTTP method, function
// name) and then walks every declared parameter, validating the kind,
// get_from, enum options and the cross-field rules that the parser relies on
// at runtime:
//
//   - kind=map cannot be sourced from the query string (no native nesting).
//   - kind=map cannot live in a form-urlencoded body (no native nesting).
//   - kind=enum must declare at least one option.
//
// On the first failure, an error is returned (either ErrInvalidRoute or
// ErrInvalidParameter) and the caller is expected to abort the boot.
func validateResource(l *log.Logger, path, method string, r Resource) error {

	// input_format is required and must be one of the known parsers
	if r.InputFormat == "" {
		l.Printf("route is missing input_format [path: %v] [method: %v]", path, method)
		return ErrInvalidRoute
	}

	if !slices.Contains(validInputFormats, r.InputFormat) {
		l.Printf("route has invalid input_format [path: %v] [method: %v] [value: %v]", path, method, r.InputFormat)
		return ErrInvalidRoute
	}

	// method must be one of the standard verbs
	if !slices.Contains([]string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"}, method) {
		l.Printf("route has invalid HTTP method [path: %v] [method: %v]", path, method)
		return ErrInvalidRoute
	}

	// the function map key must be set, otherwise callMethod has nothing to dispatch
	if r.ResourceMethod == "" {
		l.Printf("route is missing function [path: %v] [method: %v]", path, method)
		return ErrInvalidRoute
	}

	// validate each parameter and ensure no duplicate names
	seenNames := map[string]bool{}

	for i, p := range r.Parameters {
		if p.Name == "" {
			l.Printf("parameter is missing name [path: %v] [method: %v] [index: %v]", path, method, i)
			return ErrInvalidParameter
		}

		if seenNames[p.Name] {
			l.Printf("parameter declared twice [path: %v] [method: %v] [param: %v]", path, method, p.Name)
			return ErrInvalidParameter
		}
		seenNames[p.Name] = true

		if !slices.Contains(validKinds, p.Kind) {
			l.Printf("parameter has invalid kind [path: %v] [method: %v] [param: %v] [kind: %v]", path, method, p.Name, p.Kind)
			return ErrInvalidParameter
		}

		if p.GetFrom == "" {
			l.Printf("parameter is missing get_from [path: %v] [method: %v] [param: %v]", path, method, p.Name)
			return ErrInvalidParameter
		}

		if !slices.Contains(validGetFrom, p.GetFrom) {
			l.Printf("parameter has invalid get_from [path: %v] [method: %v] [param: %v] [value: %v]", path, method, p.Name, p.GetFrom)
			return ErrInvalidParameter
		}

		// enum kind requires a non-empty option list, otherwise no value can match
		if p.Kind == "enum" && len(p.Options) == 0 {
			l.Printf("parameter is enum but has no options [path: %v] [method: %v] [param: %v]", path, method, p.Name)
			return ErrInvalidParameter
		}

		// map kind cannot live in query string (no native nesting)
		if p.Kind == "map" && p.GetFrom == "query" {
			l.Printf("parameter has kind=map with get_from=query, which is not supported [path: %v] [method: %v] [param: %v]", path, method, p.Name)
			return ErrInvalidParameter
		}

		// form-urlencoded does not support nested maps anywhere
		if r.InputFormat == "form" && p.Kind == "map" {
			l.Printf("parameter has kind=map with input_format=form, which is not supported [path: %v] [method: %v] [param: %v]", path, method, p.Name)
			return ErrInvalidParameter
		}
	}

	return nil
}
