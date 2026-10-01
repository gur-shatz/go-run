// Package chiutil provides utilities for chi routers including
// self-documenting route folders with automatic navigation.
package chiutil

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
)

// capitalize upper-cases the first rune of s, leaving the rest unchanged.
func capitalize(s string) string {
	if s == "" {
		return s
	}
	r, size := utf8.DecodeRuneInString(s)
	return string(unicode.ToUpper(r)) + s[size:]
}

// ObjectMapper represents a collection of objects that can be exposed as a chi folder.
// It handles listing, lookup, and route registration in one interface.
//
// Example implementation:
//
//	type AccountMapper struct {
//	    accounts *sync.Map
//	}
//
//	func (m *AccountMapper) ListItems() []ObjectEntry {
//	    var entries []ObjectEntry
//	    m.accounts.Range(func(key, value any) bool {
//	        acc := value.(*Account)
//	        entries = append(entries, ObjectEntry{ID: acc.ID, Description: acc.Name})
//	        return true
//	    })
//	    return entries
//	}
//
//	func (m *AccountMapper) GetItem(id string) (*Account, bool) {
//	    if val, ok := m.accounts.Load(id); ok {
//	        return val.(*Account), true
//	    }
//	    return nil, false
//	}
//
//	func (m *AccountMapper) Routes() []ObjectRoute[*Account] {
//	    return []ObjectRoute[*Account]{
//	        {"GET", "/details", (*Account).Details, "Account details"},
//	        {"GET", "/settings", (*Account).Settings, "Account settings"},
//	    }
//	}
type ObjectMapper[T any] interface {
	// ListItems returns all items for the directory listing.
	ListItems() []ObjectEntry

	// GetItem retrieves an item by ID. Returns the item and true if found,
	// or the zero value and false if not found.
	GetItem(id string) (T, bool)

	// Routes returns the route definitions for items.
	// Each route maps a method/path to a handler extractor function.
	Routes() []ObjectRoute[T]
}

// ObjectEntry represents an item in the directory listing.
type ObjectEntry struct {
	ID          string
	Name        string // Optional display name (defaults to ID if empty)
	Description string
}

// ObjectRoute binds an HTTP method and path to a handler function.
//
// Action, when non-nil on a non-GET route, is also registered as GET on the
// same path so browser/backoffice navigation can render a confirmation form
// while programmatic callers keep using the route's real method.
// The Handler is a method expression that takes the item as receiver.
//
// Example using method expressions:
//
//	ObjectRoute[*Account]{"GET", "/details", (*Account).Details, "Account details"}
//
// Where Account.Details is defined as:
//
//	func (a *Account) Details(w http.ResponseWriter, r *http.Request) { ... }
//
// The method expression (*Account).Details produces:
//
//	func(*Account, http.ResponseWriter, *http.Request)
type ObjectRoute[T any] struct {
	Method      string
	Path        string
	Handler     func(T, http.ResponseWriter, *http.Request)
	Description string
	Action      Action
	// Hidden keeps the route callable while omitting it from the backoffice index.
	Hidden bool
	// External marks the index entry "open in new tab" (typically a route
	// that redirects out of this folder's tree, e.g. to a proxied console).
	External bool
}

// ScopedObjectMapper lists and resolves T within an enclosing scope item S,
// the item an outer ObjectsFolder resolved for the request (see ItemOf).
type ScopedObjectMapper[S, T any] interface {
	// ListItems returns the items of scope for the directory listing.
	ListItems(scope S) []ObjectEntry

	// GetItem retrieves an item of scope by ID.
	GetItem(scope S, id string) (T, bool)

	// Routes returns the route definitions for items.
	Routes() []ObjectRoute[T]
}

// ObjectsOption configures an ObjectsFolder.
type ObjectsOption func(*objectsConfig)

type objectsConfig struct {
	paramName string
}

// WithParam names the chi URL param the item id is matched as (default
// "id"). Distinct names across nested scopes are hygiene only: each scope
// resolves its own level either way.
func WithParam(name string) ObjectsOption {
	return func(config *objectsConfig) {
		config.paramName = name
	}
}

// itemKey is the request-context key an ObjectsFolder stores its resolved
// item under: one key per item type, so scopes of different types nest and
// two scopes of the same type shadow (the inner one wins).
type itemKey[T any] struct{}

// ItemOf returns the item an enclosing ObjectsFolder resolved for this
// request. Handlers registered on a Scope(), and everything mounted below
// it, read the item with it instead of looking the id up again.
func ItemOf[T any](r *http.Request) (T, bool) {
	item, ok := r.Context().Value(itemKey[T]{}).(T)
	return item, ok
}

// requestMapper is the request-aware form both public mappers adapt to, so
// a scoped mapper can read its enclosing item from the request.
type requestMapper[T any] interface {
	listItems(r *http.Request) []ObjectEntry
	getItem(r *http.Request, id string) (T, bool)
	routes() []ObjectRoute[T]
}

type plainMapper[T any] struct {
	mapper ObjectMapper[T]
}

func (this plainMapper[T]) listItems(*http.Request) []ObjectEntry { return this.mapper.ListItems() }

func (this plainMapper[T]) getItem(_ *http.Request, id string) (T, bool) {
	return this.mapper.GetItem(id)
}

func (this plainMapper[T]) routes() []ObjectRoute[T] { return this.mapper.Routes() }

type scopedMapper[S, T any] struct {
	mapper ScopedObjectMapper[S, T]
}

func (this scopedMapper[S, T]) listItems(r *http.Request) []ObjectEntry {
	scope, ok := ItemOf[S](r)
	if !ok {
		return nil
	}
	return this.mapper.ListItems(scope)
}

func (this scopedMapper[S, T]) getItem(r *http.Request, id string) (T, bool) {
	scope, ok := ItemOf[S](r)
	if !ok {
		var zero T
		return zero, false
	}
	return this.mapper.GetItem(scope, id)
}

func (this scopedMapper[S, T]) routes() []ObjectRoute[T] { return this.mapper.Routes() }

// objectsFolder holds the state for an objects folder.
type objectsFolder[T any] struct {
	folder    *RouteFolder // the listing, /<name>/
	item      *RouteFolder // the item scope, /<name>/{param}/
	paramName string
	mapper    requestMapper[T]
	flatJSON  bool
}

// Title sets the folder title displayed in the index.
func (this *objectsFolder[T]) Title(title string) *objectsFolder[T] {
	this.folder.title = title
	return this
}

// Description sets the folder description displayed in the index. Item
// pages carry it as their subtitle.
func (this *objectsFolder[T]) Description(desc string) *objectsFolder[T] {
	this.folder.description = desc
	this.item.description = desc
	return this
}

// Index registers a collection-level page that the HTML index viewer renders
// when no object is selected.
func (this *objectsFolder[T]) Index(handler http.HandlerFunc) *objectsFolder[T] {
	return this.IndexHandler(handler)
}

// IndexHandler registers a collection-level http.Handler rendered by the HTML
// index viewer when no object is selected.
func (this *objectsFolder[T]) IndexHandler(handler http.Handler) *objectsFolder[T] {
	this.folder.IndexHandler(handler)
	return this
}

// ItemIndex registers a per-object page that the HTML index viewer renders at
// /<name>/{id}/ in place of the default route listing. The handler reads the
// object with ItemOf, or its id from chi.URLParam under the folder's param
// name ("id" unless WithParam).
func (this *objectsFolder[T]) ItemIndex(handler http.HandlerFunc) *objectsFolder[T] {
	return this.ItemIndexHandler(handler)
}

// ItemIndexHandler registers a per-object http.Handler rendered by the HTML
// index viewer at /<name>/{id}/ in place of the default route listing.
func (this *objectsFolder[T]) ItemIndexHandler(handler http.Handler) *objectsFolder[T] {
	this.item.IndexHandler(handler)
	return this
}

// Scope returns the folder served at /<name>/{param}/. Routes and folders
// registered on it are served per item and listed on the item's index; the
// item is resolved once per request (404 when unknown) and read with ItemOf.
func (this *objectsFolder[T]) Scope() *RouteFolder {
	return this.item
}

// FlatJSON adds /{id}.json endpoints that encode items directly and makes
// list JSON entries point at those flat item documents.
func (this *objectsFolder[T]) FlatJSON() *objectsFolder[T] {
	if this.flatJSON {
		return this
	}
	this.flatJSON = true

	this.folder.router.Get("/{"+this.paramName+"}.json", this.serveFlatItemJSON)
	return this
}

// ObjectsFolder creates a folder backed by an ObjectMapper.
// This is a standalone function due to Go's limitation on generic methods.
//
// The folder automatically:
//   - Lists items via mapper.ListItems() at /<name>/
//   - Looks up the item via mapper.GetItem() once per request below /<name>/{id}/
//   - Returns 404 if item not found
//   - Dispatches to the appropriate handler
//
// URL structure created:
//
//	/<name>/                -> Lists all items (calls ListItems)
//	/<name>/{id}/           -> Lists routes for this item (the Scope folder)
//	/<name>/{id}/...        -> Dispatches to item's handler
//
// Example:
//
//	chiutil.ObjectsFolder(parent, "accounts", &AccountMapper{...})
func ObjectsFolder[T any](parent *RouteFolder, name string, mapper ObjectMapper[T], opts ...ObjectsOption) *objectsFolder[T] {
	return newObjectsFolder[T](parent, name, plainMapper[T]{mapper}, opts)
}

// ScopedObjectsFolder is ObjectsFolder mounted on an enclosing Scope(): it
// lists and resolves T within the scope item S, read with ItemOf[S].
//
//	accounts := chiutil.ObjectsFolder(bo, "accounts", accountMapper)
//	chiutil.ScopedObjectsFolder(accounts.Scope(), "consumers", consumerMapper)
//	// /accounts/{id}/consumers/{id}/...
func ScopedObjectsFolder[S, T any](scope *RouteFolder, name string, mapper ScopedObjectMapper[S, T], opts ...ObjectsOption) *objectsFolder[T] {
	return newObjectsFolder[T](scope, name, scopedMapper[S, T]{mapper}, opts)
}

func newObjectsFolder[T any](parent *RouteFolder, name string, mapper requestMapper[T], opts []ObjectsOption) *objectsFolder[T] {
	cleanName := strings.Trim(name, "/")

	config := objectsConfig{paramName: "id"}
	for _, opt := range opts {
		if opt != nil {
			opt(&config)
		}
	}
	paramName := config.paramName

	// Create the listing folder
	listingFolder := &RouteFolder{
		router:      chi.NewRouter(),
		basePath:    parent.basePath + "/" + cleanName,
		rootPath:    parent.rootPath, // inherit the tree's home
		serviceName: parent.serviceName,
		entries:     []*RouteEntry{},
	}
	listingFolder.initRouter()

	// The item scope: a folder per item, title and path from the item id.
	itemFolder := &RouteFolder{
		router:      chi.NewRouter(),
		basePath:    listingFolder.basePath + "/{" + paramName + "}",
		rootPath:    parent.rootPath,
		serviceName: parent.serviceName,
		entries:     []*RouteEntry{},
		sorted:      true,
		itemTitle:   true,
	}

	omf := &objectsFolder[T]{
		folder:    listingFolder,
		item:      itemFolder,
		paramName: paramName,
		mapper:    mapper,
	}

	// Listing endpoints - delegate to mapper.ListItems()
	listingFolder.router.Get("/", omf.serveHTML)
	listingFolder.router.Get("/index.json", omf.serveListJSON)
	registerPageAssets(listingFolder.router)

	// Item endpoints. The middleware must precede every route on the item
	// router, including what callers later register on Scope().
	itemFolder.router.Use(omf.resolveItem)
	itemFolder.initRouter()
	itemFolder.router.Get("/", itemFolder.serveHTML)
	itemFolder.router.Get("/index.json", itemFolder.serveJSON)
	registerPageAssets(itemFolder.router)
	addObjectRoutes(itemFolder, mapper.routes())
	listingFolder.router.Mount("/{"+paramName+"}", itemFolder.router)

	// Mount on parent router
	parent.router.Mount("/"+cleanName, listingFolder.router)

	// Add folder entry to parent's index
	parent.entries = append(parent.entries, &RouteEntry{
		Name:      cleanName,
		Method:    "GET",
		Path:      cleanName + "/",
		IsFolder:  true,
		subfolder: listingFolder,
	})

	return omf
}

// addObjectRoutes registers the mapper's item routes on the item folder.
// Each handler receives the item resolveItem put in the request context.
//
// A non-GET route with an Action is listed under its real method and also
// answers GET with the Action form, unless the mapper declares its own GET
// on that path.
func addObjectRoutes[T any](folder *RouteFolder, routes []ObjectRoute[T]) {
	explicitGETRoutes := map[string]bool{}
	for _, route := range routes {
		if strings.EqualFold(route.Method, http.MethodGet) {
			explicitGETRoutes[route.Path] = true
		}
	}
	for _, route := range routes {
		if !route.Hidden {
			name := strings.TrimPrefix(route.Path, "/")
			folder.entries = append(folder.entries, &RouteEntry{
				Name:        name,
				Method:      route.Method,
				Path:        name,
				Description: route.Description,
				IsExternal:  route.External,
			})
		}
		handler := markdownHeaderFunc(route.Path, func(w http.ResponseWriter, req *http.Request) {
			item, _ := ItemOf[T](req)
			route.Handler(item, w, req)
		})
		folder.router.Method(route.Method, route.Path, handler)
		if route.Action != nil && !strings.EqualFold(route.Method, http.MethodGet) && !explicitGETRoutes[route.Path] {
			folder.router.Get(route.Path, markdownHeaderFunc(route.Path, route.Action.ServeHTML))
		}
	}
}

// resolveItem looks the item up once per request below /<name>/{param}/ and
// puts it in the request context; an unknown id is a 404 for every path.
func (this *objectsFolder[T]) resolveItem(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, this.paramName)
		item, found := this.mapper.getItem(r, id)
		if !found {
			this.folder.notFoundItem(w, r, id)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), itemKey[T]{}, item)))
	})
}

func (this *objectsFolder[T]) serveHTML(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("preview") != "true" {
		this.folder.serveHTML(w, r)
		return
	}
	if this.folder.index != nil {
		this.folder.index.ServeHTTP(w, r)
		return
	}
	writeDefaultIndexHTML(w, this.listIndex(r))
}

// serveListJSON serves the list of items from the mapper.
func (this *objectsFolder[T]) serveListJSON(w http.ResponseWriter, r *http.Request) {
	index := this.listIndex(r)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(index)
}

func (this *objectsFolder[T]) listIndex(r *http.Request) FolderIndex {
	items := this.mapper.listItems(r)

	entries := make([]*RouteEntry, 0, len(items))
	for _, item := range items {
		name := item.Name
		if name == "" {
			name = item.ID
		}
		path := item.ID + "/"
		isFolder := true
		if this.flatJSON {
			path = url.PathEscape(item.ID) + ".json"
			isFolder = false
		}

		entries = append(entries, &RouteEntry{
			Name:        name,
			Method:      "GET",
			Path:        path,
			Description: item.Description,
			IsFolder:    isFolder,
		})
	}
	if this.folder.indexRoute {
		entries = append(entries, this.folder.indexRouteEntry())
	}

	// Sort entries alphabetically
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name < entries[j].Name
	})

	return FolderIndex{
		ServiceName: this.folder.serviceName,
		Title:       this.folder.title,
		Description: this.folder.description,
		Path:        this.folder.relPath(r),
		HasIndex:    true,
		Entries:     entries,
	}
}

// serveFlatItemJSON serves the item itself at /{id}.json.
func (this *objectsFolder[T]) serveFlatItemJSON(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, this.paramName)
	item, found := this.mapper.getItem(r, id)
	if !found {
		this.folder.notFoundItem(w, r, id)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(item)
}
