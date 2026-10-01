package chiutil_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/gur-shatz/go-run/pkg/chiutil"

	"github.com/go-chi/chi/v5"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type TestConsumer struct {
	ID        string
	AccountID string
}

func (c *TestConsumer) Details(w http.ResponseWriter, r *http.Request) {
	account, _ := chiutil.ItemOf[*TestAccount](r)
	consumer, _ := chiutil.ItemOf[*TestConsumer](r)
	json.NewEncoder(w).Encode(map[string]string{
		"id":       c.ID,
		"account":  account.ID,
		"consumer": consumer.ID,
	})
}

// TestConsumerMapper lists the consumers of the enclosing account.
type TestConsumerMapper struct {
	byAccount map[string][]*TestConsumer
}

func (m *TestConsumerMapper) ListItems(account *TestAccount) []chiutil.ObjectEntry {
	var entries []chiutil.ObjectEntry
	for _, c := range m.byAccount[account.ID] {
		entries = append(entries, chiutil.ObjectEntry{ID: c.ID})
	}
	return entries
}

func (m *TestConsumerMapper) GetItem(account *TestAccount, id string) (*TestConsumer, bool) {
	for _, c := range m.byAccount[account.ID] {
		if c.ID == id {
			return c, true
		}
	}
	return nil, false
}

func (m *TestConsumerMapper) Routes() []chiutil.ObjectRoute[*TestConsumer] {
	return []chiutil.ObjectRoute[*TestConsumer]{
		{Method: "GET", Path: "/details", Handler: (*TestConsumer).Details, Description: "Consumer details"},
	}
}

var _ = Describe("Item scope", func() {
	var (
		router   *chi.Mux
		accounts *TestAccountMapper
	)

	get := func(target string, header ...string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		for i := 0; i+1 < len(header); i += 2 {
			req.Header.Set(header[i], header[i+1])
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}
	indexOf := func(target string) chiutil.FolderIndex {
		w := get(target)
		ExpectWithOffset(1, w.Code).To(Equal(http.StatusOK), "for %s", target)
		var index chiutil.FolderIndex
		ExpectWithOffset(1, json.Unmarshal(w.Body.Bytes(), &index)).To(Succeed())
		return index
	}
	names := func(index chiutil.FolderIndex) []string {
		var out []string
		for _, e := range index.Entries {
			out = append(out, e.Name)
		}
		return out
	}

	// build mounts /backoffice/accounts/{param}/ with a transactions folder
	// and a consumers ObjectsFolder on the scope.
	build := func(opts ...chiutil.ObjectsOption) *chiutil.RouteFolder {
		router = chi.NewRouter()
		accounts = &TestAccountMapper{}
		accounts.accounts.Store("acc1", &TestAccount{ID: "acc1", Name: "Acme"})
		accounts.accounts.Store("acc2", &TestAccount{ID: "acc2", Name: "Globex"})
		bo := chiutil.NewRouteFolder(router, "/backoffice")
		scope := chiutil.ObjectsFolder(bo, "accounts", accounts, opts...).Description("Customer accounts").Scope()

		transactions := scope.Folder("transactions").Description("Account transactions")
		transactions.GetDesc("/list", "Transactions", func(w http.ResponseWriter, r *http.Request) {
			account, ok := chiutil.ItemOf[*TestAccount](r)
			Expect(ok).To(BeTrue())
			_, _ = w.Write([]byte("transactions of " + account.ID))
		})

		chiutil.ScopedObjectsFolder(scope, "consumers", &TestConsumerMapper{byAccount: map[string][]*TestConsumer{
			"acc1": {{ID: "c1", AccountID: "acc1"}, {ID: "c2", AccountID: "acc1"}},
			"acc2": {{ID: "c3", AccountID: "acc2"}},
		}}).FlatJSON()
		return bo
	}

	It("serves the item index with its routes and 404s every path of an unknown id", func() {
		build()
		index := indexOf("/backoffice/accounts/acc1/index.json")
		Expect(index.Title).To(Equal("Acc1"))
		Expect(index.Description).To(Equal("Customer accounts"))
		Expect(names(index)).To(Equal([]string{"consumers", "details", "settings", "transactions"}))

		for _, p := range []string{"/", "/index.json", "/details", "/settings", "/transactions/", "/transactions/list", "/consumers/", "/consumers/c1/details", "/bo.css"} {
			Expect(get("/backoffice/accounts/nope"+p).Code).To(Equal(http.StatusNotFound), "for %s", p)
		}
	})

	It("lists folders registered on the scope as folder entries", func() {
		build()
		index := indexOf("/backoffice/accounts/acc1/index.json")
		byName := map[string]*chiutil.RouteEntry{}
		for _, e := range index.Entries {
			byName[e.Name] = e
		}
		Expect(byName["transactions"].IsFolder).To(BeTrue())
		Expect(byName["transactions"].Description).To(Equal("Account transactions"))
		Expect(byName["consumers"].IsFolder).To(BeTrue())

		Expect(get("/backoffice/accounts/acc2/transactions/list").Body.String()).To(Equal("transactions of acc2"))
	})

	It("lists and resolves a scoped collection within its account only", func() {
		build()
		Expect(names(indexOf("/backoffice/accounts/acc1/consumers/index.json"))).To(Equal([]string{"c1", "c2"}))
		Expect(names(indexOf("/backoffice/accounts/acc2/consumers/index.json"))).To(Equal([]string{"c3"}))

		w := get("/backoffice/accounts/acc1/consumers/c1/details")
		Expect(w.Code).To(Equal(http.StatusOK))
		var body map[string]string
		Expect(json.Unmarshal(w.Body.Bytes(), &body)).To(Succeed())
		Expect(body).To(Equal(map[string]string{"id": "c1", "account": "acc1", "consumer": "c1"}))

		Expect(get("/backoffice/accounts/acc2/consumers/c1/details").Code).To(Equal(http.StatusNotFound))
		Expect(get("/backoffice/accounts/acc2/consumers/c1/").Code).To(Equal(http.StatusNotFound))
	})

	It("keeps a scoped FlatJSON inside the scope", func() {
		build()
		Expect(get("/backoffice/accounts/acc1/consumers/c1.json").Code).To(Equal(http.StatusOK))
		Expect(get("/backoffice/accounts/acc2/consumers/c1.json").Code).To(Equal(http.StatusNotFound))
	})

	It("publishes index paths with the real ids", func() {
		build()
		Expect(indexOf("/backoffice/accounts/acc1/index.json").Path).To(Equal("/accounts/acc1"))
		Expect(indexOf("/backoffice/accounts/acc1/transactions/index.json").Path).To(Equal("/accounts/acc1/transactions"))
		Expect(indexOf("/backoffice/accounts/acc1/consumers/index.json").Path).To(Equal("/accounts/acc1/consumers"))
		// Both levels use the default "id": each segment gets its own value.
		consumer := indexOf("/backoffice/accounts/acc1/consumers/c2/index.json")
		Expect(consumer.Path).To(Equal("/accounts/acc1/consumers/c2"))
		Expect(consumer.Title).To(Equal("C2"))
	})

	It("names the param with WithParam", func() {
		router = chi.NewRouter()
		accounts = &TestAccountMapper{}
		accounts.accounts.Store("acc1", &TestAccount{ID: "acc1"})
		bo := chiutil.NewRouteFolder(router, "/backoffice")
		chiutil.ObjectsFolder(bo, "accounts", accounts, chiutil.WithParam("accountId")).
			ItemIndex(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte("param " + chi.URLParam(r, "accountId") + "/" + chi.URLParam(r, "id")))
			})

		Expect(get("/backoffice/accounts/acc1/_index").Body.String()).To(Equal("param acc1/"))
		Expect(get("/backoffice/accounts/acc1/?preview=true").Body.String()).To(Equal("param acc1/"))
		Expect(indexOf("/backoffice/accounts/acc1/index.json").Path).To(Equal("/accounts/acc1"))
	})

	It("lists an Action route under its real method", func() {
		router = chi.NewRouter()
		accounts = &TestAccountMapper{}
		accounts.accounts.Store("acc1", &TestAccount{ID: "acc1"})
		bo := chiutil.NewRouteFolder(router, "/backoffice")
		chiutil.ObjectsFolder(bo, "accounts", actionTestAccountMapper{TestAccountMapper: accounts})

		index := indexOf("/backoffice/accounts/acc1/index.json")
		Expect(index.Entries).To(HaveLen(1))
		Expect(index.Entries[0].Method).To(Equal(http.MethodPost))
		Expect(get("/backoffice/accounts/acc1/report").Code).To(Equal(http.StatusOK))
		Expect(get("/backoffice/accounts/nope/report").Code).To(Equal(http.StatusNotFound))
	})

	Describe("navigable 404", func() {
		html := "text/html,application/xhtml+xml"
		hrefs := func(body string) []string {
			var out []string
			for _, m := range regexp.MustCompile(`href="([^"]*)"`).FindAllStringSubmatch(body, -1) {
				out = append(out, m[1])
			}
			return out
		}
		// resolve applies a relative href to the page URL the way a browser does.
		resolve := func(page, href string) string {
			base, err := url.Parse(page)
			Expect(err).NotTo(HaveOccurred())
			ref, err := url.Parse(href)
			Expect(err).NotTo(HaveOccurred())
			return base.ResolveReference(ref).Path
		}
		backOf := func(page string) string {
			w := get(page, "Accept", html)
			ExpectWithOffset(1, w.Code).To(Equal(http.StatusNotFound))
			ExpectWithOffset(1, w.Header().Get("Content-Type")).To(HavePrefix("text/html"))
			links := hrefs(w.Body.String())
			return resolve(page, links[len(links)-1])
		}

		It("leads from an unknown item back to its collection and home", func() {
			build()
			page := "/backoffice/accounts/nope/details"
			w := get(page, "Accept", html)
			Expect(w.Code).To(Equal(http.StatusNotFound))
			Expect(w.Body.String()).To(ContainSubstring(`accounts has no item &#34;nope&#34;`))
			links := hrefs(w.Body.String())
			Expect(links).To(Equal([]string{"../../", "../", "../"}))
			Expect(resolve(page, links[0])).To(Equal("/backoffice/"))
			Expect(resolve(page, links[1])).To(Equal("/backoffice/accounts/"))

			Expect(backOf("/backoffice/accounts/nope")).To(Equal("/backoffice/accounts/"))
			Expect(backOf("/backoffice/accounts/nope/")).To(Equal("/backoffice/accounts/"))
		})

		It("keeps the plain 404 for non-navigation requests", func() {
			build()
			for _, w := range []*httptest.ResponseRecorder{
				get("/backoffice/accounts/nope/details"),
				get("/backoffice/accounts/nope/index.json"),
				get("/backoffice/accounts/nope/?preview=true", "Accept", html),
			} {
				Expect(w.Code).To(Equal(http.StatusNotFound))
				Expect(w.Body.String()).To(Equal("404 page not found\n"))
			}
		})

		It("leads from a mistyped route back to the nearest folder with real ids", func() {
			build()
			page := "/backoffice/accounts/acc1/transactions/nope"
			w := get(page, "Accept", html)
			Expect(w.Body.String()).To(ContainSubstring(`&#34;nope&#34; not found in transactions`))
			Expect(w.Body.String()).To(ContainSubstring(`>acc1</a>`))
			Expect(backOf(page)).To(Equal("/backoffice/accounts/acc1/transactions/"))

			Expect(backOf("/backoffice/accounts/acc1/consumers/c1/nope/deeper/")).To(Equal("/backoffice/accounts/acc1/consumers/c1/"))
			Expect(backOf("/backoffice/accounts/acc1/consumers/zz/details")).To(Equal("/backoffice/accounts/acc1/consumers/"))
		})

		It("leads from a missing static file back to its folder", func() {
			router = chi.NewRouter()
			dir := GinkgoT().TempDir()
			Expect(os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644)).To(Succeed())
			bo := chiutil.NewRouteFolder(router, "/backoffice")
			bo.StaticFilesFolder("files", dir)

			Expect(backOf("/backoffice/files/missing/b.txt")).To(Equal("/backoffice/files/"))
		})

		It("404s a wildcard instance that was never added, on every path below it", func() {
			router = chi.NewRouter()
			bo := chiutil.NewRouteFolder(router, "/backoffice")
			components := bo.WildcardFolder("components", "name", func(r chi.Router) {
				r.Get("/info", func(w http.ResponseWriter, r *http.Request) {
					_, _ = w.Write([]byte("info " + chi.URLParam(r, "name")))
				})
			}).Title("Components")
			components.Add("alpha", "Alpha")

			Expect(get("/backoffice/components/alpha/info").Body.String()).To(Equal("info alpha"))
			Expect(get("/backoffice/components/alpha/index.json").Code).To(Equal(http.StatusOK))
			for _, p := range []string{"/", "/index.json", "/info"} {
				Expect(get("/backoffice/components/beta"+p).Code).To(Equal(http.StatusNotFound), "for %s", p)
			}
			w := get("/backoffice/components/beta/info", "Accept", html)
			Expect(w.Body.String()).To(ContainSubstring(`Components has no item &#34;beta&#34;`))
			Expect(backOf("/backoffice/components/beta/info")).To(Equal("/backoffice/components/"))

			components.Remove("alpha")
			Expect(get("/backoffice/components/alpha/info").Code).To(Equal(http.StatusNotFound))
		})

		It("lets a handler answer the navigable 404 with chiutil.NotFound", func() {
			// A route on the item scope that finds nothing to show.
			router = chi.NewRouter()
			accounts = &TestAccountMapper{}
			accounts.accounts.Store("acc1", &TestAccount{ID: "acc1"})
			bo := chiutil.NewRouteFolder(router, "/backoffice")
			scope := chiutil.ObjectsFolder(bo, "accounts", accounts).Scope()
			scope.Folder("billing").GetDesc("/contract", "Contract", func(w http.ResponseWriter, r *http.Request) {
				chiutil.NotFound(w, r)
			})

			page := "/backoffice/accounts/acc1/billing/contract"
			w := get(page, "Accept", html)
			Expect(w.Code).To(Equal(http.StatusNotFound))
			Expect(w.Body.String()).To(ContainSubstring(`&#34;contract&#34; not found in billing`))
			Expect(backOf(page)).To(Equal("/backoffice/accounts/acc1/billing/"))
			Expect(get(page).Body.String()).To(Equal("404 page not found\n"))

			// Outside a folder tree it is the plain http.NotFound.
			plain := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/x", nil)
			req.Header.Set("Accept", html)
			chiutil.NotFound(plain, req)
			Expect(plain.Code).To(Equal(http.StatusNotFound))
			Expect(plain.Body.String()).To(Equal("404 page not found\n"))
		})

		It("stays relative behind a path-stripping proxy", func() {
			build()
			// The browser sees /proxy/svc/backoffice/...; the proxy strips
			// /proxy/svc before it reaches the server.
			w := get("/backoffice/accounts/nope/details", "Accept", html)
			for _, href := range hrefs(w.Body.String()) {
				Expect(strings.HasPrefix(href, "/")).To(BeFalse(), href)
			}
			links := hrefs(w.Body.String())
			Expect(resolve("/proxy/svc/backoffice/accounts/nope/details", links[0])).To(Equal("/proxy/svc/backoffice/"))
		})
	})
})
