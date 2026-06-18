package cmds

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/sova-lang/sova-fonts/internal/lock"
	"github.com/spf13/cobra"
)

// newRemoveCmd registers `sova-fonts remove <family>`. Drops the matching entry from sova-fonts.toml and (when --prune-files is set) deletes the corresponding lock entry plus the staged WOFF2 files from `output.fonts_dir`. The default is to leave staged files alone — they're harmless and removing them eagerly would break a still-deployed build that references the old hashes.
func newRemoveCmd() *cobra.Command {
	var (
		manifestPath string
		source       string
		pruneFiles   bool
	)
	cmd := &cobra.Command{
		Use:   "remove <family>",
		Short: "Remove a font entry from sova-fonts.toml (use --prune-files to also delete staged WOFF2 files)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			family := args[0]
			m, manifestAbs, exists, err := loadOrInit(manifestPath)
			if err != nil {
				return err
			}
			if !exists {
				return fmt.Errorf("%s does not exist — nothing to remove", manifestAbs)
			}
			if ok := removeFont(&m, family, source); !ok {
				return fmt.Errorf("no [[font]] entry matches family=%q source=%q", family, source)
			}
			if err := saveManifest(manifestAbs, m); err != nil {
				return err
			}
			fmt.Printf("removed %s from %s\n", family, manifestAbs)

			projectRoot := filepath.Dir(manifestAbs)
			lockPath := filepath.Join(projectRoot, lock.LockFilename)
			if existingLock, ok, _ := lock.Load(lockPath); ok {
				removedSource := source
				if removedSource == "" {
					removedSource = "google"
				}
				if pruneFiles {
					if lf := existingLock.Find(family, removedSource); lf != nil {
						fontsDir := filepath.Join(projectRoot, m.Output.FontsDir)
						for _, face := range lf.Faces {
							_ = os.Remove(filepath.Join(fontsDir, face.LocalName))
						}
						fmt.Printf("pruned %d staged file(s) from %s\n", len(lf.Faces), m.Output.FontsDir)
					}
				}
				if existingLock.Remove(family, removedSource) {
					if err := lock.Save(lockPath, existingLock); err != nil {
						return err
					}
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&manifestPath, "config", "", "path to sova-fonts.toml (default: ./sova-fonts.toml)")
	cmd.Flags().StringVar(&source, "source", "", "narrow the match to a specific source (google | bunny | local)")
	cmd.Flags().BoolVar(&pruneFiles, "prune-files", false, "also delete staged WOFF2 files for the removed font")
	return cmd
}
