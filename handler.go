package baseapi

import (
	"io"
	"net/http"
	"strings"

	"github.com/ncastellani/baseutils"
)

// HandleHTTPServerRequests is the net/http adapter. Mount it under the
// catch-all path of any http.ServeMux (or framework router) and it will
// translate the request into a baseapi.Request, run the lifecycle and
// write the response back.
//
// Request-shaping details:
//
//   - Path: stripped of the leading slash; the empty path becomes "index"
//     so the mandatory index route serves "/".
//   - IP: taken from RemoteAddr, but Fly-Client-IP overrides it when
//     present (transparent support for fly.io's edge).
//   - Request ID: generated locally as a 16-char random string, but
//     Fly-Request-Id overrides it when present so traces can be correlated
//     across the edge and the application.
//   - Headers / Query: only the first value of each key is kept (the
//     library's parameter model is single-valued by design; multi-valued
//     form bodies still flow through the form parser, not the query map).
//   - ResultCode: pre-seeded to "OK" so the lifecycle starts in a good
//     state.
//
// Response-shaping details: all headers returned by HandleRequest are
// copied to the writer, and an extra `x-request-id` header is appended so
// the client can echo it back in support requests.
func HandleHTTPServerRequests(w http.ResponseWriter, e *http.Request, api *API) {

	// parse the path for getting the resource
	path := "index"
	if e.URL.Path != "/" {
		path = e.URL.Path[1:]
	}

	// get the request input body
	input, _ := io.ReadAll(e.Body)

	// get the IP from request
	ip := strings.Split(e.RemoteAddr, ":")[0]
	if strings.Contains(e.RemoteAddr, "[::1]") {
		ip = "127.0.0.1"
	}

	// iterate over the headers to get the first value
	requestID := baseutils.RandomString(16, true, true, true)
	headers := make(map[string]string)

	for k, v := range e.Header {
		headers[k] = v[0]
		switch k {
		case "Fly-Request-Id":
			requestID = headers[k]
		case "Fly-Client-IP":
			ip = headers[k]
		}
	}

	// iterate over the query string params to get the first value
	queryParams := make(map[string]string)
	for k, v := range e.URL.Query() {
		queryParams[k] = v[0]
	}

	// assemble the request
	r := Request{
		ID:      requestID,
		IP:      ip,
		Headers: headers,
		Query:   queryParams,
		Method:  e.Method,
		Path:    path,
		Input:   input,
		Context: map[string]any{},

		// set the request result as OK
		ResultCode: "OK",
		ResultData: baseutils.Empty,
	}

	// call the request handler
	code, content, headers := r.HandleRequest(api)

	// handle the headers
	headers["x-request-id"] = r.ID
	for k, v := range headers {
		w.Header().Set(k, v)
	}

	// return the response to the user
	w.WriteHeader(code)
	w.Write(content)

	r.Logger.Println("DONE!")
}
