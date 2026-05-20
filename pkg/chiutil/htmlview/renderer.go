// Package htmlview renders Go values as simple HTML pages for chiutil routes.
package htmlview

import (
	"fmt"
	"html/template"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"time"
)

const defaultEmptyText = "No data"

// Handler renders either a fixed object or a value loaded from a request.
type Handler struct {
	title     string
	object    any
	loader    func(*http.Request) (any, error)
	columns   []string
	emptyText string
}

// Renderer returns an empty HTML renderer.
func Renderer() *Handler {
	return &Handler{emptyText: defaultEmptyText}
}

// Render returns a renderer configured with a fixed object or slice.
func Render(value any) *Handler {
	return Renderer().WithObject(value)
}

// Loader returns a renderer configured with a typed request-time loader.
func Loader[T any](loader func(*http.Request) (T, error)) *Handler {
	return Renderer().WithLoader(func(r *http.Request) (any, error) {
		return loader(r)
	})
}

// WithObject configures the renderer to render value.
func (h *Handler) WithObject(value any) *Handler {
	h.object = value
	h.loader = nil
	return h
}

// WithLoader configures the renderer to load the rendered value per request.
func (h *Handler) WithLoader(loader func(*http.Request) (any, error)) *Handler {
	h.loader = loader
	return h
}

// WithTitle sets the page title and heading.
func (h *Handler) WithTitle(title string) *Handler {
	h.title = title
	return h
}

// WithColumns selects and orders struct fields for object and slice rendering.
func (h *Handler) WithColumns(columns ...string) *Handler {
	h.columns = append(h.columns[:0], columns...)
	return h
}

// WithEmptyText sets the text used for nil and empty values.
func (h *Handler) WithEmptyText(text string) *Handler {
	h.emptyText = text
	return h
}

// ServeHTTP renders the configured value as HTML.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	value := h.object
	if h.loader != nil {
		loaded, err := h.loader(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		value = loaded
	}

	model := h.buildPage(value)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := pageTpl.Execute(w, model); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (h *Handler) buildPage(value any) pageData {
	title := h.title
	if title == "" {
		title = "View"
	}

	model := pageData{
		Title: title,
		Empty: h.emptyText,
	}
	if model.Empty == "" {
		model.Empty = defaultEmptyText
	}

	v := derefValue(reflect.ValueOf(value))
	if !v.IsValid() {
		model.Text = model.Empty
		return model
	}

	switch v.Kind() {
	case reflect.Struct:
		model.Kind = "object"
		model.Rows = h.objectRows(v)
	case reflect.Slice, reflect.Array:
		model.Kind = "table"
		model.Headers, model.TableRows = h.tableRows(v)
		if len(model.TableRows) == 0 {
			model.Text = model.Empty
		}
	case reflect.Map:
		model.Kind = "object"
		model.Rows = mapRows(v)
	default:
		model.Kind = "scalar"
		model.Text = formatValue(v)
	}

	return model
}

func (h *Handler) objectRows(v reflect.Value) []kvRow {
	fields := selectedFields(v.Type(), h.columns)
	rows := make([]kvRow, 0, len(fields))
	for _, f := range fields {
		rows = append(rows, kvRow{
			Key:   fieldLabel(f),
			Value: formatValue(fieldValue(v, f.Index)),
		})
	}
	return rows
}

func (h *Handler) tableRows(v reflect.Value) ([]string, [][]string) {
	if v.Len() == 0 {
		return nil, nil
	}

	first := firstValidElement(v)
	if !first.IsValid() {
		return []string{"Value"}, emptyScalarRows(v)
	}

	if first.Kind() != reflect.Struct {
		rows := make([][]string, 0, v.Len())
		for i := 0; i < v.Len(); i++ {
			rows = append(rows, []string{formatValue(derefValue(v.Index(i)))})
		}
		return []string{"Value"}, rows
	}

	fields := selectedFields(first.Type(), h.columns)
	headers := make([]string, 0, len(fields))
	for _, f := range fields {
		headers = append(headers, fieldLabel(f))
	}

	rows := make([][]string, 0, v.Len())
	for i := 0; i < v.Len(); i++ {
		item := derefValue(v.Index(i))
		row := make([]string, 0, len(fields))
		if item.IsValid() && item.Kind() == reflect.Struct {
			for _, f := range fields {
				row = append(row, formatValue(fieldValue(item, f.Index)))
			}
		} else {
			for range fields {
				row = append(row, "")
			}
		}
		rows = append(rows, row)
	}
	return headers, rows
}

func emptyScalarRows(v reflect.Value) [][]string {
	rows := make([][]string, 0, v.Len())
	for i := 0; i < v.Len(); i++ {
		rows = append(rows, []string{formatValue(derefValue(v.Index(i)))})
	}
	return rows
}

func firstValidElement(v reflect.Value) reflect.Value {
	for i := 0; i < v.Len(); i++ {
		item := derefValue(v.Index(i))
		if item.IsValid() {
			return item
		}
	}
	return reflect.Value{}
}

func selectedFields(t reflect.Type, columns []string) []reflect.StructField {
	if len(columns) == 0 {
		fields := make([]reflect.StructField, 0, t.NumField())
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if fieldVisible(f) {
				fields = append(fields, f)
			}
		}
		return fields
	}

	fields := make([]reflect.StructField, 0, len(columns))
	for _, name := range columns {
		if f, ok := t.FieldByName(name); ok && fieldVisible(f) {
			fields = append(fields, f)
		}
	}
	return fields
}

func fieldVisible(f reflect.StructField) bool {
	return f.PkgPath == "" && f.Tag.Get("htmlview") != "-"
}

func fieldValue(v reflect.Value, index []int) reflect.Value {
	for _, i := range index {
		v = derefValue(v)
		if !v.IsValid() || v.Kind() != reflect.Struct || i >= v.NumField() {
			return reflect.Value{}
		}
		v = v.Field(i)
	}
	return derefValue(v)
}

func fieldLabel(f reflect.StructField) string {
	if tag := f.Tag.Get("htmlview"); tag != "" {
		name := strings.Split(tag, ",")[0]
		if name != "" && name != "-" {
			return name
		}
	}
	return f.Name
}

func mapRows(v reflect.Value) []kvRow {
	keys := v.MapKeys()
	sort.Slice(keys, func(i, j int) bool {
		return formatValue(keys[i]) < formatValue(keys[j])
	})

	rows := make([]kvRow, 0, len(keys))
	for _, key := range keys {
		rows = append(rows, kvRow{
			Key:   formatValue(key),
			Value: formatValue(derefValue(v.MapIndex(key))),
		})
	}
	return rows
}

func derefValue(v reflect.Value) reflect.Value {
	for v.IsValid() && (v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface) {
		if v.IsNil() {
			return reflect.Value{}
		}
		v = v.Elem()
	}
	return v
}

func formatValue(v reflect.Value) string {
	v = derefValue(v)
	if !v.IsValid() {
		return ""
	}
	if v.CanInterface() {
		if t, ok := v.Interface().(time.Time); ok {
			if t.IsZero() {
				return ""
			}
			return t.Format(time.RFC3339)
		}
		if s, ok := v.Interface().(fmt.Stringer); ok {
			return s.String()
		}
	}
	if !v.CanInterface() {
		return ""
	}
	switch v.Kind() {
	case reflect.Struct, reflect.Map, reflect.Slice, reflect.Array:
		return fmt.Sprintf("%v", v.Interface())
	default:
		return fmt.Sprint(v.Interface())
	}
}

type pageData struct {
	Title     string
	Kind      string
	Rows      []kvRow
	Headers   []string
	TableRows [][]string
	Text      string
	Empty     string
}

type kvRow struct {
	Key   string
	Value string
}

var pageTpl = template.Must(template.New("htmlview").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}}</title>
<style>
  :root { color-scheme: light dark; --bg: #ffffff; --text: #17202a; --muted: #667085; --border: #d0d5dd; --head: #f5f7fa; --row: #fbfcfd; }
  @media (prefers-color-scheme: dark) {
    :root { --bg: #111827; --text: #e5e7eb; --muted: #9ca3af; --border: #374151; --head: #1f2937; --row: #172033; }
  }
  body { margin: 0; padding: 24px; background: var(--bg); color: var(--text); font: 14px/1.5 -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; }
  main { max-width: 1120px; margin: 0 auto; }
  h1 { margin: 0 0 18px; font-size: 20px; font-weight: 650; }
  table { width: 100%; border-collapse: collapse; border: 1px solid var(--border); border-radius: 6px; overflow: hidden; }
  th, td { padding: 8px 10px; border-bottom: 1px solid var(--border); text-align: left; vertical-align: top; }
  th { background: var(--head); font-size: 12px; font-weight: 650; color: var(--muted); }
  tr:nth-child(even) td { background: var(--row); }
  tr:last-child td { border-bottom: 0; }
  .kv th { width: 220px; }
  .empty, .scalar { white-space: pre-wrap; }
</style>
</head>
<body>
<main>
  <h1>{{.Title}}</h1>
  {{if eq .Kind "object"}}
    {{if .Rows}}
      <table class="kv"><tbody>{{range .Rows}}<tr><th>{{.Key}}</th><td>{{.Value}}</td></tr>{{end}}</tbody></table>
    {{else}}
      <div class="empty">{{.Empty}}</div>
    {{end}}
  {{else if eq .Kind "table"}}
    {{if .TableRows}}
      <table>
        <thead><tr>{{range .Headers}}<th>{{.}}</th>{{end}}</tr></thead>
        <tbody>{{range .TableRows}}<tr>{{range .}}<td>{{.}}</td>{{end}}</tr>{{end}}</tbody>
      </table>
    {{else}}
      <div class="empty">{{.Empty}}</div>
    {{end}}
  {{else if eq .Kind "scalar"}}
    <div class="scalar">{{.Text}}</div>
  {{else}}
    <div class="empty">{{.Text}}</div>
  {{end}}
</main>
</body>
</html>`))
