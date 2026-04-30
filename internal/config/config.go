package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server  ServerConfig  `yaml:"server"`
	Auth    AuthConfig    `yaml:"auth"`
	SSH     SSHConfig     `yaml:"ssh"`
	Bastion BastionConfig `yaml:"bastion"`
	Targets []Target      `yaml:"targets"`
}

type ServerConfig struct {
	Listen string     `yaml:"listen"`
	TLS    *TLSConfig `yaml:"tls,omitempty"`
}

type TLSConfig struct {
	Cert string `yaml:"cert"`
	Key  string `yaml:"key"`
}

type AuthConfig struct {
	Users   []User        `yaml:"users"`
	Session SessionConfig `yaml:"session"`
}

type User struct {
	Username     string `yaml:"username"`
	PasswordHash string `yaml:"password_hash"`
}

type SessionConfig struct {
	Secret string `yaml:"secret"`
	MaxAge int    `yaml:"max_age"`
}

type SSHConfig struct {
	UseAgent   bool   `yaml:"use_agent"`
	KnownHosts string `yaml:"known_hosts"`
}

type BastionConfig struct {
	Host        string `yaml:"host"`
	User        string `yaml:"user"`
	PrivateKey  string `yaml:"private_key"`
	Passphrase  string `yaml:"passphrase,omitempty"`
	KnownHosts  string `yaml:"known_hosts"`
	Keepalive   int    `yaml:"keepalive"`
	MaxSessions int    `yaml:"max_sessions"`
}

type Target struct {
	Name               string          `yaml:"name"`
	Host               string          `yaml:"host"`
	User               string          `yaml:"user"`
	PrivateKey         string          `yaml:"private_key"`
	Passphrase         string          `yaml:"passphrase,omitempty"`
	HostKeyFingerprint string          `yaml:"host_key_fingerprint,omitempty"`
	Terminal           *TerminalConfig `yaml:"terminal,omitempty"`
	Desktop            *DesktopConfig  `yaml:"desktop,omitempty"`
}

type TerminalConfig struct {
	Enabled        bool   `yaml:"enabled"`
	DefaultSession string `yaml:"default_session"`
}

type DesktopConfig struct {
	Enabled bool `yaml:"enabled"`
	VNCPort int  `yaml:"vnc_port"`
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config file: %w", err)
	}

	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	cfg.setDefaults()
	return &cfg, nil
}

func (c *Config) validate() error {
	if c.Server.Listen == "" {
		return fmt.Errorf("server.listen is required")
	}
	if len(c.Auth.Users) == 0 {
		return fmt.Errorf("at least one auth.users entry is required")
	}
	if c.Auth.Session.Secret == "" {
		return fmt.Errorf("auth.session.secret is required")
	}
	for i, u := range c.Auth.Users {
		if u.Username == "" {
			return fmt.Errorf("auth.users[%d].username is required", i)
		}
		if u.PasswordHash == "" {
			return fmt.Errorf("auth.users[%d].password_hash is required", i)
		}
	}
	if c.Bastion.Host == "" {
		return fmt.Errorf("bastion.host is required")
	}
	if c.Bastion.User == "" {
		return fmt.Errorf("bastion.user is required")
	}
	if c.Bastion.PrivateKey == "" && !c.SSH.UseAgent {
		return fmt.Errorf("bastion.private_key is required when ssh.use_agent is false")
	}
	if c.Bastion.KnownHosts == "" && c.SSH.KnownHosts == "" {
		return fmt.Errorf("bastion.known_hosts or ssh.known_hosts is required")
	}
	for i, t := range c.Targets {
		if t.Name == "" {
			return fmt.Errorf("targets[%d].name is required", i)
		}
		if t.Host == "" {
			return fmt.Errorf("targets[%d].host is required", i)
		}
		if t.User == "" {
			return fmt.Errorf("targets[%d].user is required", i)
		}
		if t.PrivateKey == "" && !c.SSH.UseAgent {
			return fmt.Errorf("targets[%d].private_key is required when ssh.use_agent is false", i)
		}
		if t.Desktop != nil && t.Desktop.Enabled && t.Desktop.VNCPort == 0 {
			return fmt.Errorf("targets[%d].desktop.vnc_port is required when desktop is enabled", i)
		}
	}
	return nil
}

func (c *Config) setDefaults() {
	if c.Auth.Session.MaxAge == 0 {
		c.Auth.Session.MaxAge = 86400
	}
	if c.Bastion.Keepalive == 0 {
		c.Bastion.Keepalive = 30
	}
	if c.Bastion.MaxSessions == 0 {
		c.Bastion.MaxSessions = 8
	}
	if c.Bastion.KnownHosts == "" {
		c.Bastion.KnownHosts = c.SSH.KnownHosts
	}
	for i := range c.Targets {
		if c.Targets[i].Terminal != nil && c.Targets[i].Terminal.DefaultSession == "" {
			c.Targets[i].Terminal.DefaultSession = "main"
		}
	}
}

func (c *Config) TargetByName(name string) (*Target, bool) {
	for i := range c.Targets {
		if c.Targets[i].Name == name {
			return &c.Targets[i], true
		}
	}
	return nil, false
}
