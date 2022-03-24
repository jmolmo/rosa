/*
Copyright (c) 2020 Red Hat, Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

  http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package service

import (
	"fmt"
	"os"
	"text/tabwriter"

	msv1 "github.com/openshift-online/ocm-sdk-go/servicemgmt/v1"
	"github.com/openshift/rosa/pkg/logging"
	"github.com/openshift/rosa/pkg/ocm"
	"github.com/openshift/rosa/pkg/output"
	rprtr "github.com/openshift/rosa/pkg/reporter"
	"github.com/spf13/cobra"
)

var Cmd = &cobra.Command{
	Use:     "services",
	Aliases: []string{"services"},
	Short:   "List managed services",
	Long:    "List managed services.",
	Example: `  # List all managed services
  rosa list services`,
	Args: cobra.NoArgs,
	Run:  run,
}

func init() {
	flags := Cmd.Flags()
	flags.SortFlags = false

	output.AddFlag(Cmd)
}

func run(cmd *cobra.Command, argv []string) {
	reporter := rprtr.CreateReporterOrExit()
	logger := logging.CreateLoggerOrExit(reporter)

	// Parse out CLI flags, then override positional arguments
	// This allows for arbitrary flags used for addon parameters
	_ = cmd.Flags().Parse(argv)

	// Create the client for the OCM API:
	ocmClient, err := ocm.NewClient().
		Logger(logger).
		Build()
	if err != nil {
		reporter.Errorf("Failed to create OCM connection: %v", err)
		os.Exit(1)
	}
	defer func() {
		err = ocmClient.Close()
		if err != nil {
			reporter.Errorf("Failed to close OCM connection: %v", err)
		}
	}()

	servicesList, err := ocmClient.ListManagedServices(1000)
	if err != nil {
		reporter.Errorf("Failed to retrieve list of managed services: %v", err)
		os.Exit(1)
	}

	writer := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(writer, "ID\tSERVICE\tSTATE\n")
	servicesList.Each(func(srv *msv1.ManagedService) bool {
		fmt.Fprintf(writer,"%s\t%s\t%s\n",srv.ID(),srv.Service(),srv.ServiceState())
		return true
	})
	writer.Flush()
}