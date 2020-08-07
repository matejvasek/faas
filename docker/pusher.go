package docker

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
)

// Pusher of images from local to remote registry.
type Pusher struct {
	// Verbose logging.
	Verbose bool
	Tag     string
}

// NewPusher creates an instance of a docker-based image pusher.
func NewPusher(tag string) *Pusher {
	return &Pusher{Tag: tag}
}

// Push an image by name.  Docker is expected to be already authenticated.
func (n *Pusher) Push() (err error) {
	// Check for the docker binary explicitly so that we can return
	// an extra-friendly error message.
	_, err = exec.LookPath("docker")
	if err != nil {
		err = errors.New("please install 'docker'")
		return
	}

	// set up the command, specifying a sanitized project name and connecting
	// standard output and error.
	cmd := exec.Command("docker", "push", n.Tag)

	// If verbose logging is enabled, echo appsody's chatty stdout.
	if n.Verbose {
		fmt.Println(cmd)
		cmd.Stdout = os.Stdout
	}

	// Capture stderr for echoing on failure.
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	// Run the command, echoing captured stderr as well ass the cmd internal error.
	err = cmd.Run()
	if err != nil {
		// TODO: sanitize stderr from appsody, or submit a PR to remove duplicates etc.
		err = fmt.Errorf("%v. %v", stderr.String(), err.Error())
	}
	return
}
