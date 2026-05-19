# htmlview

`htmlview` is planned as a small helper package for rendering Go values as
passable HTML in `chiutil` backoffice pages.

The goal is to make object and slice endpoints nicer than raw JSON/YAML while
keeping handler code very small. `RouteFolder` stays responsible for routing,
navigation, and previews; `htmlview` is responsible only for turning values into
HTML.

## Intended API

The core type is a renderer that implements `http.Handler`:

```go
type Renderer struct {
	// internal render configuration
}

func Renderer() *Renderer
func Render(value any) *Renderer
func Loader[T any](loader func(*http.Request) (T, error)) *Renderer

func (r *Renderer) WithObject(value any) *Renderer
func (r *Renderer) WithLoader(loader func(*http.Request) (any, error)) *Renderer
func (r *Renderer) WithTitle(title string) *Renderer
func (r *Renderer) WithColumns(columns ...string) *Renderer
func (r *Renderer) WithEmptyText(text string) *Renderer
func (r *Renderer) ServeHTTP(w http.ResponseWriter, req *http.Request)
```

`Render` is for already available values:

```go
folder.GetHandler("/sessions", htmlview.Render(sessions).
	WithTitle("Sessions").
	WithColumns("ID", "Package", "StartedAt"))
```

`Loader` is for request-time values:

```go
folder.GetHandler("/sessions", htmlview.Loader(func(r *http.Request) ([]sessionView, error) {
	return store.ListSessions(r.Context())
}).
	WithTitle("Sessions").
	WithColumns("ID", "Package", "StartedAt"))
```

`WithLoader` remains available for builder-style configuration, but most typed
call sites should prefer `Loader` because it avoids forcing the loader function
to return `any`.

```go
folder.GetHandler("/sessions", htmlview.Renderer().
	WithTitle("Sessions").
	WithLoader(func(r *http.Request) (any, error) {
		return store.ListSessions(r.Context())
	}).
	WithColumns("ID", "Package", "StartedAt"))
```

## RouteFolder Integration

Because `Renderer` implements `http.Handler`, `RouteFolder` should grow handler
registration helpers alongside the existing `http.HandlerFunc` helpers:

```go
func (f *RouteFolder) GetHandler(path string, handler http.Handler)
func (f *RouteFolder) GetHandlerDesc(path, description string, handler http.Handler)
```

That keeps route registration direct:

```go
folder.GetHandler("/my", htmlview.Render(account).
	WithTitle("My Account").
	WithColumns("ID", "Email", "Plan"))
```

## ObjectsFolder Integration

`ObjectsFolder` routes should stay as they are. They are already descriptive:

```go
func (m *sessionMapper) Routes() []chiutil.ObjectRoute[*sessionView] {
	return []chiutil.ObjectRoute[*sessionView]{
		{
			Method:      "GET",
			Path:        "/details",
			Handler:     (*sessionView).serveDetails,
			Description: "Session identity, package, timing",
		},
		{
			Method:      "GET",
			Path:        "/subsets",
			Handler:     (*sessionView).serveSubsets,
			Description: "API subsets snapshot taken at initialize",
		},
	}
}
```

The renderer should make those handler bodies simple:

```go
func (s *sessionView) serveDetails(w http.ResponseWriter, r *http.Request) {
	htmlview.Render(s).
		WithTitle("Session details").
		WithColumns("ID", "Package", "StartedAt", "FinishedAt").
		ServeHTTP(w, r)
}

func (s *sessionView) serveSubsets(w http.ResponseWriter, r *http.Request) {
	htmlview.Render(s.Subsets).
		WithTitle("API subsets").
		WithColumns("Name", "Operations", "CapturedAt").
		ServeHTTP(w, r)
}
```

This keeps `ObjectsFolder` focused on lookup and route dispatch. `htmlview`
only handles presentation.

## Rendering Rules

Initial behavior should stay intentionally small:

- Structs render as a compact key/value table.
- Slices and arrays of structs render as a table.
- Maps render as a key/value table.
- Scalars render as escaped text.
- Pointers are dereferenced; nil pointers render as empty text.
- `time.Time` renders as a readable timestamp.
- `fmt.Stringer` values render through `String()`.
- Output is escaped by default.

`WithColumns` selects and orders fields for struct and slice rendering:

```go
htmlview.Render(accounts).
	WithTitle("Accounts").
	WithColumns("ID", "Name", "Status")
```

The result should be a regular HTML table with only those fields, in that order.

## Non-Goals

- Do not make a component framework.
- Do not replace `RouteFolder` or `ObjectsFolder`.
- Do not use JSON/YAML as an intermediate rendering format.
- Do not render raw HTML by default.
- Do not add broad styling/configuration APIs until real handlers need them.

