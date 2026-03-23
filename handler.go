package baseapi

import (
	"encoding/base64"
	"io"
	"net/http"
	"strings"

	"github.com/aws/aws-lambda-go/events"
	"github.com/ncastellani/baseutils"
)

// handle Go standard lib HTTP requests
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

// handle AWS API Gateway HTTP requests
func HandleLambdaAPIGatewayRequests(e events.APIGatewayProxyRequest, api *API) (events.APIGatewayProxyResponse, error) {

	// assemble the request
	r := Request{
		ID:      e.RequestContext.RequestID,
		IP:      e.RequestContext.Identity.SourceIP,
		Headers: e.Headers,
		Query:   e.QueryStringParameters,
		Method:  e.RequestContext.HTTPMethod,

		// set the request result as OK
		ResultCode: "OK",
		ResultData: baseutils.Empty,
	}

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
