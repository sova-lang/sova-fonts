package cmds

import (
	"github.com/spf13/cobra"
)

// newUpdateCmd registers `sova-fonts update [family]...`. With no arguments, refetches every font from its upstream source and rewrites the lockfile. With one or more family names, refetches only those families (others keep their locked state). Useful when Google bumps a font version and you want to pull the latest WOFF2 bytes without changing your manifest.
func newUpdateCmd() *cobra.Command {
	var manifestPath string
	cmd := &cobra.Command{
		Use:   "update [family]...",
		Short: "Refetch fonts from their upstream sources and refresh the lockfile",
		RunE: func(cmd *cobra.Command, args []string) error {
			opts := generateOptions{}
			if len(args) == 0 {
				opts.ignoreLock = true
			} else {
				opts.onlyFamilies = map[string]bool{}
				for _, a := range args {
					opts.onlyFamilies[a] = true
				}
			}
			_, err := runGenerate(manifestPath, opts)
			return err
		},
	}
	cmd.Flags().StringVar(&manifestPath, "config", "", "path to sova-fonts.toml (default: ./sova-fonts.toml)")
	return cmd
}
