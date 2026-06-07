// Package chiutil provides utilities for chi routers including
// self-documenting route folders with automatic navigation.
package chiutil

import (
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
}

// objectsFolder holds the state for an objects folder.
type objectsFolder[T any] struct {
	folder         *RouteFolder
	paramName      string
	mapper         ObjectMapper[T]
	instanceRoutes []*RouteEntry
	flatJSON       bool
	itemIndexFn    http.Handler
}

// Title sets the folder title displayed in the index.
func (this *objectsFolder[T]) Title(title string) *objectsFolder[T] {
	this.folder.title = title
	return this
}

// Description sets the folder description displayed in the index.
func (this *objectsFolder[T]) Description(desc string) *objectsFolder[T] {
	this.folder.description = desc
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
	this.folder.index = handler
	return this
}

// ItemIndex registers a per-object page that the HTML index viewer renders at
// /<name>/{id}/ in place of the default route listing. The handler reads the
// object id from chi.URLParam(r, "id") to render the selected object.
func (this *objectsFolder[T]) ItemIndex(handler http.HandlerFunc) *objectsFolder[T] {
	return this.ItemIndexHandler(handler)
}

// ItemIndexHandler registers a per-object http.Handler rendered by the HTML
// index viewer at /<name>/{id}/ in place of the default route listing.
func (this *objectsFolder[T]) ItemIndexHandler(handler http.Handler) *objectsFolder[T] {
	this.itemIndexFn = handler
	return this
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
//   - Looks up items via mapper.GetItem() for each route
//   - Returns 404 if item not found
//   - Dispatches to the appropriate handler
//
// URL structure created:
//
//	/<name>/                -> Lists all items (calls ListItems)
//	/<name>/{id}/           -> Lists routes for this item
//	/<name>/{id}/...        -> Dispatches to item's handler
//
// Example:
//
//	chiutil.ObjectsFolder(parent, "accounts", &AccountMapper{...})
func ObjectsFolder[T any](parent *RouteFolder, name string, mapper ObjectMapper[T]) *objectsFolder[T] {
	cleanName := strings.Trim(name, "/")

	// Derive paramName: "accounts" -> "id", or use singular + "Id" if name ends with 's'
	paramName := "id"

	// Create the listing folder
	listingFolder := &RouteFolder{
		router:      chi.NewRouter(),
		basePath:    parent.basePath + "/" + cleanName,
		rootPath:    parent.rootPath, // inherit the tree's home
		serviceName: parent.serviceName,
		entries:     []*RouteEntry{},
	}

	omf := &objectsFolder[T]{
		folder:    listingFolder,
		paramName: paramName,
		mapper:    mapper,
	}

	// Build instance routes from mapper.Routes()
	routes := mapper.Routes()
	omf.instanceRoutes = make([]*RouteEntry, 0, len(routes))
	for _, route := range routes {
		name := strings.TrimPrefix(route.Path, "/")
		omf.instanceRoutes = append(omf.instanceRoutes, &RouteEntry{
			Name:        name,
			Method:      route.Method,
			Path:        name,
			Description: route.Description,
		})
	}

	// Listing endpoints - delegate to mapper.ListItems()
	listingFolder.router.Get("/", omf.serveHTML)
	listingFolder.router.Get("/index.json", omf.serveListJSON)

	// Item routes
	listingFolder.router.Route("/{"+paramName+"}", func(r chi.Router) {
		r.Get("/", omf.serveItemHTML)
		r.Get("/index.json", omf.serveItemJSON)

		// Register each route with automatic item lookup
		for _, route := range routes {
			r.Method(route.Method, route.Path, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				id := chi.URLParam(req, paramName)
				item, found := mapper.GetItem(id)
				if !found {
					http.NotFound(w, req)
					return
				}
				route.Handler(item, w, req)
			}))
		}
	})

	// Mount on parent router
	parent.router.Mount("/"+cleanName, listingFolder.router)

	// Add folder entry to parent's index
	parent.entries = append(parent.entries, &RouteEntry{
		Name:     cleanName,
		Method:   "GET",
		Path:     cleanName + "/",
		IsFolder: true,
	})

	return omf
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
	writeDefaultIndexHTML(w, this.listIndex())
}

// serveListJSON serves the list of items from the mapper.
func (this *objectsFolder[T]) serveListJSON(w http.ResponseWriter, _ *http.Request) {
	index := this.listIndex()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(index)
}

func (this *objectsFolder[T]) listIndex() FolderIndex {
	items := this.mapper.ListItems()

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

	// Sort entries alphabetically
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name < entries[j].Name
	})

	return FolderIndex{
		ServiceName: this.folder.serviceName,
		Title:       this.folder.title,
		Description: this.folder.description,
		Path:        this.folder.relPath(),
		HasIndex:    true,
		Entries:     entries,
	}
}

// serveFlatItemJSON serves the item itself at /{id}.json.
func (this *objectsFolder[T]) serveFlatItemJSON(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, this.paramName)
	item, found := this.mapper.GetItem(id)
	if !found {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(item)
}

// serveItemJSON serves the routes available for a specific item.
func (this *objectsFolder[T]) serveItemJSON(w http.ResponseWriter, r *http.Request) {
	paramValue := chi.URLParam(r, this.paramName)
	index := this.itemIndex(paramValue)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(index)
}

func (this *objectsFolder[T]) serveItemHTML(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("preview") != "true" {
		this.folder.serveHTML(w, r)
		return
	}

	if this.itemIndexFn != nil {
		this.itemIndexFn.ServeHTTP(w, r)
		return
	}

	paramValue := chi.URLParam(r, this.paramName)
	writeDefaultIndexHTML(w, this.itemIndex(paramValue))
}

func (this *objectsFolder[T]) itemIndex(paramValue string) FolderIndex {
	entries := make([]*RouteEntry, len(this.instanceRoutes))
	copy(entries, this.instanceRoutes)

	// Sort entries alphabetically
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name < entries[j].Name
	})

	// Title is the item id, not the listing folder's title, so each item page
	// is labelled with the item rather than reading like the collection.
	return FolderIndex{
		ServiceName: this.folder.serviceName,
		Title:       capitalize(paramValue),
		Description: this.folder.description,
		Path:        relativeToRoot(this.folder.basePath+"/"+paramValue, this.folder.rootPath),
		HasIndex:    true,
		Entries:     entries,
	}
}
