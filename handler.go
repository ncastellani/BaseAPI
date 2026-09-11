package baseapi

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"strings"

	"github.com/aws/aws-lambda-go/events"
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
//   - Context: the incoming *http.Request context is attached to the
//     baseapi.Request, so resource methods get cancellation for free when
//     the client disconnects (see Request.Context).
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
		Values:  map[string]any{},

		// set the request result as OK
		ResultCode: "OK",
		ResultData: baseutils.Empty,
	}

	// bind the incoming request context so the lifecycle is cancelled when
	// the client goes away
	r.SetContext(e.Context())

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

// HandleLambdaAPIGatewayRequests is the AWS Lambda adapter for API Gateway
// (REST API, Lambda proxy integration) requests. Wire it as the Lambda
// handler and it will translate the event into a baseapi.Request, run the
// lifecycle and return the API Gateway proxy response.
//
// The ctx Lambda passes to the handler is attached to the request, so the
// invocation deadline (and anything else the runtime put in it, such as the
// lambdacontext data) reaches the resource methods through
// Request.Context. A resource `timeout` shorter than the remaining
// invocation time still wins; a longer one is capped by the deadline.
//
// Request-shaping details:
//
//   - Path: taken from the API Gateway request path, stripped of the
//     leading slash; "/" becomes "index" so the mandatory index route
//     serves the root.
//   - Input: the request body, Base64-decoded when API Gateway marks it
//     as such (binary payloads).
//   - ResultCode: pre-seeded to "OK" so the lifecycle starts in a good
//     state.
//
// Response-shaping details: all headers returned by HandleRequest are
// copied to the API Gateway response, and an extra `x-request-id` header
// is appended so the client can echo it back in support requests.
func HandleLambdaAPIGatewayRequests(ctx context.Context, e events.APIGatewayProxyRequest, api *API) (events.APIGatewayProxyResponse, error) {

	// assemble the request
	r := Request{
		ID:      e.RequestContext.RequestID,
		IP:      e.RequestContext.Identity.SourceIP,
		Headers: e.Headers,
		Query:   e.QueryStringParameters,
		Method:  e.RequestContext.HTTPMethod,
		Values:  map[string]any{},

		// set the request result as OK
		ResultCode: "OK",
		ResultData: baseutils.Empty,
	}

	// bind the invocation context so its deadline bounds the lifecycle
	r.SetContext(ctx)

	// parse the path for getting the action
	r.Path = "index"

	if e.Path != "/" {
		r.Path = e.Path[1:]
	}

	// get the request input body also handling Base64 encoded bodies
	if e.IsBase64Encoded {
		r.Input, _ = base64.StdEncoding.DecodeString(e.Body)
	} else {
		r.Input = []byte(e.Body)
	}

	// call the request handler
	code, content, headers := r.HandleRequest(api)

	r.Logger.Println("DONE!")

	// append the request ID
	headers["x-request-id"] = r.ID

	return events.APIGatewayProxyResponse{
		StatusCode: code,
		Headers:    headers,
		Body:       string(content),
	}, nil
}
