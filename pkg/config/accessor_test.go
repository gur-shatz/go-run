package config_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gopkg.in/yaml.v3"

	"github.com/gur-shatz/go-run/pkg/config"
)

var _ = Describe("Config O", func() {
	Describe("Get and GetString", func() {
		var cfg config.O

		Context("with simple key-value pairs", func() {
			BeforeEach(func() {
				yamlData := `
name: test-app
port: 8080
enabled: true
`
				Expect(yaml.Unmarshal([]byte(yamlData), &cfg)).To(Succeed())
			})

			It("should get string value", func() {
				val, ok := cfg.Get("name")
				Expect(ok).To(BeTrue())
				Expect(val).To(Equal("test-app"))
			})

			It("should get int value", func() {
				val, ok := cfg.Get("port")
				Expect(ok).To(BeTrue())
				Expect(val).To(Equal(8080))
			})

			It("should get bool value", func() {
				val, ok := cfg.Get("enabled")
				Expect(ok).To(BeTrue())
				Expect(val).To(BeTrue())
			})

			It("should return false for non-existent key", func() {
				_, ok := cfg.Get("nonexistent")
				Expect(ok).To(BeFalse())
			})

			It("should GetString for string value", func() {
				Expect(cfg.GetString("name")).To(Equal("test-app"))
			})

			It("should GetString for int value", func() {
				Expect(cfg.GetString("port")).To(Equal("8080"))
			})

			It("should GetString for bool value", func() {
				Expect(cfg.GetString("enabled")).To(Equal("true"))
			})

			It("should return empty string for non-existent key", func() {
				Expect(cfg.GetString("nonexistent")).To(Equal(""))
			})
		})

		Context("with nested configuration", func() {
			BeforeEach(func() {
				yamlData := `
database:
  type: postgres
  host: localhost
  port: 5432
  credentials:
    username: admin
    password: secret
server:
  http:
    port: 8080
    timeout: 30
`
				Expect(yaml.Unmarshal([]byte(yamlData), &cfg)).To(Succeed())
			})

			It("should get nested value with dot notation", func() {
				val, ok := cfg.Get("database.type")
				Expect(ok).To(BeTrue())
				Expect(val).To(Equal("postgres"))
			})

			It("should get deeply nested value", func() {
				val, ok := cfg.Get("database.credentials.username")
				Expect(ok).To(BeTrue())
				Expect(val).To(Equal("admin"))
			})

			It("should get nested int value", func() {
				val, ok := cfg.Get("server.http.port")
				Expect(ok).To(BeTrue())
				Expect(val).To(Equal(8080))
			})

			It("should return false for partial path that doesn't exist", func() {
				_, ok := cfg.Get("database.nonexistent.value")
				Expect(ok).To(BeFalse())
			})

			It("should return false when path goes through non-map value", func() {
				_, ok := cfg.Get("database.type.invalid")
				Expect(ok).To(BeFalse())
			})

			It("should GetString for nested value", func() {
				Expect(cfg.GetString("database.host")).To(Equal("localhost"))
			})

			It("should GetString for deeply nested int", func() {
				Expect(cfg.GetString("server.http.timeout")).To(Equal("30"))
			})

			It("should return entire nested map and use GetInto", func() {
				val, ok := cfg.Get("database.credentials")
				Expect(ok).To(BeTrue())
				Expect(val).ToNot(BeNil())

				type Credentials struct {
					Username string `yaml:"username"`
					Password string `yaml:"password"`
				}
				var creds Credentials
				Expect(cfg.GetInto("database.credentials", &creds)).To(Succeed())
				Expect(creds.Username).To(Equal("admin"))
				Expect(creds.Password).To(Equal("secret"))
			})
		})

		Context("with nil config", func() {
			It("should return false for Get on nil O", func() {
				var nilCfg config.O
				_, ok := nilCfg.Get("any.key")
				Expect(ok).To(BeFalse())
			})

			It("should return empty string for GetString on nil O", func() {
				var nilCfg config.O
				Expect(nilCfg.GetString("any.key")).To(Equal(""))
			})
		})
	})

	Describe("GetNumber", func() {
		var cfg config.O

		BeforeEach(func() {
			yamlData := `
port: 8080
rate: 3.14
negative: -42
big: 9999999999
`
			Expect(yaml.Unmarshal([]byte(yamlData), &cfg)).To(Succeed())
		})

		It("should get int value as int", func() {
			val, ok := config.GetNumber[int](cfg, "port")
			Expect(ok).To(BeTrue())
			Expect(val).To(Equal(8080))
		})

		It("should get int value as int64", func() {
			val, ok := config.GetNumber[int64](cfg, "port")
			Expect(ok).To(BeTrue())
			Expect(val).To(Equal(int64(8080)))
		})

		It("should get int value as float64", func() {
			val, ok := config.GetNumber[float64](cfg, "port")
			Expect(ok).To(BeTrue())
			Expect(val).To(BeNumerically("~", 8080.0, 0.001))
		})

		It("should get float value as float64", func() {
			val, ok := config.GetNumber[float64](cfg, "rate")
			Expect(ok).To(BeTrue())
			Expect(val).To(BeNumerically("~", 3.14, 0.001))
		})

		It("should get float value as int (truncated)", func() {
			val, ok := config.GetNumber[int](cfg, "rate")
			Expect(ok).To(BeTrue())
			Expect(val).To(Equal(3))
		})

		It("should get negative value", func() {
			val, ok := config.GetNumber[int](cfg, "negative")
			Expect(ok).To(BeTrue())
			Expect(val).To(Equal(-42))
		})

		It("should get big value as int64", func() {
			val, ok := config.GetNumber[int64](cfg, "big")
			Expect(ok).To(BeTrue())
			Expect(val).To(Equal(int64(9999999999)))
		})

		It("should return false for non-existent key", func() {
			val, ok := config.GetNumber[int](cfg, "nonexistent")
			Expect(ok).To(BeFalse())
			Expect(val).To(Equal(0))
		})

		It("should return false for non-numeric value", func() {
			yamlData := `name: test`
			Expect(yaml.Unmarshal([]byte(yamlData), &cfg)).To(Succeed())
			val, ok := config.GetNumber[int](cfg, "name")
			Expect(ok).To(BeFalse())
			Expect(val).To(Equal(0))
		})

		It("should coerce uint to int", func() {
			val, ok := config.GetNumber[uint](cfg, "port")
			Expect(ok).To(BeTrue())
			Expect(val).To(Equal(uint(8080)))
		})
	})

	Describe("GetStringOrDefault", func() {
		var cfg config.O

		BeforeEach(func() {
			yamlData := `
name: myapp
port: 8080
`
			Expect(yaml.Unmarshal([]byte(yamlData), &cfg)).To(Succeed())
		})

		It("should return value when key exists", func() {
			Expect(cfg.GetStringOrDefault("name", "default")).To(Equal("myapp"))
		})

		It("should return default when key doesn't exist", func() {
			Expect(cfg.GetStringOrDefault("missing", "default")).To(Equal("default"))
		})

		It("should convert non-string to string", func() {
			Expect(cfg.GetStringOrDefault("port", "3000")).To(Equal("8080"))
		})
	})

	Describe("GetNumberOrDefault", func() {
		var cfg config.O

		BeforeEach(func() {
			yamlData := `
port: 8080
rate: 3.14
name: test
`
			Expect(yaml.Unmarshal([]byte(yamlData), &cfg)).To(Succeed())
		})

		It("should return value when key exists", func() {
			Expect(config.GetNumberOrDefault[int](cfg, "port", 3000)).To(Equal(8080))
		})

		It("should return default when key doesn't exist", func() {
			Expect(config.GetNumberOrDefault[int](cfg, "missing", 3000)).To(Equal(3000))
		})

		It("should return default for non-numeric value", func() {
			Expect(config.GetNumberOrDefault[int](cfg, "name", 42)).To(Equal(42))
		})

		It("should work with float64", func() {
			Expect(config.GetNumberOrDefault[float64](cfg, "rate", 1.0)).To(BeNumerically("~", 3.14, 0.001))
		})

		It("should return float64 default when missing", func() {
			Expect(config.GetNumberOrDefault[float64](cfg, "missing", 1.5)).To(Equal(1.5))
		})
	})

	Describe("GetInto", func() {
		var cfg config.O

		Context("with struct target", func() {
			type DatabaseConfig struct {
				Type     string `yaml:"type"`
				Host     string `yaml:"host"`
				Port     int    `yaml:"port"`
				MaxConns int    `yaml:"max_conns"`
			}

			BeforeEach(func() {
				yamlData := `
database:
  type: postgres
  host: localhost
  port: 5432
  max_conns: 100
`
				Expect(yaml.Unmarshal([]byte(yamlData), &cfg)).To(Succeed())
			})

			It("should decode nested config into struct", func() {
				var dbCfg DatabaseConfig
				Expect(cfg.GetInto("database", &dbCfg)).To(Succeed())
				Expect(dbCfg.Type).To(Equal("postgres"))
				Expect(dbCfg.Host).To(Equal("localhost"))
				Expect(dbCfg.Port).To(Equal(5432))
				Expect(dbCfg.MaxConns).To(Equal(100))
			})

			It("should return error for non-existent path", func() {
				var dbCfg DatabaseConfig
				err := cfg.GetInto("nonexistent", &dbCfg)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("key not found"))
			})
		})

		Context("with nested struct", func() {
			type Credentials struct {
				Username string `yaml:"username"`
				Password string `yaml:"password"`
			}

			type ServerConfig struct {
				Host        string      `yaml:"host"`
				Port        int         `yaml:"port"`
				Credentials Credentials `yaml:"credentials"`
			}

			BeforeEach(func() {
				yamlData := `
server:
  host: api.example.com
  port: 443
  credentials:
    username: admin
    password: secret123
`
				Expect(yaml.Unmarshal([]byte(yamlData), &cfg)).To(Succeed())
			})

			It("should decode deeply nested config", func() {
				var serverCfg ServerConfig
				Expect(cfg.GetInto("server", &serverCfg)).To(Succeed())
				Expect(serverCfg.Host).To(Equal("api.example.com"))
				Expect(serverCfg.Port).To(Equal(443))
				Expect(serverCfg.Credentials.Username).To(Equal("admin"))
				Expect(serverCfg.Credentials.Password).To(Equal("secret123"))
			})

			It("should decode just the credentials", func() {
				var creds Credentials
				Expect(cfg.GetInto("server.credentials", &creds)).To(Succeed())
				Expect(creds.Username).To(Equal("admin"))
				Expect(creds.Password).To(Equal("secret123"))
			})

			It("should decode simple types", func() {
				var port int
				Expect(cfg.GetInto("server.port", &port)).To(Succeed())
				Expect(port).To(Equal(443))
			})
		})

		Context("with slice fields", func() {
			type FeatureConfig struct {
				Name    string   `yaml:"name"`
				Enabled bool     `yaml:"enabled"`
				Tags    []string `yaml:"tags"`
			}

			type AppConfig struct {
				Features []FeatureConfig `yaml:"features"`
			}

			BeforeEach(func() {
				yamlData := `
app:
  features:
    - name: feature1
      enabled: true
      tags:
        - prod
        - stable
    - name: feature2
      enabled: false
      tags:
        - beta
`
				Expect(yaml.Unmarshal([]byte(yamlData), &cfg)).To(Succeed())
			})

			It("should decode config with slices", func() {
				var appCfg AppConfig
				Expect(cfg.GetInto("app", &appCfg)).To(Succeed())
				Expect(appCfg.Features).To(HaveLen(2))
				Expect(appCfg.Features[0].Name).To(Equal("feature1"))
				Expect(appCfg.Features[0].Enabled).To(BeTrue())
				Expect(appCfg.Features[0].Tags).To(ConsistOf("prod", "stable"))
				Expect(appCfg.Features[1].Name).To(Equal("feature2"))
				Expect(appCfg.Features[1].Enabled).To(BeFalse())
			})
		})

		Context("with optional/pointer fields", func() {
			type OptionalConfig struct {
				Required string  `yaml:"required"`
				Optional *string `yaml:"optional"`
				Missing  *string `yaml:"missing"`
			}

			BeforeEach(func() {
				yamlData := `
config:
  required: must-have
  optional: has-value
`
				Expect(yaml.Unmarshal([]byte(yamlData), &cfg)).To(Succeed())
			})

			It("should handle optional fields", func() {
				var optCfg OptionalConfig
				Expect(cfg.GetInto("config", &optCfg)).To(Succeed())
				Expect(optCfg.Required).To(Equal("must-have"))
				Expect(optCfg.Optional).ToNot(BeNil())
				Expect(*optCfg.Optional).To(Equal("has-value"))
				Expect(optCfg.Missing).To(BeNil())
			})
		})
	})
})
