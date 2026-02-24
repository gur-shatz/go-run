package backoffice_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestBackoffice(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Backoffice Suite")
}
