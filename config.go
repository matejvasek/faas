package faas

import (
	"io/ioutil"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v2"
)

// ConfigFileName is an optional file checked for in the function root.
const ConfigFileName = ".faas.yaml"

// Config object which provides another mechanism for overriding client static
// defaults.  Applied prior to the WithX options, such that the options take
// precedence if they are provided.
type Config struct {
	// Name specifies the name to be used for this function. As a config option,
	// this value, if provided, takes precidence over the path-derived name but
	// not over the Option WithName, if provided.
	Name string `yaml:"name"`

	// Runtime of the implementation.
	Runtime string `yaml:"runtime"`

	// OCI image tag for the function
	// typically of the form "registry/namespace/repository:tag"
	Tag string `yaml:"tag"`

	// Add new values to the applyConfig function as necessary.
}

// newConfig creates a config object from a function, effectively exporting mutable
// fields for the config file while preserving the immutability of the client
// post-instantiation.
func newConfig(f *Function) Config {
	return Config{
		Name:    f.Name,
		Runtime: f.Runtime,
		Tag:     f.Tag,
	}
}

// writeConfig out to disk.
func writeConfig(f *Function) (err error) {
	var (
		cfg     = newConfig(f)
		cfgFile = filepath.Join(f.Root, ConfigFileName)
		bb      []byte
	)
	if bb, err = yaml.Marshal(&cfg); err != nil {
		return
	}
	return ioutil.WriteFile(cfgFile, bb, 0644)
}

// Apply the config, if it exists, to the function struct.
// if an entry exists in the config file and is empty, this is interpreted as
// the intent to zero-value that field.
func applyConfig(f *Function, root string) error {
	// abort if the config file does not exist.
	filename := filepath.Join(root, ConfigFileName)
	if _, err := os.Stat(filename); os.IsNotExist(err) {
		return nil
	}

	// Read in as bytes
	bb, err := ioutil.ReadFile(filepath.Join(root, ConfigFileName))
	if err != nil {
		return err
	}

	// Create a config with defaults set to the current value of the Client object.
	// These gymnastics are necessary because we want the Client's members to be
	// private to disallow mutation post instantiation, and thus they are unavailable
	// to be set automatically
	cfg := newConfig(f)

	// Decode yaml, overriding values in the config if they were defined in the yaml.
	if err := yaml.Unmarshal(bb, &cfg); err != nil {
		return err
	}

	// Apply the config to the client object, which effectiely writes back the default
	// if it was not defined in the yaml.
	f.Name = cfg.Name
	f.Runtime = cfg.Runtime
	f.Tag = cfg.Tag

	// NOTE: cleverness < clarity

	return nil
}
