/*
Copyright (c) 2022 Red Hat, Inc.

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
	"strings"

	"github.com/aws/aws-sdk-go/aws/arn"
	"github.com/spf13/cobra"

	cmv1 "github.com/openshift-online/ocm-sdk-go/clustersmgmt/v1"
	"github.com/openshift/rosa/pkg/aws"
	"github.com/openshift/rosa/pkg/interactive"
	"github.com/openshift/rosa/pkg/logging"
	"github.com/openshift/rosa/pkg/ocm"
	"github.com/openshift/rosa/pkg/output"
	rprtr "github.com/openshift/rosa/pkg/reporter"
)

var args ocm.CreateManagedServiceArgs

var Cmd = &cobra.Command{
	Use:   "service",
	Short: "Creates a managed service.",
	Long: `  Managed Services are Openshift clusters that provide a specific function.
  Use this command to create managed services.`,
	Example: `  # Create a Managed Service using service1.
  rosa create service --service=service1 --clusterName=clusterName`,
	Run: run,
}

func init() {
	flags := Cmd.Flags()
	flags.SortFlags = false

	// Basic options
	flags.StringVar(
		&args.ServiceName,
		"service",
		"",
		"Name of the service.",
	)

	flags.StringVar(
		&args.ClusterName,
		"clusterName",
		"",
		"Name of the cluster.",
	)
}

func run(cmd *cobra.Command, _ []string) {
	reporter := rprtr.CreateReporterOrExit()
	logger := logging.CreateLoggerOrExit(reporter)

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

	awsClient := aws.GetAWSClientForUserRegion(reporter, logger)

	// Openshift version to use.
	// Hard-coding 4.9 for now
	version := "4.9"
	minor := ocm.GetVersionMinor(version)
	role := aws.AccountRoles[aws.InstallerAccountRole]

	// Find all installer roles in the current account using AWS resource tags
	var roleARN string
	var supportRoleARN string
	var controlPlaneRoleARN string
	var workerRoleARN string
	var hasRoles bool

	roleARNs, err := awsClient.FindRoleARNs(aws.InstallerAccountRole, minor)
	if err != nil {
		reporter.Errorf("Failed to find %s role: %s", role.Name, err)
		os.Exit(1)
	}

	if len(roleARNs) > 1 {
		defaultRoleARN := roleARNs[0]
		// Prioritize roles with the default prefix
		for _, rARN := range roleARNs {
			if strings.Contains(rARN, fmt.Sprintf("%s-%s-Role", aws.DefaultPrefix, role.Name)) {
				defaultRoleARN = rARN
			}
		}
		reporter.Warnf("More than one %s role found, going with %s", role.Name, defaultRoleARN)
		roleARN = defaultRoleARN
	} else if len(roleARNs) == 1 {
		if !output.HasFlag() || reporter.IsTerminal() {
			reporter.Infof("Using %s for the %s role", roleARNs[0], role.Name)
		}
		roleARN = roleARNs[0]
	} else {
		reporter.Errorf("No account roles found. " +
			"You will need to run 'rosa create account-roles' to create them first.")
	}

	if roleARN != "" {
		// Get role prefix
		rolePrefix, err := getAccountRolePrefix(roleARN, role)
		if err != nil {
			reporter.Errorf("Failed to find prefix from %s account role", role.Name)
			os.Exit(1)
		}
		reporter.Debugf("Using '%s' as the role prefix", rolePrefix)

		hasRoles = true
		for roleType, role := range aws.AccountRoles {
			if roleType == aws.InstallerAccountRole {
				// Already dealt with
				continue
			}
			roleARNs, err := awsClient.FindRoleARNs(roleType, minor)
			if err != nil {
				reporter.Errorf("Failed to find %s role: %s", role.Name, err)
				os.Exit(1)
			}
			selectedARN := ""
			for _, rARN := range roleARNs {
				if strings.Contains(rARN, fmt.Sprintf("%s-%s-Role", rolePrefix, role.Name)) {
					selectedARN = rARN
				}
			}
			if selectedARN == "" {
				reporter.Errorf("No %s account roles found. "+
					"You will need to run 'rosa create account-roles' to create them first.",
					role.Name)
				interactive.Enable()
				hasRoles = false
			}
			if !output.HasFlag() || reporter.IsTerminal() {
				reporter.Infof("Using %s for the %s role", selectedARN, role.Name)
			}
			switch roleType {
			case aws.InstallerAccountRole:
				roleARN = selectedARN
			case aws.SupportAccountRole:
				supportRoleARN = selectedARN
			case aws.ControlPlaneAccountRole:
				controlPlaneRoleARN = selectedARN
			case aws.WorkerAccountRole:
				workerRoleARN = selectedARN
			}
		}
	}
	if hasRoles == false {
		reporter.Errorf("Please create the above roles to continue")
		os.Exit(1)
	}

	args.AwsRoleARN = roleARN
	args.AwsSupportRoleARN = supportRoleARN
	args.AwsControlPlaneRoleARN = controlPlaneRoleARN
	args.AwsWorkerRoleARN = workerRoleARN

	// operator role logic.
	awsCreator, err := awsClient.GetCreator()
	if err != nil {
		reporter.Errorf("Unable to get IAM credentials: %v", err)
		os.Exit(1)
	}

	operatorRolesPrefix := getRolePrefix(args.ClusterName)
	operatorIAMRoleList := []ocm.OperatorIAMRole{}

	for _, operator := range aws.CredentialRequests {
		//If the cluster version is less than the supported operator version
		if operator.MinVersion != "" {
			isSupported, err := ocm.CheckSupportedVersion(ocm.GetVersionMinor(version), operator.MinVersion)
			if err != nil {
				reporter.Errorf("Error validating operator role '%s' version %s", operator.Name, err)
				os.Exit(1)
			}
			if !isSupported {
				continue
			}
		}
		operatorIAMRoleList = append(operatorIAMRoleList, ocm.OperatorIAMRole{
			Name:      operator.Name,
			Namespace: operator.Namespace,
			RoleARN:   getOperatorRoleArn(operatorRolesPrefix, operator, awsCreator),
		})
	}

	// Validate the role names are available on AWS
	for _, role := range operatorIAMRoleList {
		name := strings.SplitN(role.RoleARN, "/", 2)[1]
		err := awsClient.ValidateRoleNameAvailable(name)
		if err != nil {
			reporter.Errorf("Error validating role: %v", err)
			os.Exit(1)
		}
	}

	args.AwsOperatorIamRoleList = operatorIAMRoleList
	// end operator role logic.

	/*
		awsCreator, err := awsClient.GetCreator()
		if err != nil {
			reporter.Errorf("Unable to get IAM credentials: %v", err)
			os.Exit(1)
		}

		accessKey, err := awsClient.GetAWSAccessKeys()
		if err != nil {
			reporter.Errorf("Unable to get access keys: %v", err)
			os.Exit(1)
		}
		args.AwsAccountID = awsCreator.AccountID
		args.AwsAccessKeyID = accessKey.AccessKeyID
		args.AwsSecretAccessKey = accessKey.SecretAccessKey
	*/

	args.AwsAccountID = awsCreator.AccountID

	// Get AWS region
	args.AwsRegion, err = aws.GetRegion("")
	if err != nil {
		reporter.Errorf("Error getting region: %v", err)
		os.Exit(1)
	}
	reporter.Infof("Using AWS region: %s", args.AwsRegion)

	// Parameter logic
	addOn, err := ocmClient.GetAddOn(args.ServiceName)
	if err != nil {
		reporter.Errorf("Failed to process service parameters: %s", err)
	}
	addOnParameters := addOn.Parameters()
	if addOnParameters != nil {
		addOnParameters.Each(func(param *cmv1.AddOnParameter) bool {
			flag := cmd.Flags().Lookup(param.ID())
			if flag != nil {
				args.Parameters[param.ID()] = flag.Value.String()
			}
			return true
		})
	}

	// Creating the service
	service, err := ocmClient.CreateManagedService(args)
	if err != nil {
		reporter.Errorf("Failed to create managed service: %s", err)
		os.Exit(1)
	}

	reporter.Infof("%v", service)

	// The client must run these rosa commands after this for the cluster to properly install.
	rolesCMD := fmt.Sprintf("rosa create operator-roles --cluster %s", args.ClusterName)
	oidcCMD := fmt.Sprintf("rosa create oidc-provider --cluster %s", args.ClusterName)

	reporter.Infof("Run the following commands to continue the cluster creation:\n\n"+
		"\t%s\n"+
		"\t%s\n",
		rolesCMD, oidcCMD)
}

func getAccountRolePrefix(roleARN string, role aws.AccountRole) (string, error) {
	parsedARN, err := arn.Parse(roleARN)
	if err != nil {
		return "", err
	}
	roleName := strings.SplitN(parsedARN.Resource, "/", 2)[1]
	rolePrefix := strings.TrimSuffix(roleName, fmt.Sprintf("-%s-Role", role.Name))
	return rolePrefix, nil
}

func getRolePrefix(clusterName string) string {
	return fmt.Sprintf("%s-%s", clusterName, ocm.RandomLabel(4))
}

func getOperatorRoleArn(prefix string, operator aws.Operator, creator *aws.Creator) string {
	role := fmt.Sprintf("%s-%s-%s", prefix, operator.Namespace, operator.Name)
	if len(role) > 64 {
		role = role[0:64]
	}
	return fmt.Sprintf("arn:aws:iam::%s:role/%s", creator.AccountID, role)
}