package execrun_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestExecrun(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Execrun Suite")
}
