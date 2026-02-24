// Package chiutil provides utilities for chi routers including
// self-documenting route folders with automatic navigation.
package chiutil

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
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
}

// FolderIndex is the JSON structure served at index.json.
type FolderIndex struct {
	ServiceName string        `json:"serviceName,omitempty"`
	Title       string        `json:"title,omitempty"`
	Description string        `json:"description,omitempty"`
	Path        string        `json:"path"`
	Entries     []*RouteEntry `json:"entries"`
}

// RouteFolder wraps a chi.Router and provides automatic index generation.
// It tracks all registered routes and sub-folders, serving an HTML index
// at "/" and a JSON index at "/index.json".
type RouteFolder struct {
	router      chi.Router
	basePath    string
	serviceName string
	title       string
	description string
	entries     []*RouteEntry
}

// NewRouteFolder creates a new RouteFolder mounted at the given path.
// It automatically registers "/" and "/index.json" endpoints.
func NewRouteFolder(parent chi.Router, path string) *RouteFolder {
	folder := &RouteFolder{
		router:   chi.NewRouter(),
		basePath: normalizePath(path),
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

// Folder creates a nested RouteFolder and adds it to the index.
func (this *RouteFolder) Folder(path string) *RouteFolder {
	name := strings.Trim(path, "/")
	this.entries = append(this.entries, &RouteEntry{
		Name:     name,
		Method:   "GET",
		Path:     name + "/",
		IsFolder: true,
	})
	child := NewRouteFolderOn(this.router, "/"+name)
	child.serviceName = this.serviceName // Propagate service name to child
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
	listingFolder.router.Get("/", listingFolder.serveHTML)
	listingFolder.router.Get("/index.json", wildcard.serveJSON)

	// /<name>/{paramName}/... handles all parameterized routes
	listingFolder.router.Route("/{"+paramName+"}", func(r chi.Router) {
		// /<name>/{paramName}/ serves the route listing for this instance
		r.Get("/", listingFolder.serveHTML)
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
		Name:     cleanName,
		Method:   "GET",
		Path:     cleanName + "/",
		IsFolder: true,
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

// Description sets the folder description.
func (this *WildcardEntries) Description(desc string) *WildcardEntries {
	this.folder.description = desc
	return this
}

func (this *WildcardEntries) serveJSON(w http.ResponseWriter, _ *http.Request) {
	this.mu.RLock()
	entries := make([]*RouteEntry, len(this.entries))
	copy(entries, this.entries)
	this.mu.RUnlock()

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

// serveInstanceJSON serves the index for a specific instance (e.g., /accounts/acct-123/)
func (this *WildcardEntries) serveInstanceJSON(w http.ResponseWriter, r *http.Request) {
	paramValue := chi.URLParam(r, this.paramName)

	this.mu.RLock()
	entries := make([]*RouteEntry, len(this.instanceRoutes))
	copy(entries, this.instanceRoutes)
	this.mu.RUnlock()

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

// captureRoutes extracts registered routes from the chi router for the instance index
func (this *WildcardEntries) captureRoutes(r chi.Router) {
	this.mu.Lock()
	defer this.mu.Unlock()

	// Walk the routes to build the instance index
	walkFunc := func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		// Skip the index routes we added
		if route == "/" || route == "/index.json" {
			return nil
		}
		// Clean up the route path
		name := strings.TrimPrefix(route, "/")
		name = strings.TrimSuffix(name, "/")

		// Check if it's a folder (has wildcard or ends with /)
		isFolder := strings.Contains(name, "/") || strings.Contains(route, "*")

		this.instanceRoutes = append(this.instanceRoutes, &RouteEntry{
			Name:     name,
			Method:   method,
			Path:     name,
			IsFolder: isFolder,
		})
		return nil
	}

	chi.Walk(r, walkFunc)
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
				serveDirJSON(w, fsPath, folder.basePath+"/"+urlPath, folder.serviceName)
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
		Name:     cleanName,
		Method:   "GET",
		Path:     cleanName + "/",
		IsFolder: true,
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
func (this *RouteFolder) Get(path string, handler http.HandlerFunc) {
	this.addEntry("GET", path, "")
	this.router.Get(path, handler)
}

// GetDesc registers a GET route with a description.
func (this *RouteFolder) GetDesc(path, description string, handler http.HandlerFunc) {
	this.addEntry("GET", path, description)
	this.router.Get(path, handler)
}

// Post registers a POST route and adds it to the index.
func (this *RouteFolder) Post(path string, handler http.HandlerFunc) {
	this.addEntry("POST", path, "")
	this.router.Post(path, handler)
}

// PostDesc registers a POST route with a description.
func (this *RouteFolder) PostDesc(path, description string, handler http.HandlerFunc) {
	this.addEntry("POST", path, description)
	this.router.Post(path, handler)
}

// Put registers a PUT route and adds it to the index.
func (this *RouteFolder) Put(path string, handler http.HandlerFunc) {
	this.addEntry("PUT", path, "")
	this.router.Put(path, handler)
}

// PutDesc registers a PUT route with a description.
func (this *RouteFolder) PutDesc(path, description string, handler http.HandlerFunc) {
	this.addEntry("PUT", path, description)
	this.router.Put(path, handler)
}

// Patch registers a PATCH route and adds it to the index.
func (this *RouteFolder) Patch(path string, handler http.HandlerFunc) {
	this.addEntry("PATCH", path, "")
	this.router.Patch(path, handler)
}

// PatchDesc registers a PATCH route with a description.
func (this *RouteFolder) PatchDesc(path, description string, handler http.HandlerFunc) {
	this.addEntry("PATCH", path, description)
	this.router.Patch(path, handler)
}

// Delete registers a DELETE route and adds it to the index.
func (this *RouteFolder) Delete(path string, handler http.HandlerFunc) {
	this.addEntry("DELETE", path, "")
	this.router.Delete(path, handler)
}

// DeleteDesc registers a DELETE route with a description.
func (this *RouteFolder) DeleteDesc(path, description string, handler http.HandlerFunc) {
	this.addEntry("DELETE", path, description)
	this.router.Delete(path, handler)
}

// Handle registers a route with the specified method.
func (this *RouteFolder) Handle(method, path string, handler http.HandlerFunc) {
	this.addEntry(method, path, "")
	this.router.Method(method, path, handler)
}

// HandleDesc registers a route with a description.
func (this *RouteFolder) HandleDesc(method, path, description string, handler http.HandlerFunc) {
	this.addEntry(method, path, description)
	this.router.Method(method, path, handler)
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

// Mount mounts an http.Handler at the given path and adds it to the index as a folder.
func (this *RouteFolder) Mount(path string, handler http.Handler) {
	name := strings.Trim(path, "/")
	this.entries = append(this.entries, &RouteEntry{
		Name:     name,
		Method:   "GET",
		Path:     name + "/",
		IsFolder: true,
	})
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

func (this *RouteFolder) addEntry(method, path, description string) {
	name := strings.TrimPrefix(path, "/")
	this.entries = append(this.entries, &RouteEntry{
		Name:        name,
		Method:      method,
		Path:        name,
		Description: description,
	})
}

func (this *RouteFolder) serveHTML(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(folderHTML)
}

func (this *RouteFolder) serveJSON(w http.ResponseWriter, _ *http.Request) {
	index := FolderIndex{
		ServiceName: this.serviceName,
		Title:       this.title,
		Description: this.description,
		Path:        this.basePath,
		Entries:     this.entries,
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(index)
}

func normalizePath(path string) string {
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return strings.TrimSuffix(path, "/")
}

