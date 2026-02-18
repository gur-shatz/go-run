package glob_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestGlob(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Glob Suite")
}
