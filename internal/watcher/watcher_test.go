package watcher_test

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/gur-shatz/go-run/internal/glob"
	"github.com/gur-shatz/go-run/internal/hasher"
	"github.com/gur-shatz/go-run/internal/log"
	"github.com/gur-shatz/go-run/internal/sumfile"
	"github.com/gur-shatz/go-run/internal/watcher"
)

var _ = Describe("Watcher", func() {
	var (
		tmpDir   string
		patterns []glob.Pattern
	)

	testLogger := log.New("[test]", false)

	BeforeEach(func() {
		tmpDir = GinkgoT().TempDir()
		patterns = []glob.Pattern{{Raw: "**/*.txt"}}
	})

	writeFile := func(name, content string) {
		path := filepath.Join(tmpDir, name)
		Expect(os.MkdirAll(filepath.Dir(path), 0755)).To(Succeed())
		Expect(os.WriteFile(path, []byte(content), 0644)).To(Succeed())
	}

	hashFile := func(name string) string {
		h, err := hasher.HashFile(filepath.Join(tmpDir, name))
		Expect(err).NotTo(HaveOccurred())
		return h
	}

	scanInitial := func() map[string]string {
		files, err := glob.ExpandPatterns(tmpDir, patterns)
		Expect(err).NotTo(HaveOccurred())
		sums := make(map[string]string, len(files))
		for _, f := range files {
			sums[f] = hashFile(f)
		}
		return sums
	}

	Describe("file change detection", func() {
		It("detects modified files", func() {
			writeFile("a.txt", "original")

			var mu sync.Mutex
			var received *sumfile.ChangeSet

			initialSums := scanInitial()

			w := watcher.New(tmpDir, patterns, 50*time.Millisecond, 50*time.Millisecond, func(changes sumfile.ChangeSet) {
				mu.Lock()
				defer mu.Unlock()
				received = &changes
			}, testLogger)
			w.SetCurrentSums(initialSums)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go w.Run(ctx)

			// Let the watcher start
			time.Sleep(100 * time.Millisecond)

			// Modify the file
			writeFile("a.txt", "modified content")

			Eventually(func() *sumfile.ChangeSet {
				mu.Lock()
				defer mu.Unlock()
				return received
			}, 3*time.Second, 50*time.Millisecond).ShouldNot(BeNil())

			mu.Lock()
			defer mu.Unlock()
			Expect(received.Modified).To(ContainElement("a.txt"))
		})

		It("detects added files as pre-existing but unknown", func() {
			// The watcher detects files that exist on disk but were not in the
			// initial sums (simulating a file added between sum and watcher start).
			writeFile("a.txt", "existing")
			writeFile("b.txt", "also exists")

			// Only include a.txt in initial sums
			aHash := hashFile("a.txt")
			initialSums := map[string]string{"a.txt": aHash}

			var mu sync.Mutex
			var received *sumfile.ChangeSet

			w := watcher.New(tmpDir, patterns, 50*time.Millisecond, 50*time.Millisecond, func(changes sumfile.ChangeSet) {
				mu.Lock()
				defer mu.Unlock()
				received = &changes
			}, testLogger)
			w.SetCurrentSums(initialSums)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go w.Run(ctx)

			// Trigger a scan by modifying a tracked file
			time.Sleep(100 * time.Millisecond)
			writeFile("a.txt", "modified existing")

			Eventually(func() *sumfile.ChangeSet {
				mu.Lock()
				defer mu.Unlock()
				return received
			}, 5*time.Second, 50*time.Millisecond).ShouldNot(BeNil())

			mu.Lock()
			defer mu.Unlock()
			// a.txt modified and b.txt should show up as added (it's in
			// trackedFiles from buildFileList but not in the initial sums)
			Expect(received.Modified).To(ContainElement("a.txt"))
			Expect(received.Added).To(ContainElement("b.txt"))
		})

		It("detects removed files", func() {
			writeFile("a.txt", "to be removed")
			writeFile("b.txt", "stays")

			var mu sync.Mutex
			var received *sumfile.ChangeSet

			initialSums := scanInitial()

			w := watcher.New(tmpDir, patterns, 50*time.Millisecond, 50*time.Millisecond, func(changes sumfile.ChangeSet) {
				mu.Lock()
				defer mu.Unlock()
				received = &changes
			}, testLogger)
			w.SetCurrentSums(initialSums)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go w.Run(ctx)

			time.Sleep(100 * time.Millisecond)

			Expect(os.Remove(filepath.Join(tmpDir, "a.txt"))).To(Succeed())

			Eventually(func() *sumfile.ChangeSet {
				mu.Lock()
				defer mu.Unlock()
				return received
			}, 3*time.Second, 50*time.Millisecond).ShouldNot(BeNil())

			mu.Lock()
			defer mu.Unlock()
			Expect(received.Removed).To(ContainElement("a.txt"))
		})
	})

	Describe("negation patterns", func() {
		It("excludes files matching negation patterns", func() {
			patterns = []glob.Pattern{
				{Raw: "**/*.txt"},
				{Raw: "ignored.txt", Negated: true},
			}
			writeFile("a.txt", "watched")
			writeFile("ignored.txt", "excluded")

			var mu sync.Mutex
			var received *sumfile.ChangeSet

			initialSums := scanInitial()
			Expect(initialSums).NotTo(HaveKey("ignored.txt"))

			w := watcher.New(tmpDir, patterns, 50*time.Millisecond, 50*time.Millisecond, func(changes sumfile.ChangeSet) {
				mu.Lock()
				defer mu.Unlock()
				received = &changes
			}, testLogger)
			w.SetCurrentSums(initialSums)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go w.Run(ctx)

			time.Sleep(100 * time.Millisecond)

			// Modify only the ignored file
			writeFile("ignored.txt", "modified excluded content")

			// Should NOT trigger a change
			Consistently(func() *sumfile.ChangeSet {
				mu.Lock()
				defer mu.Unlock()
				return received
			}, 500*time.Millisecond, 50*time.Millisecond).Should(BeNil())
		})
	})

	Describe("context cancellation", func() {
		It("stops the watcher", func() {
			writeFile("a.txt", "content")

			initialSums := scanInitial()

			done := make(chan struct{})
			w := watcher.New(tmpDir, patterns, 50*time.Millisecond, 50*time.Millisecond, func(changes sumfile.ChangeSet) {}, testLogger)
			w.SetCurrentSums(initialSums)

			ctx, cancel := context.WithCancel(context.Background())
			go func() {
				w.Run(ctx)
				close(done)
			}()

			time.Sleep(100 * time.Millisecond)
			cancel()

			Eventually(done, 2*time.Second).Should(BeClosed())
		})
	})
})
