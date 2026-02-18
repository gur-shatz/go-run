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
		It("parses build target only", func() {
			cfg, err := cli.Parse([]string{"./cmd/server"})
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.Command).To(Equal(cli.CommandRun))
			Expect(cfg.BuildTarget).To(Equal("./cmd/server"))
			Expect(cfg.AppArgs).To(BeEmpty())
			Expect(cfg.PollInterval).To(Equal(500 * time.Millisecond))
			Expect(cfg.Debounce).To(Equal(300 * time.Millisecond))
			Expect(cfg.Verbose).To(BeFalse())
		})

		It("parses build target with app args", func() {
			cfg, err := cli.Parse([]string{"./cmd/server", "-port", "8080"})
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.Command).To(Equal(cli.CommandRun))
			Expect(cfg.BuildTarget).To(Equal("./cmd/server"))
			Expect(cfg.AppArgs).To(Equal([]string{"-port", "8080"}))
		})

		It("parses all gorun flags", func() {
			cfg, err := cli.Parse([]string{"--poll", "1s", "--debounce", "200ms", "-v", "./cmd/server"})
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.PollInterval).To(Equal(time.Second))
			Expect(cfg.Debounce).To(Equal(200 * time.Millisecond))
			Expect(cfg.Verbose).To(BeTrue())
			Expect(cfg.BuildTarget).To(Equal("./cmd/server"))
		})

		It("parses --verbose flag", func() {
			cfg, err := cli.Parse([]string{"--verbose", "./cmd/server"})
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.Verbose).To(BeTrue())
		})

		It("returns CommandRun with verbose when no build target", func() {
			cfg, err := cli.Parse([]string{"-v"})
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.Command).To(Equal(cli.CommandRun))
			Expect(cfg.Verbose).To(BeTrue())
			Expect(cfg.BuildTarget).To(BeEmpty())
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
			Expect(cfg.BuildTarget).To(BeEmpty())
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

		It("returns flag.ErrHelp for -h", func() {
			_, err := cli.Parse([]string{"-h"})
			Expect(err).To(Equal(flag.ErrHelp))
		})

		It("returns flag.ErrHelp for --help", func() {
			_, err := cli.Parse([]string{"--help"})
			Expect(err).To(Equal(flag.ErrHelp))
		})


		Context("go build flags", func() {
			It("passes -race via -- separator", func() {
				cfg, err := cli.Parse([]string{"-v", "--", "-race", "./cmd/server"})
				Expect(err).NotTo(HaveOccurred())
				Expect(cfg.Verbose).To(BeTrue())
				Expect(cfg.BuildFlags).To(Equal([]string{"-race"}))
				Expect(cfg.BuildTarget).To(Equal("./cmd/server"))
				Expect(cfg.AppArgs).To(BeEmpty())
			})

			It("passes -tags with value", func() {
				cfg, err := cli.Parse([]string{"--", "-tags", "integration", "./cmd/server"})
				Expect(err).NotTo(HaveOccurred())
				Expect(cfg.BuildFlags).To(Equal([]string{"-tags", "integration"}))
				Expect(cfg.BuildTarget).To(Equal("./cmd/server"))
			})

			It("passes -ldflags with quoted value", func() {
				cfg, err := cli.Parse([]string{"--", "-ldflags", "-X main.version=1.0", "./cmd/server"})
				Expect(err).NotTo(HaveOccurred())
				Expect(cfg.BuildFlags).To(Equal([]string{"-ldflags", "-X main.version=1.0"}))
				Expect(cfg.BuildTarget).To(Equal("./cmd/server"))
			})

			It("handles build flags + build target + app args", func() {
				cfg, err := cli.Parse([]string{"-v", "--", "-race", "-tags=e2e", "./cmd/server", "-port", "8080"})
				Expect(err).NotTo(HaveOccurred())
				Expect(cfg.Verbose).To(BeTrue())
				Expect(cfg.BuildFlags).To(Equal([]string{"-race", "-tags=e2e"}))
				Expect(cfg.BuildTarget).To(Equal("./cmd/server"))
				Expect(cfg.AppArgs).To(Equal([]string{"-port", "8080"}))
			})

			It("has no build flags without -- separator", func() {
				cfg, err := cli.Parse([]string{"./cmd/server", "-port", "8080"})
				Expect(err).NotTo(HaveOccurred())
				Expect(cfg.BuildFlags).To(BeEmpty())
				Expect(cfg.BuildTarget).To(Equal("./cmd/server"))
				Expect(cfg.AppArgs).To(Equal([]string{"-port", "8080"}))
			})
		})

		Context("subcommands", func() {
			It("parses init without target", func() {
				cfg, err := cli.Parse([]string{"init"})
				Expect(err).NotTo(HaveOccurred())
				Expect(cfg.Command).To(Equal(cli.CommandInit))
				Expect(cfg.BuildTarget).To(BeEmpty())
			})

			It("parses init with -c flag", func() {
				cfg, err := cli.Parse([]string{"-c", "myapp.yaml", "init"})
				Expect(err).NotTo(HaveOccurred())
				Expect(cfg.Command).To(Equal(cli.CommandInit))
				Expect(cfg.ConfigFile).To(Equal("myapp.yaml"))
			})

			It("parses sum without config", func() {
				cfg, err := cli.Parse([]string{"sum"})
				Expect(err).NotTo(HaveOccurred())
				Expect(cfg.Command).To(Equal(cli.CommandSum))
				Expect(cfg.ConfigFile).To(BeEmpty())
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
		It("returns gorun.config for empty target", func() {
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
