# chiutil: an ObjectsFolder item becomes a folder (item scope)

Date: 2026-10-01. Status: implemented (uncommitted); live reference `make example-backoffice-scopes`. Work from this directory (`go-run`); the consumer
(safeapi gateway) follows in a separate change after a tag bump.

## Goal

Let a backoffice tree branch per item of a dynamic collection. Today `chiutil.ObjectsFolder` is a leaf
construct: an item is a bag of `ObjectRoute[T]` handlers, and nothing can be mounted under
`/<name>/{id}/`. The consumer needs this shape:

```text
/backoffice/accounts/                      ObjectsFolder over accounts
/backoffice/accounts/{accountId}/          an item, now a RouteFolder ("scope")
/backoffice/accounts/{accountId}/consumers/            nested ObjectsFolder, filtered by the account
/backoffice/accounts/{accountId}/consumers/{id}/details
/backoffice/accounts/{accountId}/transactions/          plain sub-folder, handlers read the account
/backoffice/accounts/{accountId}/contexts/              nested ObjectsFolder from another package
```

Each package that owns a store mounts a per-item view on the scope; the store itself stays flat. The item
is resolved once per request and read from the request context by whatever is mounted below.

## Where things are today

`pkg/chiutil/objects_folder.go` (421 lines)

- `ObjectsFolder[T]` (line 186): builds `listingFolder`, then `listingFolder.router.Route("/{id}", ...)`
  (line 223) with an anonymous subrouter: `/`, `/index.json`, and one handler per `mapper.Routes()` entry,
  each calling `mapper.GetItem` itself.
- `paramName` is hardcoded `"id"` (line 190, the comment says otherwise).
- The item index is hand-built: `instanceRoutes []*RouteEntry` + `itemIndex(paramValue)` (line 400),
  `serveItemHTML` / `serveItemJSON` / `serveItemIndex` / `addItemIndexRoute`.
- `ObjectMapper[T]` is the public interface: `ListItems()`, `GetItem(id)`, `Routes()`. 15 call sites across
  go-run tests and safeapi implement it. It must not change.

`pkg/chiutil/route_folder.go` (1108 lines)

- `RouteFolder{router, basePath, rootPath, serviceName, title, description, index, indexRoute, entries}`
  (line 71). `basePath` is a literal string.
- `relPath()` (line 86) → `relativeToRoot(basePath, rootPath)`; published as `FolderIndex.Path` by
  `indexData()` (line 995), served by `serveJSON` (line 989, discards `r`), `serveHTML` preview branch
  (line 951), `serveIndex` (line 183).
- `folder.html` `browserBasePath()` (line 671) subtracts `FolderIndex.Path` from `location.pathname` to
  find the mount base, so a literal `{accountId}` in `Path` breaks the breadcrumb and every relative link.
- `Folder()` (line 210) appends a `RouteEntry{IsFolder, subfolder}` to `entries`; `resolveEntries`
  (line 1082) renders them. This is the mechanism the scope reuses: anything registered on the scope
  folder lands in its `entries` and shows in the item index for free.
- `WildcardFolder` (line 260): the previous attempt at this. Stays as is, superseded for new work.

chi fact used below: `Context.URLParam` returns the *last* match for a key, and a mounted sub-mux's
middleware runs before that sub-mux has matched its own routes. So nested scopes with the same param
name work; distinct names are hygiene only.

## Changes

### A. `relPath` substitutes URL params at serve time (route_folder.go)

```go
// relPath returns this folder's path relative to the tree's mount root, with
// {param} segments replaced by the request's URL params.
func (this *RouteFolder) relPath(r *http.Request) string
```

- Walk `basePath` segments; a segment `{name}` becomes `chi.URLParam(r, name)`; leave it literal when
  the request has no such param (index built outside a request, tests).
- `indexData(r)`, `serveJSON`, `serveHTML` (preview branch), `serveIndex` pass `r` through. There are 14
  internal call sites of `relPath()` / `indexData()` / `listIndex()` / `itemIndex()`; all are inside
  handlers and have `r` at hand.
- No new fields. Children inherit substitution because their `basePath` is built from the parent's.

### B. `Scope()` and `ItemOf` (objects_folder.go)

Replace the anonymous `/{id}` subrouter with a real `RouteFolder`:

```go
itemFolder := &RouteFolder{
    router:      chi.NewRouter(),
    basePath:    listingFolder.basePath + "/{" + paramName + "}",
    rootPath:    parent.rootPath,
    serviceName: parent.serviceName,
    description: ...,   // the collection description, as today
}
itemFolder.router.Use(resolveItem(mapper, paramName))  // GetItem once; 404 on miss; item into ctx
itemFolder.router.Get("/", itemFolder.serveHTML)
itemFolder.router.Get("/index.json", itemFolder.serveJSON)
registerPageAssets(itemFolder.router)
listingFolder.router.Mount("/{"+paramName+"}", itemFolder.router)
```

- Item routes from `mapper.Routes()` register on `itemFolder` via the existing `RouteFolder` route
  methods (so they append to `itemFolder.entries` with Hidden/External/Action honoured) and read the item
  with `ItemOf[T](r)` instead of a second `GetItem`.
- Delete `instanceRoutes`, `itemIndex()`, `serveItemHTML`, `serveItemJSON`, `serveItemIndex`,
  `addItemIndexRoute`. `ItemIndexHandler(h)` becomes `itemFolder.IndexHandler(h)`; `ItemIndex` likewise.
- Item index title: today `capitalize(paramValue)`. Keep that: `indexData(r)` on a folder whose
  `basePath` ends in a `{param}` segment uses the substituted value as title when `title == ""`.
- New API:

```go
// Scope returns the folder served at /<name>/{param}/. Routes and folders
// registered on it are served per item, with the item in the request context.
func (this *objectsFolder[T]) Scope() *RouteFolder

// ItemOf returns the item an enclosing ObjectsFolder resolved for this request.
func ItemOf[T any](r *http.Request) (T, bool)
```

  Context key is `itemKey[T]{}`: one key per item type, so scopes of different types nest; two scopes of
  the same type shadow, which is acceptable and documented.

- `FlatJSON` (`/{id}.json`) stays on `listingFolder` as today, untouched.

### C. Param name option (objects_folder.go)

```go
type ObjectsOption func(*objectsConfig)
func WithParam(name string) ObjectsOption
func ObjectsFolder[T any](parent *RouteFolder, name string, mapper ObjectMapper[T], opts ...ObjectsOption) *objectsFolder[T]
```

Default stays `"id"`: safeapi reads `chi.URLParam(r, "id")` in two item-index handlers. Fix the stale
comment at line 190.

### D. Scoped mapper (objects_folder.go)

```go
// ScopedObjectMapper lists and resolves T within an enclosing scope item S.
type ScopedObjectMapper[S, T any] interface {
    ListItems(scope S) []ObjectEntry
    GetItem(scope S, id string) (T, bool)
    Routes() []ObjectRoute[T]
}

// ScopedObjectsFolder is ObjectsFolder mounted on a Scope(); S is read with ItemOf[S].
func ScopedObjectsFolder[S, T any](scope *RouteFolder, name string, mapper ScopedObjectMapper[S, T], opts ...ObjectsOption) *objectsFolder[T]
```

Implementation: `objectsFolder` switches internally to a private request-aware mapper

```go
type requestMapper[T any] interface {
    listItems(r *http.Request) []ObjectEntry
    getItem(r *http.Request, id string) (T, bool)
    routes() []ObjectRoute[T]
}
```

with two adapters: `ObjectMapper[T]` ignores `r`; `ScopedObjectMapper[S, T]` calls `ItemOf[S](r)` first
and returns nothing / false when the scope is absent. `listIndex()` gains `r`. The public `ObjectMapper`
is unchanged.

### E. Navigable 404 (route_folder.go, new `not_found.go`)

With B, an unknown id 404s at the item level, so the shell is never served and the user is left on a
bare `404 page not found` with no way up. Same for a mistyped route inside any folder. The tree gets a
404 page that links back to the nearest folder that does exist.

```go
// serveNotFound answers a miss below this folder. Browsers get an HTML page
// whose links climb back to this folder and its ancestors; everything else
// keeps the plain http.NotFound body.
func (this *RouteFolder) serveNotFound(w http.ResponseWriter, r *http.Request)

// notFoundItem is serveNotFound for an unknown item: the message names the
// collection and the id.
func (this *RouteFolder) notFoundItem(w http.ResponseWriter, r *http.Request, id string)
```

Where it is wired:

- Every folder router sets `router.NotFound(folder.serveNotFound)` when created (`NewRouteFolder`,
  `NewRouteFolderOn`, `Folder`, the ObjectsFolder listing and item folders, `WildcardFolder`,
  `StaticFSFolder`). Each folder sets its own so the "nearest existing folder" is the one that missed;
  chi only copies a parent's handler to sub-muxes that have none.
- `resolveItem` (B) calls `listingFolder.notFoundItem(w, r, id)` on a miss.
- `StaticFSFolder`'s `fs.Stat` miss calls `folder.serveNotFound` instead of `http.NotFound`.
- User handlers mounted with `Mount` / `MountDesc` are not touched.

Response:

- Status is always 404. HTML only when the request is a navigation (`Accept` contains `text/html`)
  and not a shell preview; otherwise `http.NotFound` as today, so `index.json` fetches, previews and API
  callers see no change. Previews are excluded because the viewer renders them in an iframe whose
  relative links resolve against the shell, not the missed path.
- Links are relative only, so the page works behind a path-stripping proxy (same rule as
  `redirectToTrailingSlash`). From the request path and the folder's expanded `basePath` (change A):
  the number of segments below the folder gives the `../` count to reach it, one less when the request
  has no trailing slash. `rootPath` gives the home link the same way.
- Body: a breadcrumb from home to the missing path. Ancestors up to the nearest existing folder are
  links; segments below it are plain text,
  since they would 404 too. A primary "Back to <folder title or name>" link, and a one-line message:
  `accounts has no item "nope"` for an item miss, `no route "x" in transactions` otherwise.
- Styling matches `writeDefaultIndexHTML` (same inline CSS, no shell, no JS).

## Tests (ginkgo, `pkg/chiutil/*_test.go`)

Existing fixtures: `TestAccount` + `TestAccountMapper` (sync.Map) in `objects_folder_test.go`, 15 specs
under `Describe("ObjectMapper")`. They are the regression net for B; all must pass unchanged.

Add a `TestConsumer` fixture and a `TestConsumerMapper` implementing `ScopedObjectMapper[*TestAccount,
*TestConsumer]` that lists the account's consumers. New specs:

1. `Scope()` serves `/accounts/{id}/` with the item's routes listed; unknown id is 404 for `/`,
   `/index.json`, each route, and every nested path.
2. A `Folder("transactions")` with a `GetDesc` and a `ScopedObjectsFolder("consumers")` registered on
   the scope appear in `/accounts/acc1/index.json` as folder entries.
3. `/accounts/acc1/consumers/index.json` lists only acc1's consumers; `/accounts/acc1/consumers/c1/details`
   resolves through the account; `/accounts/acc2/consumers/c1/details` is 404.
4. `ItemOf[*TestAccount]` and `ItemOf[*TestConsumer]` both resolve inside the nested handler.
5. `FolderIndex.Path` at `/accounts/acc1/`, `/accounts/acc1/consumers/`, `/accounts/acc1/consumers/c1/`
   carries the real ids, not `{id}`; extend the spec at `route_folder_test.go:185`.
6. `WithParam("accountId")` changes the chi param and `ItemIndex` handlers see it; default remains `"id"`.
7. Nested scopes with the default `"id"` on both levels still resolve the right item at each level.
8. `GET /backoffice/accounts/nope/details` with `Accept: text/html` is 404, HTML, and its "back" link
   is `../` (resolves to `/backoffice/accounts/`); the home link `../../` resolves to `/backoffice/`.
9. Same path without `Accept: text/html`, and `/backoffice/accounts/nope/index.json`, keep the plain
   404 body.
10. A mistyped route in a nested folder (`/backoffice/accounts/acc1/transactions/nope`) links back to
    `transactions/` with the real account id in the breadcrumb; a missing file in a `StaticFSFolder`
    links back to that folder.
11. The page is correct behind a stripping proxy: mount the tree under a prefix the request path does
    not carry and assert the links are still relative and resolve.

## Demo (`examples/backoffice-demo/main.go`)

The demo mounts a `backoffice.New()` folder with the log viewer and three JSON routes on a TCP port.
Add an in-memory `accounts` ObjectsFolder (two accounts, a few consumers each) with:

- `Scope().Folder("transactions")` serving a JSON list filtered by `ItemOf[*Account](r)`,
- `ScopedObjectsFolder(scope, "consumers", ...)` with a `/details` route,
- `WithParam("accountId")` on the outer folder.

Run: `make example-backoffice-demo DEMO_PORT=8081 BACKOFFICE_ADDR=:9090`, open `/backoffice/` and click
through; the breadcrumb and relative links are the visual check for change A. Then edit the URL to a
missing account and to a missing route under a consumer; each 404 page must lead back up (change E).

## Acceptance

- `make test` green; no change to any existing `ObjectMapper` implementation.
- Demo browsable at two nesting levels with correct breadcrumbs.
- Tag go-run; safeapi bumps `github.com/gur-shatz/go-run` and wires the gateway `accounts` scope (that
  design lives in safeapi `docs/private/261001_backoffice_per_account_scope.md`).

## Out of scope

`WildcardFolder` is left alone. No changes to `folder.html` / `bo.js` are expected; if the shell needs
one, that is a signal A is incomplete.

## Implementation notes (2026-10-01)

- Path expansion is positional per param name (`expandParams`, route_folder.go), not
  `chi.URLParam`, so `/accounts/{id}/consumers/{id}` publishes both real ids.
- Item routes register through a private `addObjectRoutes` that keeps today's index semantics (a
  non-GET route with an Action is listed under its real method). Item folders keep sorted entries and
  are titled by the capitalized id; `objectsFolder.Description` sets the item subtitle too.
- FlatJSON resolves through the request-aware mapper, so a scoped FlatJSON respects its scope.
- `Static()` builds `http.StripPrefix` from the literal `basePath` and is not supported under a scope;
  `StaticFSFolder` is.
- Behaviour change: an unknown id now 404s on `/`, `index.json`, page assets and Action forms (the
  item index and Action forms used to answer 200).
- Specs: `pkg/chiutil/item_scope_test.go` (12 specs). Live reference: `examples/backoffice-scopes`.
- Follow-up (same day): a `WildcardFolder` instance that is not in the listing now 404s (navigable
  page) on its index and every route below it; before, any name got a 200 index. Handlers signal a miss
  with `chiutil.NotFound(w, r)`, which finds the innermost folder through a request-context key that
  every folder router sets (`initRouter`, not_found.go).
