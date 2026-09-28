package baseapi

import (
	"encoding/xml"
	"errors"
	"io"
	"log"
	"net/http/httptest"
	"strings"
	"testing"
)

type testTwiML struct {
	XMLName xml.Name `xml:"Response"`
	Message string   `xml:"Message,omitempty"`
}

const testCodes = `{
	"OK":   {"status": 200, "message": {"en-us": "ok"}},
	"I001": {"status": 500, "message": {"en-us": "internal"}},
	"I002": {"status": 501, "message": {"en-us": "unknown code"}},
	"I003": {"status": 501, "message": {"en-us": "no function"}},
	"G004": {"status": 406, "message": {"en-us": "bad json"}},
	"G005": {"status": 406, "message": {"en-us": "bad params"}},
	"G008": {"status": 406, "message": {"en-us": "bad form"}},
	"G009": {"status": 504, "message": {"en-us": "timeout"}},
	"X001": {"status": 403, "message": {"en-us": "forbidden"}}
}`

func xmlRoute(fn string) string {
	return `{"POST": {"function": "` + fn + `", "input_format": "form", "output_format": "xml", "timeout": 0, "parameters": []}}`
}

// newOutputFormatAPI boots an API with the XML test routes, plus the extra
// route declarations given (each prefixed by a comma)
func newOutputFormatAPI(extraRoutes string) (API, error) {
	routes := `{
		"index": {"GET": {"function": "Index", "input_format": "json", "output_format": "json", "timeout": 0, "parameters": []}},
		"xml/ok": ` + xmlRoute("XMLOK") + `,
		"xml/empty": ` + xmlRoute("XMLEmpty") + `,
		"xml/nilptr": ` + xmlRoute("XMLNilPtr") + `,
		"xml/fail": ` + xmlRoute("XMLFail") + `,
		"xml/unmarshalable": ` + xmlRoute("XMLBad") + extraRoutes + `
	}`

	return NewAPIFromBytes([]byte(routes), []byte(testCodes), Methods{
		"Index":     func(r *Request) (any, string) { return nil, "OK" },
		"XMLOK":     func(r *Request) (any, string) { return testTwiML{Message: "hi"}, "OK" },
		"XMLEmpty":  func(r *Request) (any, string) { return nil, "OK" },
		"XMLNilPtr": func(r *Request) (any, string) { return (*testTwiML)(nil), "OK" },
		"XMLFail":   func(r *Request) (any, string) { return testTwiML{Message: "no"}, "X001" },
		"XMLBad":    func(r *Request) (any, string) { return map[string]string{"a": "b"}, "OK" },
	}, log.New(io.Discard, "", 0), true, []string{"test"})
}

func call(api *API, method, path string) (int, string, string) {
	w := httptest.NewRecorder()
	HandleHTTPServerRequests(w, httptest.NewRequest(method, path, strings.NewReader("")), api)
	return w.Code, w.Header().Get("Content-Type"), w.Body.String()
}

func TestOutputFormatXML(t *testing.T) {
	api, err := newOutputFormatAPI("")
	if err != nil {
		t.Fatal(err)
	}

	const xmlType, jsonType = "application/xml; charset=utf-8", "application/json; charset=utf-8"

	cases := []struct {
		path, wantType, wantBody string
		wantStatus               int
		contains                 bool
	}{
		{"/xml/ok", xmlType, xml.Header + `<Response><Message>hi</Message></Response>`, 200, false},
		{"/xml/empty", xmlType, "", 200, false},
		{"/xml/nilptr", xmlType, "", 200, false},
		{"/xml/fail", jsonType, `"code":"X001"`, 403, true},
		{"/xml/unmarshalable", jsonType, `"code":"I001"`, 500, true},
	}

	for _, c := range cases {
		status, ctype, body := call(&api, "POST", c.path)
		bodyOK := body == c.wantBody || (c.contains && strings.Contains(body, c.wantBody))
		if status != c.wantStatus || ctype != c.wantType || !bodyOK {
			t.Errorf("%s: got %d %q %q", c.path, status, ctype, body)
		}
	}

	// a json route keeps the envelope
	if status, ctype, body := call(&api, "GET", "/"); status != 200 || ctype != jsonType || !strings.Contains(body, `"code":"OK"`) {
		t.Errorf("index: got %d %q %q", status, ctype, body)
	}

	// failures before any resource is matched keep the envelope too
	if _, ctype, _ := call(&api, "GET", "/xml/ok"); ctype != jsonType {
		t.Errorf("xml/ok with GET: got content type %q", ctype)
	}
}

func TestOutputFormatValidation(t *testing.T) {
	cases := map[string]string{
		"missing": `"input_format": "json"`,
		"invalid": `"input_format": "json", "output_format": "yaml"`,
	}

	for name, formats := range cases {
		_, err := newOutputFormatAPI(`,
			"bad/route": {"POST": {"function": "Index", ` + formats + `, "timeout": 0, "parameters": []}}`)

		if !errors.Is(err, ErrInvalidRoute) {
			t.Errorf("%s: expected ErrInvalidRoute, got %v", name, err)
		}
	}
}
