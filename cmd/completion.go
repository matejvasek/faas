package cmd

import (
	"errors"
	"os"

	"github.com/spf13/cobra"
)

func init() {
	root.AddCommand(completionCmd)
}

// completionCmd represents the completion command
var completionCmd = &cobra.Command{
	Use:   "completion",
	Short: "Generates bash/zsh completion scripts",
	Long: `To load completion run

For zsh:
source <(faas completion zsh)

If you would like to use alias:
alias f=faas
compdef _faas f

For bash:
source <(faas completion bash)

`,
	ValidArgs: []string{"bash", "zsh"},
	Args:      cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) (err error) {
		if len(args) < 1 {
			return errors.New("missing argument")
		}
		if args[0] == "bash" {
			err = root.GenBashCompletion(os.Stdout)
			return err
		}
		if args[0] == "zsh" {
			// manually edited script based on `root.GenZshCompletion(os.Stdout)`
			// unfortunately it doesn't support completion so well as for bash
			// some manual edits had to be done
			return root.GenZshCompletion(os.Stdout)
		}
		return errors.New("unknown shell, only bash and zsh are supported")
	},
}
