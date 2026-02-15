package baseapi

import (
	"log"

	"gopkg.in/guregu/null.v4"
)

// resource function name into a application func map
type Methods map[string]func(r *Request) (any, string)

// define an operation result code, its messages by language and its HTTP code
type Code struct {
	HTTPCode int               `json:"status"`  // HTTP return code
	Message  map[string]string `json:"message"` // messages from the code
}

// define an incoming request, with its metadata, payload and useful contents.
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
	User    any
	AppData any

	// method response
	ResultData any
	ResultCode string
}

// define an API method within a route
type Resource struct {
	ResourceMethod string              `json:"function"`       // application map into a API function
	Authentication bool                `json:"authentication"` // if a Authorization header (bearer token) should be at the request
	Parameters     []ResourceParameter `json:"parameters"`     // acceptable parameters for this action
}

// define an parameter specification for the resource
type ResourceParameter struct {
	Name      string   `json:"name"`       // parameter name
	Kind      string   `json:"kind"`       // parameter type (string/number/enum)
	Required  bool     `json:"required"`   // is required
	Options   []string `json:"options"`    // if type ENUM, this is a list of the available options
	MaxLength int      `json:"max_length"` // !! if type STRING, validate its length
}
