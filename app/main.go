package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/ninaiad/fq-connector-go/app/bench"
	"github.com/ninaiad/fq-connector-go/app/client"
	"github.com/ninaiad/fq-connector-go/app/server"
	"github.com/ninaiad/fq-connector-go/app/validate"
	"github.com/ninaiad/fq-connector-go/app/version"
)

var rootCmd = &cobra.Command{
	Use:   "connector",
	Short: "Connector for external data sources",
}

func init() {
	rootCmd.AddCommand(bench.Cmd)
	rootCmd.AddCommand(client.Cmd)
	rootCmd.AddCommand(server.Cmd)
	rootCmd.AddCommand(validate.Cmd)
	rootCmd.AddCommand(version.Cmd)
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}
