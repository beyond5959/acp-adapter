package pi

import (
	"io"
	"os"
	"strings"
)

const (
	// DefaultBin is the default pi executable path.
	DefaultBin = "pi"
)

// Config configures the Pi RPC-backed adapter.
type Config struct {
	PiBin           string
	ExtraArgs       []string
	DefaultProvider string
	DefaultModel    string
	SessionDir      string
	WorkDir         string
	EnableGate      bool
	Trace           func(direction string, payload []byte)
	Stderr          io.Writer
}

// Normalize applies defaults.
func (c Config) Normalize() Config {
	if strings.TrimSpace(c.PiBin) == "" {
		c.PiBin = DefaultBin
	}
	if c.Stderr == nil {
		c.Stderr = os.Stderr
	}
	return c
}
