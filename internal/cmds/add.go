package cmds

import (
	"fmt"

	"github.com/sova-lang/sova-fonts/internal/config"
	"github.com/spf13/cobra"
)

// newAddCmd registers `sova-fonts add <family>`. Adds (or replaces) an entry in sova-fonts.toml without running generate — letting the user batch multiple add calls before paying the network cost once. Most flags map 1:1 to manifest fields; --generate piggybacks `sova-fonts generate` so the common single-add-then-regenerate workflow is one command.
func newAddCmd() *cobra.Command {
	var (
		manifestPath string
		weights      string
		weightRange  string
		italic       bool
		display      string
		source       string
		alsoGenerate bool
	)
	cmd := &cobra.Command{
		Use:   "add <family>",
		Short: "Add (or replace) a font entry in sova-fonts.toml",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			family := args[0]
			m, manifestAbs, exists, err := loadOrInit(manifestPath)
			if err != nil {
				return err
			}
			if !exists && m.Output.SovaPackage == "" {
				return fmt.Errorf("%s does not exist — create one with the [output] block first (sova_package is required)", manifestAbs)
			}
			spec := config.FontSpec{Family: family, Italic: italic, Display: display, Source: source}
			if weightRange != "" {
				lo, hi, err := parseWeightRange(weightRange)
				if err != nil {
					return err
				}
				spec.WeightRange = []int{lo, hi}
			} else if weights != "" {
				ws, err := parseWeightList(weights)
				if err != nil {
					return err
				}
				spec.Weights = ws
			}
			inserted := upsertFont(&m, spec)
			if err := saveManifest(manifestAbs, m); err != nil {
				return err
			}
			if inserted {
				fmt.Printf("added %s to %s\n", family, manifestAbs)
			} else {
				fmt.Printf("replaced %s in %s\n", family, manifestAbs)
			}
			if alsoGenerate {
				_, err := runGenerate(manifestPath, generateOptions{onlyFamilies: map[string]bool{family: true}})
				return err
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&manifestPath, "config", "", "path to sova-fonts.toml (default: ./sova-fonts.toml)")
	cmd.Flags().StringVarP(&weights, "weights", "w", "400", "comma-separated weight list (e.g. \"400,700\")")
	cmd.Flags().StringVar(&weightRange, "weight-range", "", "variable-font weight range MIN..MAX (e.g. \"100..900\"); google source only")
	cmd.Flags().BoolVar(&italic, "italic", false, "also fetch italic faces for every weight")
	cmd.Flags().StringVar(&display, "display", "swap", "CSS font-display value")
	cmd.Flags().StringVar(&source, "source", "google", "font source (google | bunny | local)")
	cmd.Flags().BoolVar(&alsoGenerate, "generate", false, "run `sova-fonts generate` after the add")
	return cmd
}
