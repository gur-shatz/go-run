package chiutil

import (
	_ "embed"
	"html/template"
	"net/http"
)

// Action renders the HTML confirmation page that the backoffice serves on
// GET for a route registered via PostFunc. The page's <form> POSTs back
// to the same URL, so the user's click-through becomes a deliberate
// submit — and CLI clients still POST directly without going through
// the form.
type Action interface {
	ServeHTML(w http.ResponseWriter, r *http.Request)
}

//go:embed action_form.html
var actionFormHTML string

//go:embed action_json_form.html
var actionJsonFormHTML string

var (
	actionFormTpl     = template.Must(template.New("form").Parse(actionFormHTML))
	actionJsonFormTpl = template.Must(template.New("jsonform").Parse(actionJsonFormHTML))
)

const (
	actionViewerHeader = "X-Chiutil-Viewer"
	actionViewerIframe = "iframe"
)

// formAction renders one labelled text input per field and submits as a
// standard application/x-www-form-urlencoded POST. The Go handler reads
// each value via r.FormValue(field).
type formAction struct {
	Text       string
	Fields     []string
	ViewerMode string
}

// Form returns an Action that renders a small HTML page with a heading
// (text) and one text input per name in fields. The form submits as a
// POST to the same URL using application/x-www-form-urlencoded; the
// receiving handler reads values via r.FormValue.
func Form(text string, fields []string) Action {
	cp := make([]string, len(fields))
	copy(cp, fields)
	return &formAction{Text: text, Fields: cp, ViewerMode: actionViewerIframe}
}

func (this *formAction) ServeHTML(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if this.ViewerMode != "" {
		w.Header().Set(actionViewerHeader, this.ViewerMode)
	}
	data := struct {
		Text    string
		Fields  []formField
		PostURL string
	}{
		Text:    this.Text,
		Fields:  buildFormFields(this.Fields, r),
		PostURL: r.URL.Path,
	}
	_ = actionFormTpl.Execute(w, data)
}

type formField struct {
	Name    string
	Default string
}

// buildFormFields prefills inputs from current query parameters when the
// caller arrived via a URL like `/path?id=acme`. This keeps the path-as-
// query convention working: a future "click-through" link can carry the
// value, and the user just confirms.
func buildFormFields(names []string, r *http.Request) []formField {
	out := make([]formField, len(names))
	q := r.URL.Query()
	for i, n := range names {
		out[i] = formField{Name: n, Default: q.Get(n)}
	}
	return out
}

// jsonFormAction renders a single textarea seeded with defaultBody and
// submits its contents as application/json on POST. Use this when the
// payload is large, structured, or doesn't naturally split into named
// fields.
type jsonFormAction struct {
	Text        string
	DefaultBody string
}

// JsonForm returns an Action that renders a confirmation page with a
// heading (text) and a single JSON textarea pre-filled with defaultBody.
// On submit the textarea contents are POSTed as application/json to the
// same URL.
func JsonForm(text, defaultBody string) Action {
	return &jsonFormAction{Text: text, DefaultBody: defaultBody}
}

func (this *jsonFormAction) ServeHTML(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set(actionViewerHeader, actionViewerIframe)
	data := struct {
		Text        string
		DefaultBody string
		PostURL     string
	}{
		Text:        this.Text,
		DefaultBody: this.DefaultBody,
		PostURL:     r.URL.Path,
	}
	_ = actionJsonFormTpl.Execute(w, data)
}
