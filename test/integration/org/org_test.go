// Copyright 2022 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package org

import (
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/cloud-foundation-toolkit/infra/blueprint-test/pkg/gcloud"
	"github.com/GoogleCloudPlatform/cloud-foundation-toolkit/infra/blueprint-test/pkg/tft"
	"github.com/GoogleCloudPlatform/cloud-foundation-toolkit/infra/blueprint-test/pkg/utils"
	"github.com/gruntwork-io/terratest/modules/terraform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/terraform-google-modules/terraform-example-foundation/test/integration/testutils"
)

func TestOrg(t *testing.T) {

	bootstrap := tft.NewTFBlueprintTest(t,
		tft.WithTFDir("../../../0-bootstrap"),
	)

	backend_bucket := bootstrap.GetStringOutput("gcs_bucket_tfstate")

	vars := map[string]interface{}{
		"remote_state_bucket":              backend_bucket,
		"log_export_storage_force_destroy": "true",
		"cai_monitoring_kms_force_destroy": "true",
	}

	backendConfig := map[string]interface{}{
		"bucket": backend_bucket,
	}

	// Configure impersonation for test execution
	terraformSA := bootstrap.GetStringOutput("organization_step_terraform_service_account_email")
	utils.SetEnv(t, "GOOGLE_IMPERSONATE_SERVICE_ACCOUNT", terraformSA)

	org := tft.NewTFBlueprintTest(t,
		tft.WithTFDir("../../../1-org/envs/shared"),
		tft.WithVars(vars),
		tft.WithRetryableTerraformErrors(testutils.RetryableTransientErrors, 1, 2*time.Minute),
		tft.WithPolicyLibraryPath("/workspace/policy-library", bootstrap.GetTFSetupStringOutput("project_id")),
		tft.WithBackendConfig(backendConfig),
	)

	org.DefineApply(
		func(assert *assert.Assertions) {
			projectID := bootstrap.GetStringOutput("seed_project_id")
			for _, api := range []string{
				"serviceusage.googleapis.com",
				"servicenetworking.googleapis.com",
				"cloudkms.googleapis.com",
				"compute.googleapis.com",
				"logging.googleapis.com",
				"bigquery.googleapis.com",
				"cloudresourcemanager.googleapis.com",
				"cloudbilling.googleapis.com",
				"cloudbuild.googleapis.com",
				"iam.googleapis.com",
				"admin.googleapis.com",
				"appengine.googleapis.com",
				"storage-api.googleapis.com",
				"monitoring.googleapis.com",
				"pubsub.googleapis.com",
				"securitycenter.googleapis.com",
				"accesscontextmanager.googleapis.com",
				"billingbudgets.googleapis.com",
				"essentialcontacts.googleapis.com",
			} {
				utils.Poll(t, func() (bool, error) { return testutils.CheckAPIEnabled(t, projectID, api) }, 5, 2*time.Minute)
			}

			org.DefaultApply(assert)
		})
	org.DefineVerify(
		func(assert *assert.Assertions) {
			// perform default verification ensuring Terraform reports no additional changes on an applied blueprint
			org.DefaultVerify(assert)

			parentFolder := testutils.GetLastSplitElement(org.GetStringOutput("parent_resource_id"), "/")

			// creation of common folder
			commonFolder := testutils.GetLastSplitElement(org.GetStringOutput("common_folder_name"), "/")
			folder := gcloud.Runf(t, "resource-manager folders describe %s", commonFolder)
			assert.Equal("fldr-common", folder.Get("displayName").String(), "folder fldr-common should have been created")

			// creation of network folder
			networkFolder := testutils.GetLastSplitElement(org.GetStringOutput("network_folder_name"), "/")
			folderOP := gcloud.Runf(t, "resource-manager folders describe %s", networkFolder)
			assert.Equal("fldr-network", folderOP.Get("displayName").String(), "folder fldr-network should have been created")

			// check tags applied to common and bootstrap folder
			commonConfig := terraform.OutputMap(t, bootstrap.GetTFOptions(), "common_config")
			bootstrapFolder := testutils.GetLastSplitElement(commonConfig["bootstrap_folder_name"], "/")

			for _, tags := range []struct {
				folderId   string
				folderName string
				value      string
			}{
				{
					folderId:   commonFolder,
					folderName: "common",
					value:      "production",
				},
				{
					folderId:   networkFolder,
					folderName: "network",
					value:      "production",
				},
				{
					folderId:   bootstrapFolder,
					folderName: "bootstrap",
					value:      "bootstrap",
				},
			} {
				fldrTags := gcloud.Runf(t, "resource-manager tags bindings list --parent=//cloudresourcemanager.googleapis.com/folders/%s", tags.folderId).Array()
				fldrsTagValuesId := testutils.GetResultFieldStrSlice(fldrTags, "tagValue")

				var fldrsTagValues []string
				for _, tagValueId := range fldrsTagValuesId {
					tagValueObj := gcloud.Runf(t, "resource-manager tags values describe %s", tagValueId)
					fldrsTagValues = append(fldrsTagValues, tagValueObj.Get("shortName").String())
				}

				assert.Contains(fldrsTagValues, tags.value, fmt.Sprintf("folder %s (%s) should have tag %s", tags.folderName, tags.folderId, tags.value))
			}

			// boolean organization policies
			for _, booleanConstraint := range []string{
				"constraints/compute.disableNestedVirtualization",
				"constraints/compute.disableSerialPortAccess",
				"constraints/compute.skipDefaultNetworkCreation",
				"constraints/compute.restrictXpnProjectLienRemoval",
				"constraints/sql.restrictPublicIp",
				"constraints/sql.restrictAuthorizedNetworks",
				"constraints/iam.disableServiceAccountKeyCreation",
				"constraints/storage.uniformBucketLevelAccess",
				"constraints/storage.publicAccessPrevention",
				"constraints/iam.automaticIamGrantsForDefaultServiceAccounts",
			} {
				orgPolicy := gcloud.Runf(t, "resource-manager org-policies describe %s --folder %s", booleanConstraint, parentFolder)
				assert.True(orgPolicy.Get("booleanPolicy.enforced").Bool(), fmt.Sprintf("org policy %s should be enforced", booleanConstraint))
			}

			// list policies
			restrictedDomain := gcloud.Runf(t, "resource-manager org-policies describe %s --folder %s", "constraints/iam.allowedPolicyMemberDomains", parentFolder)
			assert.Equal(1, len(restrictedDomain.Get("listPolicy.allowedValues").Array()), "restricted domain org policy should be enforced")

			vmExternalIpAccess := gcloud.Runf(t, "resource-manager org-policies describe %s --folder %s", "constraints/compute.vmExternalIpAccess", parentFolder)
			assert.Equal("DENY", vmExternalIpAccess.Get("listPolicy.allValues").String(), "org policy should deny all external IP access")

			// compute.requireOsLogin is neither a boolean policy nor a list policy
			requireOsLogin := gcloud.Runf(t, "resource-manager org-policies describe %s --folder %s", "constraints/compute.requireOsLogin", parentFolder)
			assert.Equal("constraints/compute.requireOsLogin", requireOsLogin.Get("constraint").String(), "org policy should require OS Login")

			// security command center
			sccProjectID := org.GetStringOutput("scc_notifications_project_id")
			topicName := "top-scc-notification"
			topicFullName := fmt.Sprintf("projects/%s/topics/%s", sccProjectID, topicName)
			topic := gcloud.Runf(t, "pubsub topics describe %s --project %s", topicName, sccProjectID)
			assert.Equal(topicFullName, topic.Get("name").String(), fmt.Sprintf("topic %s should have been created", topicName))

			subscriptionName := "sub-scc-notification"
			subscriptionFullName := fmt.Sprintf("projects/%s/subscriptions/%s", sccProjectID, subscriptionName)
			subscription := gcloud.Runf(t, "pubsub subscriptions describe %s --project %s", subscriptionName, sccProjectID)
			assert.Equal(subscriptionFullName, subscription.Get("name").String(), fmt.Sprintf("subscription %s should have been created", subscriptionName))

			orgID := bootstrap.GetTFSetupStringOutput("org_id")
			notificationName := org.GetStringOutput("scc_notification_name")
			notification := gcloud.Runf(t, "scc notifications describe %s --organization %s", notificationName, orgID)
			assert.Equal(topicFullName, notification.Get("pubsubTopic").String(), fmt.Sprintf("notification %s should use topic %s", notificationName, topicName))

			//essential contacts
			//test case considers that just the Org Admin group exists and will subscribe for all categories
			essentialContacts := gcloud.Runf(t, "essential-contacts list --folder=%s", parentFolder).Array()
			assert.Len(essentialContacts, 1, "only one essential contact email should be created")

			groupOrgAdmins := utils.ValFromEnv(t, "TF_VAR_group_email")
			assert.Equal(groupOrgAdmins, essentialContacts[0].Get("email").String(), "essential contact email should be group org admin")
			assert.Equal("VALID", essentialContacts[0].Get("validationState").String(), "state of essential contact should be valid")

			listCategories := utils.GetResultStrSlice(essentialContacts[0].Get("notificationCategorySubscriptions").Array())
			expectedCategories := []string{"BILLING", "LEGAL", "PRODUCT_UPDATES", "SECURITY", "SUSPENSION", "TECHNICAL"}
			assert.Subset(listCategories, expectedCategories, "notification category subscriptions should be the same")

			//logging
			billingLogsProjectID := org.GetStringOutput("org_billing_logs_project_id")
			billingDatasetName := "billing_data"
			billingDatasetFullName := fmt.Sprintf("%s:%s", billingLogsProjectID, billingDatasetName)
			billingDataset := gcloud.Runf(t, "alpha bq datasets describe %s --project %s", billingDatasetName, billingLogsProjectID)
			assert.Equal(billingDatasetFullName, billingDataset.Get("id").String(), fmt.Sprintf("dataset %s should exist", billingDatasetFullName))

			auditLogsProjectID := org.GetStringOutput("org_audit_logs_project_id")

			logsExportStorageBucketName := org.GetStringOutput("logs_export_storage_bucket_name")
			gcAlphaOpts := gcloud.WithCommonArgs([]string{"--project", auditLogsProjectID, "--json"})
			bkt := gcloud.Run(t, fmt.Sprintf("alpha storage ls --buckets gs://%s", logsExportStorageBucketName), gcAlphaOpts).Array()[0]
			assert.Equal(logsExportStorageBucketName, bkt.Get("metadata.id").String(), fmt.Sprintf("Bucket %s should exist", logsExportStorageBucketName))

			logsExportLogBktName := org.GetStringOutput("logs_export_logbucket_name")
			defaultRegion := commonConfig["default_region"]
			logBktFullName := fmt.Sprintf("projects/%s/locations/%s/buckets/%s", auditLogsProjectID, defaultRegion, logsExportLogBktName)
			logBktDetails := gcloud.Runf(t, fmt.Sprintf("logging buckets describe %s --location=%s --project=%s", logsExportLogBktName, defaultRegion, auditLogsProjectID))
			assert.Equal(logBktFullName, logBktDetails.Get("name").String(), "log bucket name should match")
			linkedDatasetID := "ds_c_logbkt_analytics"
			auditLogsProjectNumber := gcloud.Runf(t, "projects describe %s", auditLogsProjectID).Get("projectNumber").String()
			linkedDsName := org.GetStringOutput("logs_export_logbucket_linked_dataset_name")
			linkedDs := gcloud.Runf(t, "logging links describe %s --bucket=%s --location=%s --project=%s", linkedDatasetID, logsExportLogBktName, defaultRegion, auditLogsProjectID)
			assert.Equal(linkedDsName, linkedDs.Get("name").String(), "log bucket linked dataset name should match")
			bigqueryDatasetID := fmt.Sprintf("bigquery.googleapis.com/projects/%s/datasets/%s", auditLogsProjectNumber, linkedDatasetID)
			assert.Equal(bigqueryDatasetID, linkedDs.Get("bigqueryDataset.datasetId").String(), "log bucket BigQuery dataset ID should match")

			logsExportTopicName := org.GetStringOutput("logs_export_pubsub_topic")
			logsExportTopicFullName := fmt.Sprintf("projects/%s/topics/%s", auditLogsProjectID, logsExportTopicName)
			logsExportTopic := gcloud.Runf(t, "pubsub topics describe %s --project %s", logsExportTopicName, auditLogsProjectID)
			assert.Equal(logsExportTopicFullName, logsExportTopic.Get("name").String(), fmt.Sprintf("topic %s should have been created", logsExportTopicName))

			// logging sinks
			logsFilter := []string{
				"logName: /logs/cloudaudit.googleapis.com%2Factivity",
				"logName: /logs/cloudaudit.googleapis.com%2Fsystem_event",
				"logName: /logs/cloudaudit.googleapis.com%2Fdata_access",
				"logName: /logs/cloudaudit.googleapis.com%2Faccess_transparency",
				"logName: /logs/cloudaudit.googleapis.com%2Fpolicy",
				"logName: /logs/compute.googleapis.com%2Fvpc_flows",
				"logName: /logs/compute.googleapis.com%2Ffirewall",
				"logName: /logs/dns.googleapis.com%2Fdns_queries",
			}

			// CAI Monitoring
			// Variables
			caiAr := org.GetStringOutput("cai_monitoring_artifact_registry")
			caiBucket := org.GetStringOutput("cai_monitoring_bucket")
			caiTopic := org.GetStringOutput("cai_monitoring_topic")

			caiSaEmail := fmt.Sprintf("cai-monitoring@%s.iam.gserviceaccount.com", sccProjectID)
			caiKmsKey := fmt.Sprintf("projects/%s/locations/%s/keyRings/krg-cai-monitoring/cryptoKeys/key-cai-monitoring", sccProjectID, defaultRegion)
			caiTopicFullName := fmt.Sprintf("projects/%s/topics/%s", sccProjectID, caiTopic)

			// Cloud Function
			opCf := gcloud.Runf(t, "functions describe caiMonitoring --project %s --gen2 --region %s", sccProjectID, defaultRegion)
			assert.Equal("ACTIVE", opCf.Get("state").String(), "Should be ACTIVE. Cloud Function is not successfully deployed.")
			assert.Equal(caiSaEmail, opCf.Get("serviceConfig.serviceAccountEmail").String(), fmt.Sprintf("Cloud Function should use the service account %s.", caiSaEmail))
			assert.Contains(opCf.Get("eventTrigger.eventType").String(), "google.cloud.pubsub.topic.v1.messagePublished", "Event Trigger is not based on Pub/Sub message. Check the EventType configuration.")

			// Cloud Function Storage Bucket
			bktArgs := gcloud.WithCommonArgs([]string{"--project", sccProjectID, "--json"})
			opSrcBucket := gcloud.Run(t, fmt.Sprintf("alpha storage ls --buckets gs://%s", caiBucket), bktArgs).Array()
			assert.Equal(caiKmsKey, opSrcBucket[0].Get("metadata.encryption.defaultKmsKeyName").String(), fmt.Sprintf("Should have same KMS key: %s", caiKmsKey))
			assert.Equal("true", opSrcBucket[0].Get("metadata.iamConfiguration.bucketPolicyOnly.enabled").String(), "Should have Bucket Policy Only enabled.")

			// Cloud Function Artifact Registry
			opAR := gcloud.Runf(t, "artifacts repositories describe %s --project %s --location %s", caiAr, sccProjectID, defaultRegion)
			assert.Equal(caiKmsKey, opAR.Get("kmsKeyName").String(), fmt.Sprintf("Should have KMS Key: %s", caiKmsKey))
			assert.Equal("DOCKER", opAR.Get("format").String(), "Should have type: DOCKER")

			// Cloud Function Pub/Sub
			opTopic := gcloud.Runf(t, "pubsub topics describe %s --project %s", caiTopic, sccProjectID)
			assert.Equal(caiTopicFullName, opTopic.Get("name").String(), fmt.Sprintf("Topic %s should have been created", caiTopicFullName))

			// Log Sink
			for _, sink := range []struct {
				name        string
				destination string
			}{
				{
					name:        "sk-c-logging-bkt",
					destination: fmt.Sprintf("storage.googleapis.com/%s", logsExportStorageBucketName),
				},
				{
					name:        "sk-c-logging-logbkt",
					destination: fmt.Sprintf("logging.googleapis.com/%s", logBktFullName),
				},
				{
					name:        "sk-c-logging-pub",
					destination: fmt.Sprintf("pubsub.googleapis.com/projects/%s/topics/%s", auditLogsProjectID, logsExportTopicName),
				},
			} {
				logSink := gcloud.Runf(t, "logging sinks describe %s --folder %s", sink.name, parentFolder)
				assert.True(logSink.Get("includeChildren").Bool(), fmt.Sprintf("sink %s should include children", sink.name))
				assert.Equal(sink.destination, logSink.Get("destination").String(), fmt.Sprintf("sink %s should have destination %s", sink.name, sink.destination))
				for _, filter := range logsFilter {
					assert.Contains(logSink.Get("filter").String(), filter, fmt.Sprintf("sink %s should include filter %s", sink.name, filter))
				}

			}

			// hub and spoke infrastructure
			enable_hub_and_spoke, err := strconv.ParseBool(bootstrap.GetTFSetupStringOutput("enable_hub_and_spoke"))
			require.NoError(t, err)
			if enable_hub_and_spoke {
				for _, hubAndSpokeProjectOutput := range []string{
					"base_net_hub_project_id",
					"restricted_net_hub_project_id",
				} {
					projectID := org.GetStringOutput(hubAndSpokeProjectOutput)
					gcOps := gcloud.WithCommonArgs([]string{"--filter", fmt.Sprintf("projectId:%s", projectID), "--format", "json"})
					projects := gcloud.Run(t, "projects list", gcOps).Array()
					assert.Equal(1, len(projects), fmt.Sprintf("project %s should exist", projectID))
					assert.Equal("ACTIVE", projects[0].Get("lifecycleState").String(), fmt.Sprintf("project %s should be ACTIVE", projectID))
				}
			}

			//projects creation
			for _, projectOutput := range []struct {
				output string
				apis   []string
			}{
				{
					output: "org_audit_logs_project_id",
					apis: []string{
						"logging.googleapis.com",
						"bigquery.googleapis.com",
					},
				},
				{
					output: "org_billing_logs_project_id",
					apis: []string{
						"logging.googleapis.com",
						"bigquery.googleapis.com",
						"billingbudgets.googleapis.com",
					},
				},
				{
					output: "org_secrets_project_id",
					apis: []string{
						"logging.googleapis.com",
						"secretmanager.googleapis.com",
					},
				},
				{
					output: "interconnect_project_id",
					apis: []string{
						"billingbudgets.googleapis.com",
						"compute.googleapis.com",
					},
				},
				{
					output: "scc_notifications_project_id",
					apis: []string{
						"logging.googleapis.com",
						"pubsub.googleapis.com",
						"securitycenter.googleapis.com",
					},
				},
				{
					output: "dns_hub_project_id",
					apis: []string{
						"compute.googleapis.com",
						"dns.googleapis.com",
						"servicenetworking.googleapis.com",
						"logging.googleapis.com",
						"cloudresourcemanager.googleapis.com",
					},
				},
			} {
				projectID := org.GetStringOutput(projectOutput.output)
				prj := gcloud.Runf(t, "projects describe %s", projectID)
				assert.Equal("ACTIVE", prj.Get("lifecycleState").String(), fmt.Sprintf("project %s should be ACTIVE", projectID))

				enabledAPIS := gcloud.Runf(t, "services list --project %s", projectID).Array()
				listApis := testutils.GetResultFieldStrSlice(enabledAPIS, "config.name")
				assert.Subset(listApis, projectOutput.apis, "APIs should have been enabled")
			}
			// shared vpc projects
			for _, envName := range []string{
				"development",
				"non-production",
				"production",
			} {
				for _, projectEnvOutput := range []struct {
					projectOutput string
					apis          []string
				}{
					{
						projectOutput: "base_shared_vpc_project_id",
						apis: []string{
							"compute.googleapis.com",
							"dns.googleapis.com",
							"servicenetworking.googleapis.com",
							"container.googleapis.com",
							"logging.googleapis.com",
							"billingbudgets.googleapis.com",
						},
					},
					{
						projectOutput: "restricted_shared_vpc_project_id",
						apis: []string{
							"compute.googleapis.com",
							"dns.googleapis.com",
							"servicenetworking.googleapis.com",
							"container.googleapis.com",
							"logging.googleapis.com",
							"cloudresourcemanager.googleapis.com",
							"accesscontextmanager.googleapis.com",
							"billingbudgets.googleapis.com",
						},
					},
				} {
					envProj := terraform.OutputMapOfObjects(t, org.GetTFOptions(), "shared_vpc_projects")[envName].(map[string]interface{})
					projectID := envProj[projectEnvOutput.projectOutput]
					prj := gcloud.Runf(t, "projects describe %s", projectID)
					assert.Equal(projectID, prj.Get("projectId").String(), fmt.Sprintf("project %s should exist", projectID))
					assert.Equal("ACTIVE", prj.Get("lifecycleState").String(), fmt.Sprintf("project %s should be ACTIVE", projectID))

					enabledAPIS := gcloud.Runf(t, "services list --project %s", projectID).Array()
					listApis := testutils.GetResultFieldStrSlice(enabledAPIS, "config.name")
					assert.Subset(listApis, projectEnvOutput.apis, "APIs should have been enabled")
				}
			}
		})
	org.Test()
}
