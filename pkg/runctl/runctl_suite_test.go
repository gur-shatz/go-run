package runctl_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestRunctl(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Runctl Suite")
}
