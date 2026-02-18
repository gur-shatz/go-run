package gorun_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestGorun(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Gorun Suite")
}
