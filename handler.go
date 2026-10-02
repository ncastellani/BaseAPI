package baseapi

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/textproto"
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
//   - Request ID: when the request went through API Gateway and carries
//     x-amzn-RequestId, that value is used verbatim as the request ID.
//     Otherwise it is generated locally as a 16-char random string, with
//     Fly-Request-Id overriding it when present, and wrapped into the
//     hostData correlation identifier. Either way traces can be correlated
//     across the edge and the application.
//   - Headers / Query: only the first value of each key is kept (the
//     library's parameter model is single-valued by design; multi-valued
//     form bodies still flow through the form parser, not the query map).
//     Header keys arrive canonicalized by net/http ("Authorization") and
//     are kept that way.
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

	// iterate over the headers to get the first value. the keys of an
	// http.Header are already in canonical form ("Authorization"), which is
	// the shape the rest of the library expects
	headers := make(map[string]string, len(e.Header))
	for k, v := range e.Header {
		headers[k] = v[0]
	}

	// prefer the API Gateway request ID, then the fly.io edge one, over the
	// locally generated request ID, and let fly.io override the IP taken
	// from RemoteAddr. Header.Get canonicalizes the name it is given, so the
	// lookup matches regardless of how the client spelled it
	requestID := baseutils.RandomString(16, true, true, true)
	gatewayID := false
	if v := e.Header.Get("X-Amzn-Requestid"); v != "" {
		requestID = v
		gatewayID = true
	} else if v := e.Header.Get("Fly-Request-Id"); v != "" {
		requestID = v
	}

	if v := e.Header.Get("Fly-Client-IP"); v != "" {
		ip = v
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

		gatewayID: gatewayID,
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
//   - Headers: the keys are canonicalized ("authorization" becomes
//     "Authorization"). API Gateway HTTP APIs lowercase every header name
//     they forward, REST APIs preserve the client's casing, so without this
//     step the same request would be seen differently by each of them.
//   - Request ID: the API Gateway request ID (the x-amzn-RequestId AWS
//     returns to the client), used verbatim. It comes from the request
//     context, falling back to an x-amzn-RequestId header when the context
//     has none; without either, a random ID wrapped into the hostData
//     correlation identifier is used, as in the net/http adapter.
//   - Input: the request body, Base64-decoded when API Gateway marks it
//     as such (binary payloads).
//   - ResultCode: pre-seeded to "OK" so the lifecycle starts in a good
//     state.
//
// Response-shaping details: all headers returned by HandleRequest are
// copied to the API Gateway response, and an extra `x-request-id` header
// is appended so the client can echo it back in support requests.
func HandleLambdaAPIGatewayRequests(ctx context.Context, e events.APIGatewayProxyRequest, api *API) (events.APIGatewayProxyResponse, error) {

	// canonicalize the header keys. API Gateway HTTP APIs lowercase every
	// header name before the event reaches the function, while REST APIs
	// keep the casing the client sent — normalizing here makes both flavours
	// (and the net/http adapter, whose keys are canonical already) agree
	headers := make(map[string]string, len(e.Headers))
	for k, v := range e.Headers {
		headers[textproto.CanonicalMIMEHeaderKey(k)] = v
	}

	// use the API Gateway request ID as is, so it matches x-amzn-RequestId
	requestID := e.RequestContext.RequestID
	if requestID == "" {
		requestID = headers["X-Amzn-Requestid"]
	}

	gatewayID := requestID != ""
	if !gatewayID {
		requestID = baseutils.RandomString(16, true, true, true)
	}

	// assemble the request
	r := Request{
		ID:      requestID,
		IP:      e.RequestContext.Identity.SourceIP,
		Headers: headers,
		Query:   e.QueryStringParameters,
		Method:  e.RequestContext.HTTPMethod,
		Values:  map[string]any{},

		// set the request result as OK
		ResultCode: "OK",
		ResultData: baseutils.Empty,

		gatewayID: gatewayID,
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
