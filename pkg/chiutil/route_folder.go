// Package chiutil provides utilities for chi routers including
// self-documenting route folders with automatic navigation.
package chiutil

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/go-chi/chi/v5"
)

//go:embed folder.html
var folderHTML []byte

// RouteEntry represents a single route or sub-folder in the index.
type RouteEntry struct {
	Name        string `json:"name"`
	Method      string `json:"method"`
	Path        string `json:"path"`
	Description string `json:"description,omitempty"`
	IsFolder    bool   `json:"isFolder,omitempty"`
	IsExternal  bool   `json:"isExternal,omitempty"`
	Hidden      bool   `json:"-"`

	// subfolder, when non-nil, lets serveJSON pull a live description from
	// the child folder if no explicit one was set on the entry. This handles
	// the common builder order: parent.Folder("x").Description("...").
	subfolder *RouteFolder
}

// RouteOption adjusts how a route is represented in the generated backoffice index.
type RouteOption func(*RouteEntry)

// Hidden keeps a route mounted and callable while omitting it from the
// generated backoffice index.
func Hidden() RouteOption {
	return func(entry *RouteEntry) {
		entry.Hidden = true
	}
}

// FolderIndex is the JSON structure served at index.json.
type FolderIndex struct {
	ServiceName string        `json:"serviceName,omitempty"`
	Title       string        `json:"title,omitempty"`
	Description string        `json:"description,omitempty"`
	Path        string        `json:"path"`
	HasIndex    bool          `json:"hasIndex,omitempty"`
	Entries     []*RouteEntry `json:"entries"`
}

// RouteFolder wraps a chi.Router and provides automatic index generation.
// It tracks all registered routes and sub-folders, serving an HTML index
// at "/" and a JSON index at "/index.json".
//
// basePath is the absolute server path of this folder (e.g.
// "/backoffice/components"). rootPath is the absolute server path of the
// mount root of the whole tree (e.g. "/backoffice") — the folder the index
// UI treats as "home". Sub-folders inherit rootPath from their parent, so the
// breadcrumb can render every level relative to home rather than to the
// server root.
type RouteFolder struct {
	router      chi.Router
	basePath    string
	rootPath    string
	serviceName string
	title       string
	description string
	index       http.Handler
	indexRoute  bool
	entries     []*RouteEntry
}

// relPath returns this folder's path relative to the tree's mount root, with a
// leading slash. The mount root itself is "/". This is what the index UI
// publishes as `path`, so the breadcrumb treats the mount root as home.
func (this *RouteFolder) relPath() string {
	return relativeToRoot(this.basePath, this.rootPath)
}

// relativeToRoot strips the mount-root prefix from an absolute folder path.
// relativeToRoot("/backoffice/logs", "/backoffice") == "/logs";
// relativeToRoot("/backoffice", "/backoffice") == "/".
func relativeToRoot(full, root string) string {
	rel := strings.TrimPrefix(full, root)
	if rel == "" {
		return "/"
	}
	if !strings.HasPrefix(rel, "/") {
		rel = "/" + rel
	}
	return rel
}

// NewRouteFolder creates a new RouteFolder mounted at the given path.
// It automatically registers "/" and "/index.json" endpoints.
func NewRouteFolder(parent chi.Router, path string) *RouteFolder {
	folder := &RouteFolder{
		router:   chi.NewRouter(),
		basePath: normalizePath(path),
		rootPath: normalizePath(path), // a freshly-mounted tree is its own home
		entries:  []*RouteEntry{},
	}

	// Register index endpoints on the folder's router
	folder.router.Get("/", folder.serveHTML)
	folder.router.Get("/index.json", folder.serveJSON)

	// Mount the folder's router on the parent
	parent.Mount(path, folder.router)

	return folder
}

// NewRouteFolderOn creates a RouteFolder on an existing router without mounting.
// Useful for creating sub-folders.
func NewRouteFolderOn(router chi.Router, path string) *RouteFolder {
	folder := &RouteFolder{
		router:   chi.NewRouter(),
		basePath: normalizePath(path),
		rootPath: normalizePath(path), // own home unless a parent overrides it
		entries:  []*RouteEntry{},
	}

	folder.router.Get("/", folder.serveHTML)
	folder.router.Get("/index.json", folder.serveJSON)

	router.Mount(path, folder.router)

	return folder
}

// Router returns the underlying chi.Router for advanced usage.
func (this *RouteFolder) Router() chi.Router {
	return this.router
}

// Title sets the folder title displayed in the index.
func (this *RouteFolder) Title(title string) *RouteFolder {
	this.title = title
	return this
}

// Description sets the folder description displayed in the index.
func (this *RouteFolder) Description(desc string) *RouteFolder {
	this.description = desc
	return this
}

// ServiceName sets the service name (e.g., "Backend", "Proxy") displayed in the index.
func (this *RouteFolder) ServiceName(name string) *RouteFolder {
	this.serviceName = name
	return this
}

// Index registers a folder-level page that the HTML index viewer renders when
// no entry is selected. The folder shell itself remains mounted at "/" and the
// JSON index remains at "/index.json"; the handler is served only for preview
// requests from the shell.
func (this *RouteFolder) Index(handler http.HandlerFunc) *RouteFolder {
	return this.IndexHandler(handler)
}

// IndexHandler registers a folder-level http.Handler rendered by the HTML
// index viewer when no entry is selected.
func (this *RouteFolder) IndexHandler(handler http.Handler) *RouteFolder {
	this.index = handler
	this.addIndexRoute()
	return this
}

func (this *RouteFolder) serveIndex(w http.ResponseWriter, r *http.Request) {
	if this.index == nil {
		http.NotFound(w, r)
		return
	}
	this.index.ServeHTTP(w, r)
}

func (this *RouteFolder) addIndexRoute() {
	if this.indexRoute {
		return
	}
	this.indexRoute = true
	this.entries = append(this.entries, this.indexRouteEntry())
	this.router.Get("/_index", this.serveIndex)
}

func (this *RouteFolder) indexRouteEntry() *RouteEntry {
	return &RouteEntry{
		Name:        "_index",
		Method:      http.MethodGet,
		Path:        "_index",
		Description: "Index page",
	}
}

// Folder creates a nested RouteFolder and adds it to the index.
func (this *RouteFolder) Folder(path string) *RouteFolder {
	name := strings.Trim(path, "/")
	child := NewRouteFolderOn(this.router, "/"+name)
	child.serviceName = this.serviceName        // Propagate service name to child
	child.basePath = this.basePath + "/" + name // absolute server path, not just "/name"
	child.rootPath = this.rootPath              // inherit the tree's home
	this.entries = append(this.entries, &RouteEntry{
		Name:      name,
		Method:    "GET",
		Path:      name + "/",
		IsFolder:  true,
		subfolder: child,
	})
	return child
}

// WildcardFolder creates a folder with dynamic entries and parameterized chi routes.
//
// This is useful for collections where:
//   - The list of items is dynamic (accounts, users, etc.)
//   - Each item has the same set of sub-routes
//   - You want browsable index pages at each level
//
// Structure created:
//
//	/<name>/                    -> Lists dynamic instances (managed via Add/Remove)
//	/<name>/{paramName}/        -> Lists available routes for an instance
//	/<name>/{paramName}/...     -> Your registered routes
//
// Example:
//
//	// Create wildcard folder with routes
//	accounts := parent.WildcardFolder("accounts", "accountId", func(r chi.Router) {
//	    r.Get("/details", func(w http.ResponseWriter, r *http.Request) {
//	        id := chi.URLParam(r, "accountId")
//	        // handle request for specific account
//	    })
//	    r.Get("/settings", settingsHandler)
//	}).Title("Accounts")
//
//	// Manage the listing dynamically
//	accounts.Add("acct-123", "Acme Corp")
//	accounts.Add("acct-456", "Globex Inc")
//	accounts.Remove("acct-123")
//
// This creates:
//
//	/accounts/              -> shows [acct-123/, acct-456/]
//	/accounts/acct-123/     -> shows [details, settings]
//	/accounts/acct-123/details -> executes your handler
func (this *RouteFolder) WildcardFolder(name, paramName string, routes func(chi.Router)) *WildcardEntries {
	cleanName := strings.Trim(name, "/")

	// Create the listing folder - this handles /<name>/ requests
	// and serves the list of dynamic instances
	listingFolder := &RouteFolder{
		router:      chi.NewRouter(),
		basePath:    this.basePath + "/" + cleanName,
		rootPath:    this.rootPath,    // inherit the tree's home
		serviceName: this.serviceName, // Propagate service name from parent
		entries:     []*RouteEntry{},
	}

	// WildcardEntries manages both:
	// - The dynamic instance list (shown at /<name>/)
	// - The route list (shown at /<name>/{param}/)
	wildcard := &WildcardEntries{
		folder:    listingFolder,
		paramName: paramName,
	}

	// /<name>/ serves the instance listing
	listingFolder.router.Get("/", wildcard.serveHTML)
	listingFolder.router.Get("/index.json", wildcard.serveJSON)

	// /<name>/{paramName}/... handles all parameterized routes
	listingFolder.router.Route("/{"+paramName+"}", func(r chi.Router) {
		// /<name>/{paramName}/ serves the route listing for this instance
		r.Get("/", wildcard.serveInstanceHTML)
		r.Get("/index.json", wildcard.serveInstanceJSON)

		// Register user's routes (e.g., /details, /settings)
		routes(r)

		// Walk the router to capture registered routes for the instance index
		wildcard.captureRoutes(r)
	})

	// Mount on parent router
	this.router.Mount("/"+cleanName, listingFolder.router)

	// Add folder entry to parent's index
	this.entries = append(this.entries, &RouteEntry{
		Name:      cleanName,
		Method:    "GET",
		Path:      cleanName + "/",
		IsFolder:  true,
		subfolder: listingFolder,
	})

	return wildcard
}

// WildcardEntries manages dynamic entries for a wildcard folder.
type WildcardEntries struct {
	mu             sync.RWMutex
	folder         *RouteFolder
	entries        []*RouteEntry // dynamic instances
	paramName      string
	instanceRoutes []*RouteEntry // routes available under each instance
}

// Add adds an instance to the wildcard folder's listing.
func (this *WildcardEntries) Add(id, description string) {
	this.mu.Lock()
	defer this.mu.Unlock()

	// Update if exists
	for _, e := range this.entries {
		if e.Name == id {
			e.Description = description
			return
		}
	}

	this.entries = append(this.entries, &RouteEntry{
		Name:        id,
		Method:      "GET",
		Path:        id + "/",
		Description: description,
		IsFolder:    true,
	})
}

// Remove removes an instance from the wildcard folder's listing.
func (this *WildcardEntries) Remove(id string) {
	this.mu.Lock()
	defer this.mu.Unlock()

	for i, e := range this.entries {
		if e.Name == id {
			this.entries = append(this.entries[:i], this.entries[i+1:]...)
			return
		}
	}
}

// Clear removes all instances from the wildcard folder's listing.
func (this *WildcardEntries) Clear() {
	this.mu.Lock()
	defer this.mu.Unlock()
	this.entries = []*RouteEntry{}
}

// Title sets the folder title.
func (this *WildcardEntries) Title(title string) *WildcardEntries {
	this.folder.title = title
	return this
}

// Index registers a folder-level page that the HTML index viewer renders when
// no entry is selected, mirroring RouteFolder.Index.
func (this *WildcardEntries) Index(handler http.HandlerFunc) *WildcardEntries {
	return this.IndexHandler(handler)
}

// IndexHandler registers a folder-level http.Handler rendered by the HTML
// index viewer when no entry is selected, mirroring RouteFolder.IndexHandler.
func (this *WildcardEntries) IndexHandler(handler http.Handler) *WildcardEntries {
	this.folder.IndexHandler(handler)
	return this
}

// MarkInstanceExternal flags the per-instance route entry at the given path as
// an external link so the index UI renders it as "open in new tab". The route
// itself must already be registered in the WildcardFolder callback (so it has
// an associated handler); this only changes how the entry is presented.
//
// No-op when no captured entry matches.
func (this *WildcardEntries) MarkInstanceExternal(path string) {
	this.mu.Lock()
	defer this.mu.Unlock()
	clean := strings.TrimPrefix(path, "/")
	for _, e := range this.instanceRoutes {
		if e.Name == clean {
			e.IsExternal = true
			return
		}
	}
}

// Description sets the folder description.
func (this *WildcardEntries) Description(desc string) *WildcardEntries {
	this.folder.description = desc
	return this
}

func (this *WildcardEntries) serveHTML(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("preview") != "true" {
		this.folder.serveHTML(w, r)
		return
	}
	if this.folder.index != nil {
		this.folder.index.ServeHTTP(w, r)
		return
	}

	this.mu.RLock()
	entries := make([]*RouteEntry, len(this.entries))
	copy(entries, this.entries)
	this.mu.RUnlock()
	if this.folder.indexRoute {
		entries = append(entries, this.folder.indexRouteEntry())
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name < entries[j].Name
	})

	writeDefaultIndexHTML(w, FolderIndex{
		ServiceName: this.folder.serviceName,
		Title:       this.folder.title,
		Description: this.folder.description,
		Path:        this.folder.relPath(),
		HasIndex:    true,
		Entries:     entries,
	})
}

func (this *WildcardEntries) serveJSON(w http.ResponseWriter, _ *http.Request) {
	this.mu.RLock()
	entries := make([]*RouteEntry, len(this.entries))
	copy(entries, this.entries)
	this.mu.RUnlock()
	if this.folder.indexRoute {
		entries = append(entries, this.folder.indexRouteEntry())
	}

	// Sort entries alphabetically
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name < entries[j].Name
	})

	index := FolderIndex{
		ServiceName: this.folder.serviceName,
		Title:       this.folder.title,
		Description: this.folder.description,
		Path:        this.folder.relPath(),
		HasIndex:    true,
		Entries:     entries,
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(index)
}

func (this *WildcardEntries) serveInstanceHTML(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("preview") != "true" {
		this.folder.serveHTML(w, r)
		return
	}

	paramValue := chi.URLParam(r, this.paramName)
	index := this.instanceIndex(paramValue)
	writeDefaultIndexHTML(w, index)
}

// serveInstanceJSON serves the index for a specific instance (e.g., /accounts/acct-123/)
func (this *WildcardEntries) serveInstanceJSON(w http.ResponseWriter, r *http.Request) {
	paramValue := chi.URLParam(r, this.paramName)
	index := this.instanceIndex(paramValue)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(index)
}

func (this *WildcardEntries) instanceIndex(paramValue string) FolderIndex {
	this.mu.RLock()
	entries := make([]*RouteEntry, len(this.instanceRoutes))
	copy(entries, this.instanceRoutes)
	// The instance's own listing carries its registered description (e.g. the
	// component description), used as the page subtitle.
	description := ""
	for _, e := range this.entries {
		if e.Name == paramValue {
			description = e.Description
			break
		}
	}
	this.mu.RUnlock()

	// Sort entries alphabetically
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name < entries[j].Name
	})

	// Title is the instance id (e.g. the component name), not the listing
	// folder's title — otherwise every instance page reads "Components".
	return FolderIndex{
		ServiceName: this.folder.serviceName,
		Title:       paramValue,
		Description: description,
		Path:        relativeToRoot(this.folder.basePath+"/"+paramValue, this.folder.rootPath),
		HasIndex:    true,
		Entries:     entries,
	}
}

// captureRoutes extracts registered routes from the chi router for the instance index
func (this *WildcardEntries) captureRoutes(r chi.Router) {
	this.mu.Lock()
	defer this.mu.Unlock()

	// chi.Walk visits one (method, route) pair per registered method — a
	// Handle-mounted route appears once per HTTP verb — and a wildcard
	// mount ("console/*") is the same navigation target as its bare-prefix
	// redirect ("console"). Collapse both into one entry per target, with
	// wildcard mounts rendered as navigable folders ("console/") rather
	// than a literal-star link.
	byName := make(map[string]*RouteEntry)
	var order []string
	walkFunc := func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		// Skip the index routes we added
		if route == "/" || route == "/index.json" {
			return nil
		}
		wildcard := strings.Contains(route, "*")
		name := strings.TrimPrefix(route, "/")
		name = strings.TrimSuffix(name, "/")
		name = strings.TrimSuffix(name, "*")
		name = strings.TrimSuffix(name, "/")
		isFolder := wildcard || strings.Contains(name, "/")
		path := name
		if wildcard {
			path = name + "/"
		}

		if e, ok := byName[name]; ok {
			if wildcard {
				e.IsFolder = true
				e.Path = path
			}
			if method == http.MethodGet && !e.IsFolder {
				e.Method = method
			}
			return nil
		}
		e := &RouteEntry{Name: name, Method: method, Path: path, IsFolder: isFolder}
		byName[name] = e
		order = append(order, name)
		return nil
	}

	chi.Walk(r, walkFunc)

	this.instanceRoutes = this.instanceRoutes[:0]
	for _, name := range order {
		this.instanceRoutes = append(this.instanceRoutes, byName[name])
	}
}

// MaxStaticFileSize is the maximum file size that StaticFilesFolder will serve inline.
// Files larger than this will return an error message instead.
const MaxStaticFileSize = 1 << 20 // 1 MB

// StaticFilesFolder creates a browsable file system folder using the standard folder UI.
// It registers a wildcard route that serves directories as navigable indexes
// and files with their content. Files larger than MaxStaticFileSize return an error.
func (this *RouteFolder) StaticFilesFolder(name, fsRoot string) *RouteFolder {
	cleanName := strings.Trim(name, "/")

	folder := &RouteFolder{
		router:      chi.NewRouter(),
		basePath:    this.basePath + "/" + cleanName,
		rootPath:    this.rootPath, // inherit the tree's home
		serviceName: this.serviceName,
		entries:     []*RouteEntry{},
	}

	// Handler for serving file system paths
	serveFS := func(w http.ResponseWriter, r *http.Request, urlPath string) {
		// Check if requesting index.json for a directory
		isIndexJSON := strings.HasSuffix(urlPath, "/index.json") || urlPath == "index.json"
		if isIndexJSON {
			urlPath = strings.TrimSuffix(urlPath, "/index.json")
			urlPath = strings.TrimSuffix(urlPath, "index.json")
		}

		fsPath := filepath.Join(fsRoot, urlPath)

		info, err := os.Stat(fsPath)
		if err != nil {
			http.NotFound(w, r)
			return
		}

		if info.IsDir() {
			if isIndexJSON {
				serveDirJSON(w, fsPath, relativeToRoot(folder.basePath+"/"+urlPath, folder.rootPath), folder.serviceName)
			} else {
				folder.serveHTML(w, r)
			}
		} else {
			// Check file size limit only for preview requests (from UI fetch)
			// Direct downloads (Open/Download links) have no limit
			if r.URL.Query().Get("preview") == "true" && info.Size() > MaxStaticFileSize {
				w.Header().Set("Content-Type", "text/plain; charset=utf-8")
				w.WriteHeader(http.StatusRequestEntityTooLarge)
				fmt.Fprintf(w, "File too large (%d bytes, max %d bytes). Use Download or Open to get the file.",
					info.Size(), MaxStaticFileSize)
				return
			}
			// Force download with Content-Disposition if requested
			if r.URL.Query().Get("download") == "true" {
				w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filepath.Base(fsPath)))
			}
			http.ServeFile(w, r, fsPath)
		}
	}

	// Handle root path
	folder.router.Get("/", func(w http.ResponseWriter, r *http.Request) {
		serveFS(w, r, "")
	})
	folder.router.Get("/index.json", func(w http.ResponseWriter, r *http.Request) {
		serveFS(w, r, "index.json")
	})

	// Handle all sub-paths
	folder.router.Get("/*", func(w http.ResponseWriter, r *http.Request) {
		serveFS(w, r, chi.URLParam(r, "*"))
	})

	// Mount and add to parent index
	this.router.Mount("/"+cleanName, folder.router)
	this.entries = append(this.entries, &RouteEntry{
		Name:      cleanName,
		Method:    "GET",
		Path:      cleanName + "/",
		IsFolder:  true,
		subfolder: folder,
	})

	return folder
}

func serveDirJSON(w http.ResponseWriter, fsPath, urlPath, serviceName string) {
	files, _ := os.ReadDir(fsPath)
	entries := make([]*RouteEntry, 0, len(files))

	for _, f := range files {
		path := f.Name()
		if f.IsDir() {
			path += "/"
		}
		entries = append(entries, &RouteEntry{
			Name:     f.Name(),
			Path:     path,
			IsFolder: f.IsDir(),
			Method:   "GET",
		})
	}

	index := FolderIndex{
		ServiceName: serviceName,
		Title:       filepath.Base(fsPath),
		Path:        urlPath,
		Entries:     entries,
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(index)
}

// Get registers a GET route and adds it to the index.
func (this *RouteFolder) Get(path string, handler http.HandlerFunc, opts ...RouteOption) {
	this.addEntry("GET", path, "", opts...)
	this.router.Get(path, markdownHeaderFunc(path, handler))
}

// GetDesc registers a GET route with a description.
func (this *RouteFolder) GetDesc(path, description string, handler http.HandlerFunc, opts ...RouteOption) {
	this.addEntry("GET", path, description, opts...)
	this.router.Get(path, markdownHeaderFunc(path, handler))
}

// GetHandler registers a GET route backed by an http.Handler and adds it to the index.
func (this *RouteFolder) GetHandler(path string, handler http.Handler, opts ...RouteOption) {
	this.addEntry("GET", path, "", opts...)
	this.router.Get(path, markdownHeader(handler, path).ServeHTTP)
}

// GetHandlerDesc registers a GET route backed by an http.Handler with a description.
func (this *RouteFolder) GetHandlerDesc(path, description string, handler http.Handler, opts ...RouteOption) {
	this.addEntry("GET", path, description, opts...)
	this.router.Get(path, markdownHeader(handler, path).ServeHTTP)
}

// PostArgs configures a POST route registered via PostFunc. Handler is the
// real POST endpoint; Action (optional) is rendered on GET so a browser
// click on the entry — which is always GET — lands on a confirm-and-submit
// page that POSTs back to the same path.
type PostArgs struct {
	Path        string
	Description string
	Handler     http.HandlerFunc

	// Action, when non-nil, is registered as a GET on Path. Use Form or
	// JsonForm to construct one. Leave nil for routes that are intended
	// to be called only from CLI / programmatic clients.
	Action Action

	// Hidden keeps both the POST route and optional GET action form callable
	// while omitting the PostFunc entry from the generated backoffice index.
	Hidden bool
}

// PostFunc registers a POST route. If args.Action is non-nil, it is also
// registered as GET on the same path and that GET form is the visible
// backoffice entry; the submitting POST stays callable but is not listed as a
// separate menu item. Without Action, the POST route itself is listed.
func (this *RouteFolder) PostFunc(args PostArgs) {
	if !args.Hidden {
		method := "POST"
		if args.Action != nil {
			method = "GET"
		}
		this.addEntry(method, args.Path, args.Description)
	}
	this.router.Post(args.Path, markdownHeaderFunc(args.Path, args.Handler))
	if args.Action != nil {
		this.router.Get(args.Path, markdownHeaderFunc(args.Path, args.Action.ServeHTML))
	}
}

// Endpoint registers a semantic operation endpoint backed by a single
// http.Handler value and adds that operation to the folder index.
//
// The method argument is the operation's real method and the method shown in
// the backoffice index. For example, a protobuf RPC operation should pass
// "POST" because POST is the method used by programmatic clients.
//
// In addition to registering that real method, Endpoint also registers GET on
// the same path and routes it to the same handler. This is deliberate: folder
// index links are navigated with GET by browsers, while richer endpoint
// handlers often know how to render their own human-readable documentation or
// confirmation page for GET. The handler therefore owns both surfaces:
//
//   - METHOD path: the actual operation used by clients.
//   - GET path: the browser/backoffice representation of that operation.
//
// Use Endpoint when one handler object understands the operation method and its
// GET representation. If GET should be rendered by a separate Action, keep using
// PostFunc so the split remains explicit at the call site.
func (this *RouteFolder) Endpoint(method, path, description string, handler http.Handler, opts ...RouteOption) {
	this.addEntry(method, path, description, opts...)
	wrapped := markdownHeader(handler, path)
	this.router.Method(method, path, wrapped)
	if method != http.MethodGet {
		this.router.Get(path, wrapped.ServeHTTP)
	}
}

// Put registers a PUT route and adds it to the index.
func (this *RouteFolder) Put(path string, handler http.HandlerFunc, opts ...RouteOption) {
	this.addEntry("PUT", path, "", opts...)
	this.router.Put(path, markdownHeaderFunc(path, handler))
}

// PutDesc registers a PUT route with a description.
func (this *RouteFolder) PutDesc(path, description string, handler http.HandlerFunc, opts ...RouteOption) {
	this.addEntry("PUT", path, description, opts...)
	this.router.Put(path, markdownHeaderFunc(path, handler))
}

// Patch registers a PATCH route and adds it to the index.
func (this *RouteFolder) Patch(path string, handler http.HandlerFunc, opts ...RouteOption) {
	this.addEntry("PATCH", path, "", opts...)
	this.router.Patch(path, markdownHeaderFunc(path, handler))
}

// PatchDesc registers a PATCH route with a description.
func (this *RouteFolder) PatchDesc(path, description string, handler http.HandlerFunc, opts ...RouteOption) {
	this.addEntry("PATCH", path, description, opts...)
	this.router.Patch(path, markdownHeaderFunc(path, handler))
}

// Delete registers a DELETE route and adds it to the index.
func (this *RouteFolder) Delete(path string, handler http.HandlerFunc, opts ...RouteOption) {
	this.addEntry("DELETE", path, "", opts...)
	this.router.Delete(path, markdownHeaderFunc(path, handler))
}

// DeleteDesc registers a DELETE route with a description.
func (this *RouteFolder) DeleteDesc(path, description string, handler http.HandlerFunc, opts ...RouteOption) {
	this.addEntry("DELETE", path, description, opts...)
	this.router.Delete(path, markdownHeaderFunc(path, handler))
}

// Handle registers a route with the specified method.
func (this *RouteFolder) Handle(method, path string, handler http.HandlerFunc, opts ...RouteOption) {
	this.addEntry(method, path, "", opts...)
	this.router.Method(method, path, markdownHeaderFunc(path, handler))
}

// HandleDesc registers a route with a description.
func (this *RouteFolder) HandleDesc(method, path, description string, handler http.HandlerFunc, opts ...RouteOption) {
	this.addEntry(method, path, description, opts...)
	this.router.Method(method, path, markdownHeaderFunc(path, handler))
}

// Link adds a folder entry to the index without mounting a handler.
// Use this when the target is registered on a parent router outside the folder.
func (this *RouteFolder) Link(path, description string) {
	name := strings.Trim(path, "/")
	this.entries = append(this.entries, &RouteEntry{
		Name:        name,
		Method:      "GET",
		Path:        name + "/",
		Description: description,
		IsFolder:    true,
	})
}

// ExternalLink adds a link entry that opens in a new browser tab.
// The path is relative to the current folder (just like Link), so it works
// correctly behind reverse proxies with a base path.
// For full external URLs (e.g. "https://grafana.example.com"), pass the
// complete URL as path.
func (this *RouteFolder) ExternalLink(path, description string) {
	name := strings.Trim(path, "/")
	this.entries = append(this.entries, &RouteEntry{
		Name:        name,
		Method:      "GET",
		Path:        name + "/",
		Description: description,
		IsExternal:  true,
	})
}

// Mount mounts an http.Handler at the given path and adds it to the index as a folder.
func (this *RouteFolder) Mount(path string, handler http.Handler, opts ...RouteOption) {
	this.MountDesc(path, "", handler, opts...)
}

// MountDesc mounts an http.Handler at the given path with a description.
// Hidden() keeps the mount routable while omitting it from the index (e.g.
// a proxied tree reached through an external link rather than navigation).
func (this *RouteFolder) MountDesc(path, description string, handler http.Handler, opts ...RouteOption) {
	name := strings.Trim(path, "/")
	entry := &RouteEntry{
		Name:        name,
		Method:      "GET",
		Path:        name + "/",
		Description: description,
		IsFolder:    true,
	}
	for _, opt := range opts {
		opt(entry)
	}
	this.entries = append(this.entries, entry)
	this.router.Mount(path, handler)
}

// Static serves static files from the given directory.
func (this *RouteFolder) Static(path, dir string) {
	name := strings.Trim(path, "/")
	this.entries = append(this.entries, &RouteEntry{
		Name:        name,
		Method:      "GET",
		Path:        name + "/",
		Description: "Static files",
		IsFolder:    true,
	})
	this.router.Handle(path+"/*", http.StripPrefix(this.basePath+path, http.FileServer(http.Dir(dir))))
}

func (this *RouteFolder) addEntry(method, path, description string, opts ...RouteOption) {
	name := strings.TrimPrefix(path, "/")
	entry := &RouteEntry{
		Name:        name,
		Method:      method,
		Path:        name,
		Description: description,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(entry)
		}
	}
	this.entries = append(this.entries, entry)
}

func markdownHeaderFunc(path string, handler http.HandlerFunc) http.HandlerFunc {
	return markdownHeader(http.HandlerFunc(handler), path).ServeHTTP
}

func markdownHeader(handler http.Handler, routePath string) http.Handler {
	if !strings.HasSuffix(strings.ToLower(routePath), ".md") {
		return handler
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		w.Header().Set(actionViewerHeader, "markdown")
		handler.ServeHTTP(w, r)
	})
}

func (this *RouteFolder) serveHTML(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("preview") == "true" {
		if this.index != nil {
			this.index.ServeHTTP(w, r)
			return
		}
		writeDefaultIndexHTML(w, this.indexData())
		return
	}

	// The index addresses its children (the index.json fetch, entry links,
	// breadcrumb) with relative URLs, so it must be served from a
	// trailing-slash path. A mounted folder answers both /folder and /folder/
	// with 200, so redirect the bare form here rather than rendering a page
	// whose every relative link resolves against the parent.
	if !strings.HasSuffix(r.URL.Path, "/") {
		redirectToTrailingSlash(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(folderHTML)
}

// redirectToTrailingSlash 301s a bare folder URL to its trailing-slash form.
// The target is the final path segment (relative), not the absolute path, so
// it stays correct behind a path-stripping reverse proxy where r.URL.Path is
// not the full path the browser sees.
func redirectToTrailingSlash(w http.ResponseWriter, r *http.Request) {
	target := path.Base(r.URL.Path) + "/"
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	// Set Location directly rather than via http.Redirect, which would resolve
	// the relative target back to an absolute path and undo the proxy safety.
	w.Header().Set("Location", target)
	w.WriteHeader(http.StatusMovedPermanently)
}

func (this *RouteFolder) serveJSON(w http.ResponseWriter, _ *http.Request) {
	index := this.indexData()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(index)
}

func (this *RouteFolder) indexData() FolderIndex {
	return FolderIndex{
		ServiceName: this.serviceName,
		Title:       this.title,
		Description: this.description,
		Path:        this.relPath(),
		HasIndex:    true,
		Entries:     resolveEntries(this.entries),
	}
}

func writeDefaultIndexHTML(w http.ResponseWriter, index FolderIndex) {
	title := index.Title
	if title == "" {
		title = "Routes"
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, `<!doctype html><html><head><meta charset="utf-8"><style>
body{font:14px -apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;color:#e6edf3;margin:0;padding:16px;background:#161b22}
h1{font-size:18px;margin:0 0 6px;color:#e6edf3}
p{color:#8b949e;margin:0 0 16px}
table{width:100%;border-collapse:collapse;background:#0d1117;border:1px solid #30363d}
th,td{text-align:left;padding:8px 10px;border-bottom:1px solid #30363d;vertical-align:top}
th{font-size:12px;color:#8b949e;font-weight:600;background:#161b22}
a{color:#58a6ff;text-decoration:none}
a:hover{text-decoration:underline}
.method{font:12px ui-monospace,SFMono-Regular,Menlo,monospace;color:#8b949e;white-space:nowrap}
.desc{color:#8b949e}
.empty{color:#8b949e;padding:12px 0}
</style></head><body>`)
	fmt.Fprintf(w, "<h1>%s</h1>", html.EscapeString(title))
	if index.Description != "" {
		fmt.Fprintf(w, "<p>%s</p>", html.EscapeString(index.Description))
	}
	if len(index.Entries) == 0 {
		fmt.Fprint(w, `<div class="empty">No routes registered</div>`)
		fmt.Fprint(w, `</body></html>`)
		return
	}
	fmt.Fprint(w, `<table><thead><tr><th>Name</th><th>Method</th><th>Description</th></tr></thead><tbody>`)
	for _, entry := range index.Entries {
		if !entry.IsFolder && !entry.IsExternal && entry.Method != http.MethodGet {
			continue
		}

		name := entry.Name
		if name == "" {
			name = entry.Path
		}
		method := entry.Method
		if entry.IsFolder {
			method = "DIR"
		} else if entry.IsExternal {
			method = "LINK"
		}
		target := entry.Path
		if target == "" {
			target = name
		}

		var link string
		switch {
		case entry.IsFolder:
			link = fmt.Sprintf(`<a href="%s" target="_parent">%s</a>`, html.EscapeString(target), html.EscapeString(name))
		case entry.IsExternal:
			link = fmt.Sprintf(`<a href="%s" target="_blank" rel="noopener noreferrer">%s</a>`, html.EscapeString(target), html.EscapeString(name))
		default:
			action := fmt.Sprintf("parent.postMessage({type:'chiutil:select',path:%s,method:%s}, '*'); return false;", jsonString(target), jsonString(entry.Method))
			link = fmt.Sprintf(`<a href="#" onclick="%s">%s</a>`, html.EscapeString(action), html.EscapeString(name))
		}

		fmt.Fprintf(w, `<tr><td>%s</td><td class="method">%s</td><td class="desc">%s</td></tr>`,
			link, html.EscapeString(method), html.EscapeString(entry.Description))
	}
	fmt.Fprint(w, `</tbody></table></body></html>`)
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// resolveEntries returns a copy of entries with each sub-folder entry's
// description filled in from the child folder if the entry itself has none.
// This lets descriptions set after .Folder() (the typical builder order)
// surface in the parent's index.
func resolveEntries(entries []*RouteEntry) []*RouteEntry {
	out := make([]*RouteEntry, 0, len(entries))
	for _, e := range entries {
		if e.Hidden {
			continue
		}
		if e.subfolder != nil && e.Description == "" && e.subfolder.description != "" {
			copy := *e
			copy.Description = e.subfolder.description
			out = append(out, &copy)
		} else {
			out = append(out, e)
		}
	}
	return out
}

func normalizePath(path string) string {
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	path = strings.TrimSuffix(path, "/")
	if path == "" {
		return "/"
	}
	return path
}
