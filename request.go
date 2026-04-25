package baseapi

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ncastellani/baseutils"
	"gopkg.in/guregu/null.v4"
)

// HandleRequest is the request lifecycle entry point. The transport adapter
// (HandleHTTPServerRequests, or any other) builds a Request, calls this
// method and forwards the returned HTTP code, body and headers back to the
// client.
//
// The lifecycle is:
//
//  1. Build the per-request correlation ID (hostData ++ unix-ts ++ raw ID),
//     base64-encode it and assemble the request logger.
//  2. Parse the User-Agent into r.Agent.
//  3. determineResource → match the route + method, populate r.Resource.
//  4. parseAuthentication → enforce the Bearer token if the resource declares
//     authentication=true.
//  5. parsePayload → decode and validate parameters into r.Parameters.
//  6. RequestPreMethod (only if ResultCode is still "OK") → application hook
//     for transaction setup, user loading, etc.
//  7. callMethod → dispatch to the resource function.
//  8. RequestPostMethod → unconditional cleanup hook (commit/rollback, etc.).
//  9. makeResponse → marshal the final envelope and return it.
//
// Panics raised anywhere inside the lifecycle are recovered: the result is
// rewritten to "I001" and a valid response is still returned, so the caller
// never has to deal with nil headers/body even after a recovered panic.
func (r *Request) HandleRequest(api *API) (code int, content []byte, headers map[string]string) {
	r.api = api

	// join the host data with the request ID
	requestHostData := make([]string, len(api.hostData))
	copy(requestHostData, api.hostData)

	requestHostData = append(requestHostData, fmt.Sprintf("%v", time.Now().Unix()))
	requestHostData = append(requestHostData, r.ID)

	r.ID = strings.Join(requestHostData, ":")

	// base64 encode the request ID
	r.ID = base64.StdEncoding.EncodeToString([]byte(r.ID))

	// assemble a logger
	r.Logger = log.New(r.api.writer, fmt.Sprintf("[%v][%v] ", r.ID, r.Path), log.LstdFlags|log.Lmsgprefix)

	// handle panic at request operators calls
	defer func() {
		if rcv := recover(); rcv != nil {
			r.Logger.Printf("request operator/method got in panic [err: %v]", rcv)

			r.ResultCode = "I001"
			r.ResultData = rcv

			// still produce a valid response so the outer HTTP handler does not
			// crash on nil headers; callers rely on these return values always
			// being non-nil, even after a panic was recovered
			code, content, headers = r.makeResponse()
		}
	}()

	// parse User-Agent header
	if agent, ok := r.Headers["User-Agent"]; ok {
		r.Agent = null.StringFrom(agent)
	}

	// call the request operators
	r.Logger.Printf("request recieved. handling... [method: %v] [IP: %v]", r.Method, r.IP)

	r.determineResource()
	r.parseAuthentication()
	r.parsePayload()

	// call the pre method middleware
	if r.ResultCode == "OK" {
		r.api.RequestPreMethod(r)
	}

	// call the API method
	r.callMethod()

	// always call the post method middleware so callers can perform cleanup
	// (such as commit/rollback of transactions) regardless of the result code
	r.api.RequestPostMethod(r)

	// assemble the response
	return r.makeResponse()
}

// makeResponse serializes the current request state into the JSON envelope
// the API always returns: {id, code, time, message, data}.
//
// If r.ResultCode is not present in the codes table, the envelope falls
// back to "I002" so the HTTP layer never panics on an unknown code (the
// resource's data is still attached, only the message/HTTPCode change).
//
// The CORS, cache-control and content-type headers are wide-open by design
// — this library targets pure JSON APIs that sit behind their own gateway.
func (r *Request) makeResponse() (int, []byte, map[string]string) {
	r.Logger.Printf("starting the response assemble... [code: %v]", r.ResultCode)

	// check if the response code exists and fetch its data
	code := r.api.codes["I002"]
	if v, ok := r.api.codes[r.ResultCode]; ok {
		code = v
	}

	// set the CORS, CACHE and content type headers
	var headers map[string]string = map[string]string{
		"Content-Type":                 "application/json; charset=utf-8",
		"Cache-Control":                "max-age=0,private,must-revalidate,no-cache",
		"Access-Control-Allow-Origin":  "*",
		"Access-Control-Allow-Methods": "*",
		"Access-Control-Allow-Headers": "*",
		"Access-Control-Max-Age":       "86400",
	}

	// assemble the request response with the code and provided data
	response := struct {
		ID      string            `json:"id"`
		Code    string            `json:"code"`
		Time    time.Time         `json:"time"`
		Message map[string]string `json:"message"`
		Data    any               `json:"data"`
	}{
		ID:      r.ID,
		Code:    r.ResultCode,
		Time:    time.Now(),
		Message: code.Message,
		Data:    r.ResultData,
	}

	// perform the JSON marshaling of the response
	content, _ := json.Marshal(response)

	r.Logger.Println("API response assembled. returning HTTP response...")

	return code.HTTPCode, content, headers
}

// determineResource matches r.Path and r.Method against the route table and
// stores the matching Resource on r.Resource.
//
// Result codes set by this stage:
//
//   - "G001" — path is unknown.
//   - "G002" — path exists but the method does not, and the request is an
//     OPTIONS preflight (returned with HTTP 202 so CORS handshakes succeed
//     even on resources that do not declare the verb explicitly).
//   - "G003" — path exists but the method is not declared.
//
// On success the result code is left untouched (the caller starts at "OK").
func (r *Request) determineResource() {

	// check for route existence
	if _, ok := r.api.routes[r.Path]; !ok {
		r.Logger.Printf("route not found [path: %v]", r.Path)

		r.ResultCode = "G001"
		return
	}

	// check for resource methods
	if v, ok := r.api.routes[r.Path][r.Method]; !ok {
		r.Logger.Printf("method not available for this route [method: %v]", r.Method)

		// return an OK response for OPTIONS verb validations
		if r.Method == "OPTIONS" {
			r.Logger.Printf("the current request is an OPTIONS check validation")

			r.ResultCode = "G002"
			return
		}

		r.ResultCode = "G003"
		return
	} else {
		r.Resource = v
	}

	r.Logger.Println("resource and method exists!")

}

// parseAuthentication enforces the `Authorization: Bearer <token>` header
// when the matched resource declares authentication=true.
//
// It only extracts and stores the raw token on r.Token — verifying the token
// (looking up the user, checking expiry, etc.) is the application's job and
// belongs in RequestPreMethod.
//
// Result codes set by this stage:
//
//   - "G006" — header is missing or empty.
//   - "G007" — header is present but not in the `Bearer <token>` form.
//
// No-ops when the result code is already non-OK or the resource is public.
func (r *Request) parseAuthentication() {
	if r.ResultCode != "OK" || !r.Resource.Authentication {
		return
	}

	r.Logger.Println("trying to fetch authentication token from the 'Authorization' header...")

	// check if the header Authorization was passed
	var token string

	if v, ok := r.Headers["Authorization"]; ok {
		token = v
	} else {
		r.Logger.Println("'Authorization' header is not present or does not holds any content")

		r.ResultCode = "G006"
		return
	}

	// get the second element of the authorization header
	authHeader := strings.Fields(token)
	if len(authHeader) == 1 {
		r.Logger.Println("the \"Authorization\" header is present but does not use the correct format")

		r.ResultCode = "G007"
		return
	}

	r.Token = authHeader[1]

	r.Logger.Printf("sucessfully obtained the authentication token [token: ...%v]", r.Token[len(r.Token)-4:])

}

// parsePayload is the parameter pipeline. It runs only when the request has
// reached this stage with a clean state and the resource declares at least
// one parameter.
//
// The pipeline has three steps, each in its own helper:
//
//  1. decodeBody — parse r.Input into a map according to InputFormat.
//  2. lookupParameter — for each declared parameter, fetch the raw value
//     from its declared source (body or query).
//  3. validateParameter — check the kind, convert string-typed inputs to
//     the declared Go type and apply max_length / enum option checks.
//
// Required parameters that are absent are collected in `missing`; parameters
// that fail validation are collected in `invalid`. If either list ends up
// non-empty the request short-circuits with "G005" and both lists are
// attached to the response data, so the client knows exactly what to fix.
//
// On success r.Parameters is set to the validated map, ready for the
// resource method to consume.
func (r *Request) parsePayload() {
	if r.ResultCode != "OK" || len(r.Resource.Parameters) == 0 {
		return
	}

	r.Logger.Println("starting the parse of the request payload...")

	// decode the body according to input_format
	bodyParameters, ok := r.decodeBody()
	if !ok {
		return
	}

	// validate every declared parameter
	parameters := make(map[string]any)

	var missing []ResourceParameter
	var invalid []ResourceParameter

	for _, v := range r.Resource.Parameters {
		rawValue, present := r.lookupParameter(v, bodyParameters)
		if !present {
			if v.Required {
				r.Logger.Printf("parameter missing [param: %v] [getFrom: %v]", v.Name, v.GetFrom)
				missing = append(missing, v)
			}
			continue
		}

		validatedValue, ok := r.validateParameter(v, rawValue)
		if !ok {
			invalid = append(invalid, v)
			continue
		}

		parameters[v.Name] = validatedValue
		r.Logger.Printf("sucessfully extracted and parsed parameter [parameter: %v]", v.Name)
	}

	if len(invalid) > 0 || len(missing) > 0 {
		r.Logger.Printf("this request has invalid or missing parameters [invalid: %v] [missing: %v]", len(invalid), len(missing))

		r.ResultCode = "G005"
		r.ResultData = struct {
			Missing *[]ResourceParameter `json:"missing"`
			Invalid *[]ResourceParameter `json:"invalid"`
		}{
			Missing: &missing,
			Invalid: &invalid,
		}
		return
	}

	r.Parameters = &parameters
	r.Logger.Printf("sucessfully parsed body payload [available: %v]", len(*r.Parameters))
}

// decodeBody parses the request body into a map[string]any according to the
// resource's InputFormat. Returns false (and sets the error code) if the body
// cannot be decoded.
//
// For input_format=form, values may be []any (multi-valued keys) or string.
// For input_format=json, values are whatever JSON yields (string, float64,
// bool, []any, map[string]any).
//
// If no parameter declares get_from=body, the body is not parsed at all and
// an empty map is returned.
func (r *Request) decodeBody() (map[string]any, bool) {
	needsBody := false
	for _, p := range r.Resource.Parameters {
		if p.GetFrom == "body" {
			needsBody = true
			break
		}
	}

	if !needsBody {
		return map[string]any{}, true
	}

	if len(r.Input) == 0 {
		return map[string]any{}, true
	}

	switch r.Resource.InputFormat {
	case "json":
		var parsed map[string]any
		if err := json.Unmarshal(r.Input, &parsed); err != nil {
			r.ResultCode = "G004"
			return nil, false
		}
		if !baseutils.IsMap(parsed) {
			r.ResultCode = "G004"
			return nil, false
		}
		return parsed, true

	case "form":
		values, err := url.ParseQuery(string(r.Input))
		if err != nil {
			r.ResultCode = "G008"
			return nil, false
		}

		out := make(map[string]any, len(values))
		for k, vs := range values {
			if len(vs) == 1 {
				out[k] = vs[0]
			} else {
				arr := make([]any, len(vs))
				for i, s := range vs {
					arr[i] = s
				}
				out[k] = arr
			}
		}
		return out, true
	}

	// unreachable: NewAPI rejects invalid input_format at boot
	r.ResultCode = "I003"
	return nil, false
}

// lookupParameter returns the raw value of a parameter from the appropriate
// source (body or query) and whether it was present.
//
// For query-sourced parameters, the value is always a string (since
// http query strings are always strings).
func (r *Request) lookupParameter(p ResourceParameter, body map[string]any) (any, bool) {
	switch p.GetFrom {
	case "query":
		v, ok := r.Query[p.Name]
		if !ok {
			return nil, false
		}
		return v, true
	case "body":
		v, ok := body[p.Name]
		if !ok {
			return nil, false
		}
		return v, true
	}
	return nil, false
}

// validateParameter checks the raw value against the declared kind and
// returns the value (possibly converted) plus whether it is valid.
//
// Conversions for string-typed inputs (form / query):
//   - kind=integer  : strconv.ParseInt
//   - kind=float    : strconv.ParseFloat
//   - kind=bool     : "true"/"false"/"1"/"0" (case-insensitive)
//   - kind=enum     : value must be in p.Options
//   - kind=array    : []any of strings (only valid coming from form)
//   - kind=map      : not allowed for string sources (boot rejects)
func (r *Request) validateParameter(p ResourceParameter, raw any) (any, bool) {
	isStringSource := p.GetFrom == "query" ||
		(p.GetFrom == "body" && r.Resource.InputFormat == "form")

	switch p.Kind {
	case "string":
		s, ok := raw.(string)
		if !ok {
			return nil, false
		}
		if s == "" && p.Required {
			return nil, false
		}
		if p.MaxLength > 0 && utf8.RuneCountInString(s) > p.MaxLength {
			r.Logger.Printf("parameter exceeded maximum length [param: %v] [maxLength: %v] [length: %v]", p.Name, p.MaxLength, utf8.RuneCountInString(s))
			return nil, false
		}
		return s, true

	case "enum":
		s, ok := raw.(string)
		if !ok {
			if p.Required {
				return nil, false
			}
			return nil, true
		}
		if !slices.Contains(p.Options, s) {
			r.Logger.Printf("parameter got an value that does not match the ENUM available ones [param: %v] [recieved: %v]", p.Name, s)
			return nil, false
		}
		return s, true

	case "integer":
		if isStringSource {
			s, ok := raw.(string)
			if !ok {
				return nil, false
			}
			n, err := strconv.ParseInt(s, 10, 64)
			if err != nil {
				return nil, false
			}
			return n, true
		}
		switch f := raw.(type) {
		case float64:
			if f != math.Trunc(f) {
				return nil, false
			}
			return int64(f), true
		case float32:
			if float64(f) != math.Trunc(float64(f)) {
				return nil, false
			}
			return int64(f), true
		case int, int8, int16, int32, int64:
			return f, true
		}
		return nil, false

	case "float":
		if isStringSource {
			s, ok := raw.(string)
			if !ok {
				return nil, false
			}
			f, err := strconv.ParseFloat(s, 64)
			if err != nil {
				return nil, false
			}
			return f, true
		}
		switch f := raw.(type) {
		case float64, float32, int, int8, int16, int32, int64:
			return f, true
		}
		return nil, false

	case "bool":
		if isStringSource {
			s, ok := raw.(string)
			if !ok {
				return nil, false
			}
			switch strings.ToLower(s) {
			case "true", "1":
				return true, true
			case "false", "0":
				return false, true
			}
			return nil, false
		}
		b, ok := raw.(bool)
		if !ok {
			return nil, false
		}
		return b, true

	case "array":
		switch v := raw.(type) {
		case []any:
			return v, true
		case []string:
			arr := make([]any, len(v))
			for i, s := range v {
				arr[i] = s
			}
			return arr, true
		case string:
			if isStringSource {
				return []any{v}, true
			}
		}
		return nil, false

	case "map":
		m, ok := raw.(map[string]any)
		if !ok {
			return nil, false
		}
		return m, true
	}

	return nil, false
}

// callMethod dispatches to the application function bound to
// r.Resource.ResourceMethod and stores the returned (data, code) pair on
// the request.
//
// Behavior notes:
//
//   - Skipped entirely if the result code is already non-OK (so a failed
//     parse / auth never reaches the application).
//   - "I003" is set when the resource declares a function name that the
//     application's method map does not contain — this is a configuration
//     bug, not a client error.
//   - Panics inside the application function are recovered and rewritten to
//     "I001" with the panic value attached as response data; the surrounding
//     RequestPostMethod still runs so transactions can be rolled back.
//   - An empty result code returned by the application is treated as "OK".
func (r *Request) callMethod() {
	if r.ResultCode != "OK" {
		return
	}

	// check if the resource method function exists
	if _, ok := r.api.methods[r.Resource.ResourceMethod]; !ok {
		r.Logger.Println("resource method function does not exists at the methods map")

		r.ResultCode = "I003"
		r.ResultData = r.Resource.ResourceMethod

		return
	}

	// handle panic at function call
	defer func() {
		if rcv := recover(); rcv != nil {
			r.Logger.Printf("resource method function got in panic [err: %v]", rcv)

			r.ResultCode = "I001"
			r.ResultData = rcv
		}
	}()

	// call the function
	ts := time.Now()

	r.Logger.Printf("======> %v <======", r.Path)

	r.ResultData, r.ResultCode = r.api.methods[r.Resource.ResourceMethod](r)

	r.Logger.Printf("======> %v <======", time.Since(ts))

	// fix the response code
	if r.ResultCode == "" {
		r.ResultCode = "OK"
	}

}
