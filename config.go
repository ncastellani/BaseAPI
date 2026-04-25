package baseapi

import "fmt"

// Boot-time errors returned by NewAPI. Each one corresponds to a single
// failure mode so the caller can branch on the exact cause if needed.
// Detailed context (path, method, parameter name, etc.) is always written
// to the configured writer before the error is returned.
var (
	// ErrFailedToImportRoutes — the routes JSON file failed to parse.
	ErrFailedToImportRoutes = fmt.Errorf("failed to import the routes JSON file. check the logs for more details")

	// ErrFailedToImportCodes — the codes JSON file failed to parse.
	ErrFailedToImportCodes = fmt.Errorf("failed to import the codes JSON file. check the logs for more details")

	// ErrNoIndexRoute — the routes file is missing the mandatory `index`
	// route or it does not declare a `GET` method.
	ErrNoIndexRoute = fmt.Errorf("there must be a index route with the GET method at the routes JSON file")

	// ErrNoRequiredCode — at least one entry in requiredCodes is missing
	// from the codes file.
	ErrNoRequiredCode = fmt.Errorf("a required application code is not set at the codes JSON file")

	// ErrInvalidRoute — a route declaration failed validation
	// (missing input_format, unknown HTTP method, missing function, etc.).
	ErrInvalidRoute = fmt.Errorf("a route is invalid. check the logs for more details")

	// ErrInvalidParameter — one of the parameters of a route failed
	// validation (unknown kind, missing get_from, invalid cross-field
	// combination, etc.).
	ErrInvalidParameter = fmt.Errorf("a parameter is invalid. check the logs for more details")
)

// requiredCodes lists the result codes the library itself emits. The
// codes JSON file must declare every one of them or NewAPI refuses to
// boot. Application-defined codes can live alongside these without
// restrictions.
var requiredCodes = []string{"OK", "I001", "I002", "I003", "G004", "G005", "G008"}

// validInputFormats are the accepted values for Resource.InputFormat.
var validInputFormats = []string{"json", "form"}

// validGetFrom are the accepted values for ResourceParameter.GetFrom.
var validGetFrom = []string{"body", "query"}

// validKinds are the accepted values for ResourceParameter.Kind.
var validKinds = []string{"string", "integer", "float", "enum", "bool", "array", "map"}
