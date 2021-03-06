package cmd

import (
	"errors"

	"github.com/ory/viper"
	"github.com/spf13/cobra"

	"github.com/lkingland/faas/appsody"
	"github.com/lkingland/faas/client"
	"github.com/lkingland/faas/docker"
	"github.com/lkingland/faas/kn"
)

func init() {
	root.AddCommand(updateCmd)
}

var updateCmd = &cobra.Command{
	Use:        "update",
	Short:      "Update or create a deployed Service Function",
	Long:       `Update deployed function to match the current local state.`,
	SuggestFor: []string{"push", "deploy"},
	RunE:       update,
}

func update(cmd *cobra.Command, args []string) (err error) {
	var (
		verbose   = viper.GetBool("verbose")     // Verbose logging
		registry  = viper.GetString("registry")  // Registry (ex: docker.io)
		namespace = viper.GetString("namespace") // namespace at registry (user or org name)
	)

	// Namespace can not be defaulted.
	if namespace == "" {
		return errors.New("image registry namespace (--namespace or FAAS_NAMESPACE is required)")
	}

	// Builder creates images from function source.
	builder := appsody.NewBuilder(registry, namespace)
	builder.Verbose = verbose

	// Pusher of images
	pusher := docker.NewPusher()
	pusher.Verbose = verbose

	// Deployer of built images.
	updater := kn.NewUpdater()
	updater.Verbose = verbose

	client, err := client.New(
		client.WithVerbose(verbose),
		client.WithBuilder(builder),
		client.WithPusher(pusher),
		client.WithUpdater(updater),
	)
	if err != nil {
		return
	}

	return client.Update()
}
