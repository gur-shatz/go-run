package supervisor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("ProcessLog", func() {
	It("marks a previous running status as crashed on next startup", func() {
		dir := GinkgoT().TempDir()
		paths := NewPaths(dir)
		runDir := paths.SupervisorRunLogs("20260611-120000Z-pid42")
		Expect(os.MkdirAll(runDir, 0o755)).To(Succeed())
		Expect(writeProcessLogStatus(filepath.Join(runDir, "status.json"), ProcessLogStatus{
			RunID:     "20260611-120000Z-pid42",
			PID:       42,
			Status:    "running",
			StartedAt: time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC).Format(time.RFC3339),
		})).To(Succeed())

		Expect(markStaleProcessLogs(paths)).To(Succeed())

		data, err := os.ReadFile(filepath.Join(runDir, "status.json"))
		Expect(err).NotTo(HaveOccurred())
		var status ProcessLogStatus
		Expect(json.Unmarshal(data, &status)).To(Succeed())
		Expect(status.Status).To(Equal("crashed"))
		Expect(status.EndedAt).NotTo(BeEmpty())
		Expect(status.Message).To(ContainSubstring("clean exit"))
	})
})
