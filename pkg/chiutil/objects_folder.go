// Package chiutil provides utilities for chi routers including
// self-documenting route folders with automatic navigation.
package chiutil

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"
)

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
	listingFolder.router.Get("/", listingFolder.serveHTML)
	listingFolder.router.Get("/index.json", omf.serveListJSON)

	// Item routes
	listingFolder.router.Route("/{"+paramName+"}", func(r chi.Router) {
		r.Get("/", listingFolder.serveHTML)
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

// serveListJSON serves the list of items from the mapper.
func (this *objectsFolder[T]) serveListJSON(w http.ResponseWriter, _ *http.Request) {
	items := this.mapper.ListItems()

	entries := make([]*RouteEntry, 0, len(items))
	for _, item := range items {
		name := item.Name
		if name == "" {
			name = item.ID
		}
		entries = append(entries, &RouteEntry{
			Name:        name,
			Method:      "GET",
			Path:        item.ID + "/",
			Description: item.Description,
			IsFolder:    true,
		})
	}

	// Sort entries alphabetically
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name < entries[j].Name
	})

	index := FolderIndex{
		ServiceName: this.folder.serviceName,
		Title:       this.folder.title,
		Description: this.folder.description,
		Path:        this.folder.basePath,
		Entries:     entries,
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(index)
}

// serveItemJSON serves the routes available for a specific item.
func (this *objectsFolder[T]) serveItemJSON(w http.ResponseWriter, r *http.Request) {
	paramValue := chi.URLParam(r, this.paramName)

	entries := make([]*RouteEntry, len(this.instanceRoutes))
	copy(entries, this.instanceRoutes)

	// Sort entries alphabetically
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name < entries[j].Name
	})

	index := FolderIndex{
		ServiceName: this.folder.serviceName,
		Title:       this.folder.title,
		Description: this.folder.description,
		Path:        this.folder.basePath + "/" + paramValue,
		Entries:     entries,
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(index)
}
