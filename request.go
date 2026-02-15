package baseapi

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/ncastellani/baseutils"
	"gopkg.in/guregu/null.v4"
)

// call all the request functions
func (r *Request) HandleRequest(api *API) (int, []byte, map[string]string) {
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
	r.api.RequestPreMethod(r)

	// call the API method
	r.callMethod()

	// call the post method middleware
	r.api.RequestPostMethod(r)

	// assemble the response
	return r.makeResponse()
}

// return an HTTP response for the current request result
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

// determine the requested route and resource
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

// parse the Authorization bearer token from the headers if required
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

// extract and parse parameters from URL query and body payload
func (r *Request) parsePayload() {
	if r.ResultCode != "OK" || len(r.Resource.Parameters) == 0 {
		return
	}

	r.Logger.Println("starting the parse of the request payload...")

	var err error

	// parse the body parameters
	var bodyKeys []string
	bodyParameters := make(map[string]any)

	if len(r.Input) > 0 {
		r.Logger.Println("this request got an body input")

		// parse the input data into an interface
		err = json.Unmarshal(r.Input, &bodyParameters)
		if err != nil {
			r.ResultCode = "G004"
			return
		}

		// check if the inputted body is an associative map
		if !baseutils.IsMap(bodyParameters) {
			r.ResultCode = "G004"
			return
		}

		// determine the keys passed on the body
		for _, v := range reflect.ValueOf(bodyParameters).MapKeys() {
			bodyKeys = append(bodyKeys, v.String())
		}

	}

	// validate the type of each resource parameter
	parameters := make(map[string]any)

	var missing []ResourceParameter
	var invalid []ResourceParameter

	for _, v := range r.Resource.Parameters {

		// check if the param is on the recieved keys
		var methodParams *map[string]any

		if !slices.Contains(bodyKeys, v.Name) {
			r.Logger.Printf("parameter missing at the body payload [param: %v]", v.Name)

			missing = append(missing, v)
			continue
		}

		methodParams = &bodyParameters

		// check if the informed value is of required type
		switch (*methodParams)[v.Name].(type) {
		case string:
			if v.Kind != "string" && v.Kind != "enum" {
				invalid = append(invalid, v)
				continue
			}
		case bool:
			if v.Kind != "bool" {
				invalid = append(invalid, v)
				continue
			}
		case int, int8, int16, int32, int64, float32, float64:
			if v.Kind != "number" {
				invalid = append(invalid, v)
				continue
			}
		case []string, []any:
			if v.Kind != "array" {
				invalid = append(invalid, v)
				continue
			}
		case map[string]any:
			if v.Kind != "map" {
				invalid = append(invalid, v)
				continue
			}
		default:
			if (*methodParams)[v.Name] != nil && v.Required {
				invalid = append(invalid, v)
				continue
			}
		}

		// perform param data check for the "enum" type
		if v.Kind == "enum" {
			if !slices.Contains(v.Options, (*methodParams)[v.Name].(string)) {
				r.Logger.Printf("parameter got an value that does not match the ENUM available ones [param: %v] [recieved: %v]", v.Name, (*methodParams)[v.Name].(string))

				invalid = append(invalid, v)
				continue
			}
		}

		// append this value into the parameters section
		parameters[v.Name] = (*methodParams)[v.Name]

		r.Logger.Printf("sucessfully extracted and parsed parameter [parameter: %v]", v.Name)
	}

	// return the parameters that failed the verification
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

	// assign the parsed body on the request
	r.Parameters = &parameters

	r.Logger.Printf("sucessfully parsed body payload [available: %v]", len(*r.Parameters))

}

// call the resource method function
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
