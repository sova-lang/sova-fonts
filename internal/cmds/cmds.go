package cmds

import "github.com/spf13/cobra"

// NewRootCmd builds the top-level `sova-fonts` command. Commands here:
//   - generate: read sova-fonts.toml, download fonts, write the gen file.
//
// Future: `add <family>`, `remove <family>`, `list`, `update` (re-fetch latest versions). All wrappers around `generate` with TOML-editing convenience.
func NewRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "sova-fonts",
		Short:         "Generate Sova font bindings from Google Fonts (and friends)",
		SilenceUsage:  true,
		SilenceErrors: false,
	}
	cmd.AddCommand(newGenerateCmd())
	cmd.AddCommand(newUpdateCmd())
	cmd.AddCommand(newAddCmd())
	cmd.AddCommand(newRemoveCmd())
	return cmd
}
