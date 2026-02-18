package sumfile_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestSumfile(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Sumfile Suite")
}
