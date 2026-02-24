package chiutil_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestChiutil(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Chiutil Suite")
}
