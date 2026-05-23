package cmd

import (
	"fmt"
	"runtime"

	"github.com/spf13/cobra"
)

// Set at build time via -ldflags. See the Makefile.
var (
	Version   = "0.2.0-rc2"
	Commit    = "unknown"
	BuildDate = "unknown"
)

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Show chepherd version + build info",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("chepherd %s\n", Version)
		fmt.Printf("  commit:     %s\n", Commit)
		fmt.Printf("  built:      %s\n", BuildDate)
		fmt.Printf("  go:         %s\n", runtime.Version())
		fmt.Printf("  os/arch:    %s/%s\n", runtime.GOOS, runtime.GOARCH)
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
}
