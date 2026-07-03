package pprofdump

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

func TestHandlerWritesProfiles(t *testing.T) {
	dir := t.TempDir()
	h := Handler(Options{
		Dir:       dir,
		RuntimeGC: boolPtr(false),
		Now: func() time.Time {
			return time.Date(2026, 7, 3, 12, 0, 0, 123, time.UTC)
		},
		Profiles: []Profile{{Name: "goroutine", Debug: 2, File: "goroutine.txt"}},
	})

	req := httptest.NewRequest(http.MethodPost, "/debug/pprof/dump", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp DumpResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Files) != 1 || resp.Files[0] != "goroutine.txt" {
		t.Fatalf("files = %#v", resp.Files)
	}
	if _, err := os.Stat(filepath.Join(resp.Dir, "goroutine.txt")); err != nil {
		t.Fatalf("dump file missing: %v", err)
	}
}

func TestHandlerRejectsNonPOST(t *testing.T) {
	h := Handler(Options{Dir: t.TempDir(), RuntimeGC: boolPtr(false)})
	req := httptest.NewRequest(http.MethodGet, "/debug/pprof/dump", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestHandlerRejectsUnsafeProfileFile(t *testing.T) {
	h := Handler(Options{
		Dir:       t.TempDir(),
		RuntimeGC: boolPtr(false),
		Profiles:  []Profile{{Name: "goroutine", Debug: 2, File: "../x"}},
	})
	req := httptest.NewRequest(http.MethodPost, "/debug/pprof/dump", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestRegisterChi(t *testing.T) {
	dir := t.TempDir()
	r := chi.NewRouter()
	RegisterChi(r, "/debug/pprof/dump", Options{
		Dir:       dir,
		RuntimeGC: boolPtr(false),
		Profiles:  []Profile{{Name: "goroutine", Debug: 2}},
	})

	req := httptest.NewRequest(http.MethodPost, "/debug/pprof/dump", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/debug/pprof/dump", nil)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET status = %d", rec.Code)
	}
}

func boolPtr(v bool) *bool { return &v }
