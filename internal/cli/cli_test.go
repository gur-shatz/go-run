package cli_test

import (
	"flag"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/gur-shatz/go-run/internal/cli"
)

var _ = Describe("CLI", func() {
	Describe("Parse", func() {
		It("parses all gorun flags", func() {
			cfg, err := cli.Parse([]string{"--poll", "1s", "--debounce", "200ms", "-v"})
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.Command).To(Equal(cli.CommandRun))
			Expect(cfg.PollInterval).To(Equal(time.Second))
			Expect(cfg.Debounce).To(Equal(200 * time.Millisecond))
			Expect(cfg.Verbose).To(BeTrue())
		})

		It("parses --verbose flag", func() {
			cfg, err := cli.Parse([]string{"--verbose"})
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.Verbose).To(BeTrue())
		})

		It("returns CommandRun with verbose when no subcommand", func() {
			cfg, err := cli.Parse([]string{"-v"})
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.Command).To(Equal(cli.CommandRun))
			Expect(cfg.Verbose).To(BeTrue())
		})

		It("returns error when --poll missing value", func() {
			_, err := cli.Parse([]string{"--poll"})
			Expect(err).To(HaveOccurred())
		})

		It("returns error when --debounce missing value", func() {
			_, err := cli.Parse([]string{"--debounce"})
			Expect(err).To(HaveOccurred())
		})

		It("returns CommandRun for empty args (load from gorun.yaml)", func() {
			cfg, err := cli.Parse([]string{})
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.Command).To(Equal(cli.CommandRun))
		})

		It("parses -c flag with run command", func() {
			cfg, err := cli.Parse([]string{"-c", "myapp.yaml"})
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.Command).To(Equal(cli.CommandRun))
			Expect(cfg.ConfigFile).To(Equal("myapp.yaml"))
		})

		It("parses --config flag", func() {
			cfg, err := cli.Parse([]string{"--config", "myapp.yaml"})
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.ConfigFile).To(Equal("myapp.yaml"))
		})

		It("parses --stdout flag", func() {
			cfg, err := cli.Parse([]string{"--stdout", "out.log"})
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.Stdout).To(Equal("out.log"))
		})

		It("parses --stderr flag", func() {
			cfg, err := cli.Parse([]string{"--stderr", "err.log"})
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.Stderr).To(Equal("err.log"))
		})

		It("parses --combined flag", func() {
			cfg, err := cli.Parse([]string{"--combined", "all.log"})
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.Combined).To(Equal("all.log"))
		})

		It("returns flag.ErrHelp for -h", func() {
			_, err := cli.Parse([]string{"-h"})
			Expect(err).To(Equal(flag.ErrHelp))
		})

		It("returns flag.ErrHelp for --help", func() {
			_, err := cli.Parse([]string{"--help"})
			Expect(err).To(Equal(flag.ErrHelp))
		})

		It("returns error for unknown subcommand", func() {
			_, err := cli.Parse([]string{"./cmd/server"})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("unknown subcommand"))
		})

		Context("subcommands", func() {
			It("parses init", func() {
				cfg, err := cli.Parse([]string{"init"})
				Expect(err).NotTo(HaveOccurred())
				Expect(cfg.Command).To(Equal(cli.CommandInit))
			})

			It("parses init with -c flag", func() {
				cfg, err := cli.Parse([]string{"-c", "myapp.yaml", "init"})
				Expect(err).NotTo(HaveOccurred())
				Expect(cfg.Command).To(Equal(cli.CommandInit))
				Expect(cfg.ConfigFile).To(Equal("myapp.yaml"))
			})

			It("parses sum", func() {
				cfg, err := cli.Parse([]string{"sum"})
				Expect(err).NotTo(HaveOccurred())
				Expect(cfg.Command).To(Equal(cli.CommandSum))
			})

			It("parses sum with -c flag", func() {
				cfg, err := cli.Parse([]string{"-c", "myapp.yaml", "sum"})
				Expect(err).NotTo(HaveOccurred())
				Expect(cfg.Command).To(Equal(cli.CommandSum))
				Expect(cfg.ConfigFile).To(Equal("myapp.yaml"))
			})
		})
	})

	Describe("FlattenTarget", func() {
		DescribeTable("flattens build targets",
			func(input, expected string) {
				Expect(cli.FlattenTarget(input)).To(Equal(expected))
			},
			Entry("package path", "./cmd/mypkg", "cmd_mypkg"),
			Entry("nested path", "./cmd/mypkg/sub", "cmd_mypkg_sub"),
			Entry("file path", "./cmd/mypkg/main.go", "cmd_mypkg_main"),
			Entry("dot target", ".", ""),
			Entry("empty target", "", ""),
			Entry("simple package", "./server", "server"),
			Entry("no dot-slash prefix", "cmd/server", "cmd_server"),
			Entry("trailing slash", "./cmd/server/", "cmd_server"),
		)
	})

	Describe("ConfigFileName", func() {
		It("returns gorun.yaml for empty target", func() {
			Expect(cli.ConfigFileName("")).To(Equal("gorun.yaml"))
		})

		It("returns target-specific name", func() {
			Expect(cli.ConfigFileName("./cmd/server")).To(Equal("gorun_cmd_server.yaml"))
		})

		It("handles file targets", func() {
			Expect(cli.ConfigFileName("./cmd/mypkg/main.go")).To(Equal("gorun_cmd_mypkg_main.yaml"))
		})
	})

	Describe("SumFileName", func() {
		It("returns gorun.sum for empty target", func() {
			Expect(cli.SumFileName("")).To(Equal("gorun.sum"))
		})

		It("returns target-specific name", func() {
			Expect(cli.SumFileName("./cmd/server")).To(Equal("gorun_cmd_server.sum"))
		})
	})

	Describe("BinFileName", func() {
		It("returns gorun.bin for empty target", func() {
			Expect(cli.BinFileName("")).To(Equal("gorun.bin"))
		})

		It("returns target-specific name", func() {
			Expect(cli.BinFileName("./cmd/server")).To(Equal("gorun_cmd_server.bin"))
		})
	})
})
