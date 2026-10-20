// Package kb is interragate's knowledge about resource types: what they are,
// which ones hold data that a destroy would lose, which settings protect
// them, and which in-place updates still cause disruption.
//
// Unknown types still get a sensible category from their name, so the rest of
// interragate works for any provider; the curated entries make the risk
// analysis sharper for the common ones.
package kb

import (
	"sort"
	"strings"
)

// Category groups resource types for display.
type Category string

// Categories, in the order they are usually layered: foundations first.
const (
	Network    Category = "network"
	Firewall   Category = "firewall"
	Identity   Category = "identity"
	Secrets    Category = "secrets"
	DNS        Category = "dns"
	Edge       Category = "edge"
	Compute    Category = "compute"
	Containers Category = "containers"
	Serverless Category = "serverless"
	Database   Category = "database"
	Storage    Category = "storage"
	Messaging  Category = "messaging"
	Monitoring Category = "monitoring"
	Utility    Category = "utility"
	Other      Category = "other"
)

// Categories lists every category in display order.
var Categories = []Category{Network, Firewall, Identity, Secrets, DNS, Edge, Compute, Containers, Serverless, Database, Storage, Messaging, Monitoring, Utility, Other}

// Snapshot describes how a type takes a final backup when destroyed.
type Snapshot struct {
	Skip string // boolean attribute that skips the final snapshot
	Name string // attribute naming the final snapshot
}

// Info is what interragate knows about a resource type.
type Info struct {
	Type     string
	Noun     string
	Category Category
	// Stateful types hold data (or other state, such as DNS delegation or a
	// cluster's workloads) that destroying the object loses.
	Stateful bool
	// Loss says what is lost, e.g. "all objects in the bucket".
	Loss string
	// Protection lists attributes that make the provider refuse to delete
	// the object while set (true, or a value such as "ACTIVE").
	Protection []string
	// ForceDestroy names an attribute that lets a destroy delete contents
	// (objects, images, records) instead of failing.
	ForceDestroy string
	Snapshot     *Snapshot
	// Disruptive maps attributes whose in-place update still causes downtime
	// or a restart to a short explanation.
	Disruptive map[string]string
}

// Lookup returns what is known about a resource type. It never fails: types
// without a curated entry are classified by name.
func Lookup(typ string) Info {
	if info, ok := types[typ]; ok {
		info.Type = typ
		if info.Noun == "" {
			info.Noun = nounFromType(typ)
		}
		if info.Category == "" {
			info.Category = categorize(typ)
		}
		return info
	}
	return Info{Type: typ, Noun: nounFromType(typ), Category: categorize(typ)}
}

// IsProtected reports whether an attribute value turns deletion protection on.
func IsProtected(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		switch strings.ToUpper(x) {
		case "ACTIVE", "PREVENT", "DELETE_PROTECTION_ENABLED", "TRUE", "ENABLED":
			return true
		}
	}
	return false
}

// Provider returns the provider prefix of a type: aws_instance -> aws.
func Provider(typ string) string {
	if typ == "terraform_data" {
		return "terraform"
	}
	p, _, _ := strings.Cut(typ, "_")
	return p
}

func nounFromType(typ string) string {
	_, rest, ok := strings.Cut(typ, "_")
	if !ok || typ == "terraform_data" {
		rest = typ
	}
	return strings.ReplaceAll(rest, "_", " ")
}

func categorize(typ string) Category {
	best := ""
	cat := Other
	for prefix, c := range prefixes {
		if strings.HasPrefix(typ, prefix) && len(prefix) > len(best) {
			best, cat = prefix, c
		}
	}
	if cat == Other {
		for _, w := range []struct {
			word string
			cat  Category
		}{
			{"_iam_", Identity}, {"_role", Identity}, {"_policy", Identity},
			{"_secret", Secrets}, {"_dns_", DNS}, {"_firewall", Firewall},
			{"_database", Database}, {"_bucket", Storage}, {"_queue", Messaging},
			{"_topic", Messaging}, {"_alert", Monitoring}, {"_monitor", Monitoring},
			{"_dashboard", Monitoring}, {"_network", Network}, {"_subnet", Network},
		} {
			if strings.Contains(typ, w.word) {
				return w.cat
			}
		}
	}
	return cat
}

// Known returns the curated type names, sorted. Useful for docs and tests.
func Known() []string {
	out := make([]string, 0, len(types))
	for t := range types {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

var prefixes = map[string]Category{
	// AWS
	"aws_vpc": Network, "aws_subnet": Network, "aws_route": Network, "aws_internet_gateway": Network,
	"aws_nat_gateway": Network, "aws_eip": Network, "aws_network_interface": Network, "aws_vpn": Network,
	"aws_ec2_transit_gateway": Network, "aws_dx_": Network, "aws_egress_only": Network, "aws_customer_gateway": Network,
	"aws_main_route_table": Network, "aws_default_route_table": Network, "aws_default_vpc": Network, "aws_default_subnet": Network,
	"aws_flow_log": Monitoring, "aws_networkfirewall": Firewall,
	"aws_security_group": Firewall, "aws_vpc_security_group": Firewall, "aws_network_acl": Firewall, "aws_default_security_group": Firewall,
	"aws_default_network_acl": Firewall, "aws_wafv2": Firewall, "aws_waf": Firewall, "aws_shield": Firewall,
	"aws_iam_": Identity, "aws_organizations_": Identity, "aws_ssoadmin_": Identity, "aws_identitystore_": Identity,
	"aws_cognito_": Identity, "aws_lambda_permission": Identity, "aws_s3_bucket_policy": Identity,
	"aws_sqs_queue_policy": Identity, "aws_sns_topic_policy": Identity, "aws_ecr_repository_policy": Identity,
	"aws_kms_": Secrets, "aws_secretsmanager_": Secrets, "aws_ssm_parameter": Secrets, "aws_acm": Secrets,
	"aws_route53": DNS, "aws_cloudfront_": Edge, "aws_globalaccelerator_": Edge, "aws_api_gateway": Edge, "aws_apigatewayv2": Edge,
	"aws_lb": Edge, "aws_alb": Edge, "aws_elb": Edge,
	"aws_instance": Compute, "aws_launch_template": Compute, "aws_autoscaling_": Compute, "aws_ami": Compute,
	"aws_key_pair": Compute, "aws_placement_group": Compute, "aws_spot_": Compute, "aws_lightsail_": Compute,
	"aws_ebs_": Storage, "aws_volume_attachment": Storage,
	"aws_ecs_": Containers, "aws_eks_": Containers, "aws_ecr_": Containers, "aws_apprunner_": Containers,
	"aws_lambda_": Serverless, "aws_sfn_": Serverless, "aws_scheduler_": Serverless, "aws_appsync_": Serverless,
	"aws_db_": Database, "aws_rds_": Database, "aws_dynamodb_": Database, "aws_elasticache_": Database,
	"aws_memorydb_": Database, "aws_redshift": Database, "aws_docdb_": Database, "aws_neptune_": Database,
	"aws_opensearch_": Database, "aws_elasticsearch_": Database, "aws_dax_": Database, "aws_keyspaces_": Database,
	"aws_timestreamwrite_": Database, "aws_qldb_": Database, "aws_glue_": Database, "aws_athena_": Database,
	"aws_s3": Storage, "aws_efs_": Storage, "aws_fsx_": Storage, "aws_backup_": Storage, "aws_glacier_": Storage,
	"aws_storagegateway_": Storage, "aws_datasync_": Storage,
	"aws_sqs_": Messaging, "aws_sns_": Messaging, "aws_kinesis": Messaging, "aws_msk_": Messaging, "aws_mq_": Messaging,
	"aws_cloudwatch_event": Messaging, "aws_pipes_": Messaging, "aws_ses": Messaging,
	"aws_cloudwatch_": Monitoring, "aws_cloudtrail": Monitoring, "aws_xray_": Monitoring, "aws_config_": Monitoring,
	"aws_guardduty_": Monitoring, "aws_securityhub_": Monitoring, "aws_inspector": Monitoring,
	// Google
	"google_compute_network": Network, "google_compute_subnetwork": Network, "google_compute_router": Network,
	"google_compute_address": Network, "google_compute_global_address": Network, "google_compute_route": Network,
	"google_compute_vpn": Network, "google_compute_interconnect": Network, "google_vpc_access": Network,
	"google_compute_firewall": Firewall, "google_compute_security_policy": Firewall, "google_compute_network_firewall": Firewall,
	"google_project_iam": Identity, "google_service_account": Identity, "google_organization_iam": Identity, "google_folder_iam": Identity,
	"google_kms_": Secrets, "google_secret_manager_": Secrets, "google_certificate_manager": Secrets,
	"google_dns_": DNS, "google_compute_url_map": Edge, "google_compute_backend": Edge, "google_compute_target": Edge,
	"google_compute_forwarding_rule": Edge, "google_compute_global_forwarding_rule": Edge, "google_api_gateway": Edge,
	"google_compute_instance": Compute, "google_compute_disk": Storage, "google_compute_region_disk": Storage,
	"google_compute_snapshot": Storage, "google_compute_image": Compute,
	"google_container_": Containers, "google_artifact_registry": Containers, "google_cloud_run": Serverless,
	"google_cloudfunctions": Serverless, "google_workflows": Serverless, "google_cloud_scheduler": Serverless,
	"google_sql_": Database, "google_spanner_": Database, "google_bigtable_": Database, "google_bigquery_": Database,
	"google_firestore_": Database, "google_redis_": Database, "google_memcache_": Database, "google_alloydb_": Database,
	"google_storage_": Storage, "google_filestore_": Storage,
	"google_pubsub_": Messaging, "google_monitoring_": Monitoring, "google_logging_": Monitoring,
	"google_project": Other,
	// Azure
	"azurerm_virtual_network": Network, "azurerm_subnet": Network, "azurerm_route": Network, "azurerm_public_ip": Network,
	"azurerm_nat_gateway": Network, "azurerm_network_interface": Network, "azurerm_virtual_network_peering": Network,
	"azurerm_private_endpoint": Network, "azurerm_vpn": Network, "azurerm_express_route": Network,
	"azurerm_network_security": Firewall, "azurerm_firewall": Firewall, "azurerm_web_application_firewall": Firewall,
	"azurerm_role_": Identity, "azurerm_user_assigned_identity": Identity, "azurerm_federated_identity": Identity, "azuread_": Identity,
	"azurerm_key_vault": Secrets,
	"azurerm_dns_":      DNS, "azurerm_private_dns_": DNS, "azurerm_cdn_": Edge, "azurerm_frontdoor": Edge,
	"azurerm_lb": Edge, "azurerm_application_gateway": Edge, "azurerm_api_management": Edge, "azurerm_traffic_manager": Edge,
	"azurerm_linux_virtual_machine": Compute, "azurerm_windows_virtual_machine": Compute, "azurerm_virtual_machine": Compute,
	"azurerm_managed_disk": Storage, "azurerm_kubernetes_": Containers, "azurerm_container_": Containers,
	"azurerm_linux_function_app": Serverless, "azurerm_windows_function_app": Serverless, "azurerm_function_app": Serverless,
	"azurerm_linux_web_app": Compute, "azurerm_windows_web_app": Compute, "azurerm_service_plan": Compute,
	"azurerm_mssql_": Database, "azurerm_postgresql_": Database, "azurerm_mysql_": Database, "azurerm_cosmosdb_": Database,
	"azurerm_redis_": Database, "azurerm_storage_": Storage, "azurerm_servicebus_": Messaging, "azurerm_eventhub": Messaging,
	"azurerm_eventgrid_": Messaging, "azurerm_monitor_": Monitoring, "azurerm_log_analytics_": Monitoring,
	"azurerm_application_insights": Monitoring, "azurerm_resource_group": Other,
	// Kubernetes and Helm
	"kubernetes_": Containers, "helm_": Containers,
	// Edge, DNS and SaaS providers
	"cloudflare_record": DNS, "cloudflare_dns_record": DNS, "cloudflare_zone": DNS, "cloudflare_": Edge,
	"fastly_": Edge, "akamai_": Edge, "digitalocean_record": DNS, "digitalocean_domain": DNS, "dnsimple_": DNS, "ns1_": DNS,
	"github_": Other, "gitlab_": Other, "tfe_": Other, "vault_": Secrets, "onepassword_": Secrets,
	"datadog_": Monitoring, "newrelic_": Monitoring, "grafana_": Monitoring, "pagerduty_": Monitoring, "opsgenie_": Monitoring,
	"sentry_": Monitoring, "honeycombio_": Monitoring,
	"digitalocean_droplet": Compute, "digitalocean_kubernetes": Containers, "digitalocean_database": Database,
	"digitalocean_volume": Storage, "digitalocean_spaces": Storage, "digitalocean_firewall": Firewall, "digitalocean_vpc": Network,
	"hcloud_server": Compute, "hcloud_volume": Storage, "hcloud_firewall": Firewall, "hcloud_network": Network,
	"linode_instance": Compute, "linode_volume": Storage, "mongodbatlas_": Database, "snowflake_": Database,
	"postgresql_": Database, "mysql_": Database, "confluent_": Messaging, "kafka_": Messaging,
	// Utility providers that never touch real infrastructure.
	"random_": Utility, "null_": Utility, "local_": Utility, "time_": Utility, "tls_": Utility, "terraform_": Utility,
	"archive_": Utility, "external": Utility, "http": Utility, "cloudinit_": Utility,
}

var (
	rdsSnapshot     = &Snapshot{Skip: "skip_final_snapshot", Name: "final_snapshot_identifier"}
	restartDB       = "the database restarts; immediately if apply_immediately is set, otherwise in the next maintenance window"
	stopStart       = "the instance is stopped and started"
	rollingNodes    = "nodes are replaced in a rolling update"
	controlPlaneUpg = "the control plane is upgraded in place; this cannot be rolled back"
)

var types = map[string]Info{
	// AWS: databases and data stores
	"aws_db_instance": {Noun: "RDS database instance", Category: Database, Stateful: true, Loss: "all data in the database",
		Protection: []string{"deletion_protection"}, Snapshot: rdsSnapshot,
		Disruptive: map[string]string{"instance_class": restartDB, "engine_version": restartDB, "parameter_group_name": "parameter changes may need a reboot", "ca_cert_identifier": restartDB, "storage_type": "storage is migrated; performance drops while it runs", "port": restartDB}},
	"aws_rds_cluster": {Noun: "Aurora/RDS cluster", Category: Database, Stateful: true, Loss: "all data in the cluster",
		Protection: []string{"deletion_protection"}, Snapshot: rdsSnapshot,
		Disruptive: map[string]string{"engine_version": restartDB, "db_cluster_instance_class": restartDB}},
	"aws_rds_cluster_instance": {Noun: "Aurora instance", Category: Database,
		Disruptive: map[string]string{"instance_class": "the instance restarts; a writer fails over", "engine_version": restartDB}},
	"aws_rds_global_cluster":    {Noun: "Aurora global cluster", Category: Database, Stateful: true, Loss: "the global database", Protection: []string{"deletion_protection"}},
	"aws_dynamodb_table":        {Noun: "DynamoDB table", Category: Database, Stateful: true, Loss: "every item in the table", Protection: []string{"deletion_protection_enabled"}},
	"aws_dynamodb_global_table": {Noun: "DynamoDB global table", Category: Database, Stateful: true, Loss: "every item in the table"},
	"aws_elasticache_cluster": {Noun: "ElastiCache cluster", Category: Database, Stateful: true, Loss: "all cached data",
		Snapshot:   &Snapshot{Name: "final_snapshot_identifier"},
		Disruptive: map[string]string{"node_type": "nodes are replaced; expect a brief outage", "engine_version": "nodes restart for the upgrade"}},
	"aws_elasticache_replication_group": {Noun: "ElastiCache replication group", Category: Database, Stateful: true, Loss: "all data in the cache",
		Snapshot:   &Snapshot{Name: "final_snapshot_identifier"},
		Disruptive: map[string]string{"node_type": "nodes are resized online; expect failovers", "engine_version": "nodes restart for the upgrade"}},
	"aws_memorydb_cluster": {Noun: "MemoryDB cluster", Category: Database, Stateful: true, Loss: "all data in the cluster", Snapshot: &Snapshot{Name: "final_snapshot_name"}},
	"aws_redshift_cluster": {Noun: "Redshift cluster", Category: Database, Stateful: true, Loss: "all data in the warehouse", Snapshot: rdsSnapshot,
		Disruptive: map[string]string{"node_type": "the cluster is resized and read-only while it runs", "number_of_nodes": "the cluster is resized and read-only while it runs"}},
	"aws_redshiftserverless_namespace": {Noun: "Redshift Serverless namespace", Category: Database, Stateful: true, Loss: "all data in the namespace"},
	"aws_docdb_cluster":                {Noun: "DocumentDB cluster", Category: Database, Stateful: true, Loss: "all documents", Protection: []string{"deletion_protection"}, Snapshot: rdsSnapshot},
	"aws_neptune_cluster":              {Noun: "Neptune cluster", Category: Database, Stateful: true, Loss: "the whole graph", Protection: []string{"deletion_protection"}, Snapshot: rdsSnapshot},
	"aws_opensearch_domain": {Noun: "OpenSearch domain", Category: Database, Stateful: true, Loss: "every index",
		Disruptive: map[string]string{"engine_version": "a blue/green deployment runs", "cluster_config": "a blue/green deployment runs"}},
	"aws_elasticsearch_domain": {Noun: "Elasticsearch domain", Category: Database, Stateful: true, Loss: "every index",
		Disruptive: map[string]string{"elasticsearch_version": "a blue/green deployment runs", "cluster_config": "a blue/green deployment runs"}},
	"aws_keyspaces_table":          {Noun: "Keyspaces table", Category: Database, Stateful: true, Loss: "every row"},
	"aws_timestreamwrite_table":    {Noun: "Timestream table", Category: Database, Stateful: true, Loss: "all records"},
	"aws_timestreamwrite_database": {Noun: "Timestream database", Category: Database, Stateful: true, Loss: "all tables and records"},
	"aws_qldb_ledger":              {Noun: "QLDB ledger", Category: Database, Stateful: true, Loss: "the ledger and its history", Protection: []string{"deletion_protection"}},
	"aws_db_snapshot":              {Noun: "RDS snapshot", Category: Database, Stateful: true, Loss: "the backup"},
	"aws_db_cluster_snapshot":      {Noun: "RDS cluster snapshot", Category: Database, Stateful: true, Loss: "the backup"},
	"aws_glue_catalog_table":       {Noun: "Glue catalog table", Category: Database},
	"aws_mq_broker": {Noun: "Amazon MQ broker", Category: Messaging, Stateful: true, Loss: "queued messages",
		Disruptive: map[string]string{"engine_version": "the broker reboots", "host_instance_type": "the broker reboots"}},
	// AWS: storage
	"aws_s3_bucket":               {Noun: "S3 bucket", Category: Storage, Stateful: true, Loss: "every object in the bucket", ForceDestroy: "force_destroy"},
	"aws_s3_object":               {Noun: "S3 object", Category: Storage},
	"aws_s3_directory_bucket":     {Noun: "S3 directory bucket", Category: Storage, Stateful: true, Loss: "every object in the bucket", ForceDestroy: "force_destroy"},
	"aws_efs_file_system":         {Noun: "EFS file system", Category: Storage, Stateful: true, Loss: "every file"},
	"aws_ebs_volume":              {Noun: "EBS volume", Category: Storage, Stateful: true, Loss: "all data on the volume"},
	"aws_ebs_snapshot":            {Noun: "EBS snapshot", Category: Storage, Stateful: true, Loss: "the backup"},
	"aws_fsx_lustre_file_system":  {Noun: "FSx for Lustre file system", Category: Storage, Stateful: true, Loss: "every file"},
	"aws_fsx_windows_file_system": {Noun: "FSx for Windows file system", Category: Storage, Stateful: true, Loss: "every file", Snapshot: &Snapshot{Skip: "skip_final_backup"}},
	"aws_fsx_ontap_file_system":   {Noun: "FSx for ONTAP file system", Category: Storage, Stateful: true, Loss: "every file"},
	"aws_fsx_openzfs_file_system": {Noun: "FSx for OpenZFS file system", Category: Storage, Stateful: true, Loss: "every file", Snapshot: &Snapshot{Skip: "skip_final_backup"}},
	"aws_backup_vault":            {Noun: "Backup vault", Category: Storage, Stateful: true, Loss: "every recovery point", ForceDestroy: "force_destroy"},
	"aws_glacier_vault":           {Noun: "Glacier vault", Category: Storage, Stateful: true, Loss: "every archive"},
	"aws_ecr_repository":          {Noun: "ECR repository", Category: Containers, Stateful: true, Loss: "every image", ForceDestroy: "force_delete"},
	"aws_codecommit_repository":   {Noun: "CodeCommit repository", Category: Other, Stateful: true, Loss: "the repository and its history"},
	"aws_cloudwatch_log_group":    {Noun: "CloudWatch log group", Category: Monitoring, Stateful: true, Loss: "every log event"},
	// AWS: messaging
	"aws_sqs_queue":                        {Noun: "SQS queue", Category: Messaging, Stateful: true, Loss: "messages waiting in the queue"},
	"aws_sns_topic":                        {Noun: "SNS topic", Category: Messaging},
	"aws_kinesis_stream":                   {Noun: "Kinesis stream", Category: Messaging, Stateful: true, Loss: "records in the stream"},
	"aws_kinesis_firehose_delivery_stream": {Noun: "Firehose stream", Category: Messaging},
	"aws_msk_cluster": {Noun: "MSK cluster", Category: Messaging, Stateful: true, Loss: "every topic and message",
		Disruptive: map[string]string{"kafka_version": "brokers restart one at a time", "broker_node_group_info": "brokers restart one at a time"}},
	"aws_msk_serverless_cluster": {Noun: "MSK Serverless cluster", Category: Messaging, Stateful: true, Loss: "every topic and message"},
	// AWS: keys, secrets and identity
	"aws_kms_key":                     {Noun: "KMS key", Category: Secrets, Stateful: true, Loss: "the ability to decrypt anything encrypted with the key once the deletion window ends"},
	"aws_kms_replica_key":             {Noun: "KMS replica key", Category: Secrets, Stateful: true, Loss: "the ability to decrypt data in this region"},
	"aws_secretsmanager_secret":       {Noun: "Secrets Manager secret", Category: Secrets, Stateful: true, Loss: "the secret value and its versions"},
	"aws_ssm_parameter":               {Noun: "SSM parameter", Category: Secrets},
	"aws_acm_certificate":             {Noun: "ACM certificate", Category: Secrets},
	"aws_cognito_user_pool":           {Noun: "Cognito user pool", Category: Identity, Stateful: true, Loss: "every user account", Protection: []string{"deletion_protection"}},
	"aws_iam_user":                    {Noun: "IAM user", Category: Identity},
	"aws_iam_access_key":              {Noun: "IAM access key", Category: Identity},
	"aws_iam_role":                    {Noun: "IAM role", Category: Identity},
	"aws_iam_policy":                  {Noun: "IAM policy", Category: Identity},
	"aws_iam_role_policy":             {Noun: "IAM role policy", Category: Identity},
	"aws_iam_instance_profile":        {Noun: "instance profile", Category: Identity},
	"aws_iam_openid_connect_provider": {Noun: "OIDC provider", Category: Identity},
	// AWS: compute and network
	"aws_instance": {Noun: "EC2 instance", Category: Compute, Protection: []string{"disable_api_termination"},
		Disruptive: map[string]string{"instance_type": stopStart, "user_data": stopStart, "user_data_base64": stopStart, "cpu_options": stopStart}},
	"aws_launch_template":   {Noun: "launch template", Category: Compute},
	"aws_autoscaling_group": {Noun: "Auto Scaling group", Category: Compute},
	"aws_eks_cluster": {Noun: "EKS cluster", Category: Containers, Stateful: true, Loss: "every workload running in the cluster",
		Disruptive: map[string]string{"version": controlPlaneUpg}},
	"aws_eks_node_group": {Noun: "EKS node group", Category: Containers,
		Disruptive: map[string]string{"version": rollingNodes, "release_version": rollingNodes, "launch_template": rollingNodes}},
	"aws_ecs_cluster":                   {Noun: "ECS cluster", Category: Containers},
	"aws_ecs_service":                   {Noun: "ECS service", Category: Containers},
	"aws_lambda_function":               {Noun: "Lambda function", Category: Serverless},
	"aws_vpc":                           {Noun: "VPC", Category: Network},
	"aws_subnet":                        {Noun: "subnet", Category: Network},
	"aws_route_table":                   {Noun: "route table", Category: Network},
	"aws_nat_gateway":                   {Noun: "NAT gateway", Category: Network},
	"aws_internet_gateway":              {Noun: "internet gateway", Category: Network},
	"aws_eip":                           {Noun: "Elastic IP", Category: Network},
	"aws_security_group":                {Noun: "security group", Category: Firewall},
	"aws_security_group_rule":           {Noun: "security group rule", Category: Firewall},
	"aws_lb":                            {Noun: "load balancer", Category: Edge, Protection: []string{"enable_deletion_protection"}},
	"aws_alb":                           {Noun: "load balancer", Category: Edge, Protection: []string{"enable_deletion_protection"}},
	"aws_lb_listener":                   {Noun: "listener", Category: Edge},
	"aws_lb_target_group":               {Noun: "target group", Category: Edge},
	"aws_cloudfront_distribution":       {Noun: "CloudFront distribution", Category: Edge},
	"aws_lb_target_group_attachment":    {Noun: "target group attachment", Category: Edge},
	"aws_lb_listener_rule":              {Noun: "listener rule", Category: Edge},
	"aws_cloudwatch_metric_alarm":       {Noun: "CloudWatch alarm", Category: Monitoring},
	"aws_cloudwatch_event_rule":         {Noun: "EventBridge rule", Category: Messaging},
	"aws_cloudwatch_event_target":       {Noun: "EventBridge target", Category: Messaging},
	"aws_kms_alias":                     {Noun: "KMS alias", Category: Secrets},
	"aws_db_subnet_group":               {Noun: "DB subnet group", Category: Database},
	"aws_db_parameter_group":            {Noun: "DB parameter group", Category: Database},
	"aws_s3_bucket_public_access_block": {Noun: "S3 public access block", Category: Storage},
	"aws_s3_bucket_versioning":          {Noun: "S3 versioning configuration", Category: Storage},
	"aws_s3_bucket_policy":              {Noun: "S3 bucket policy", Category: Identity},
	"aws_s3_bucket_server_side_encryption_configuration": {Noun: "S3 encryption configuration", Category: Storage},
	"aws_s3_bucket_lifecycle_configuration":              {Noun: "S3 lifecycle configuration", Category: Storage},
	"aws_route_table_association":                        {Noun: "route table association", Category: Network},
	"aws_route":                                          {Noun: "route", Category: Network},
	"aws_vpc_endpoint":                                   {Noun: "VPC endpoint", Category: Network},
	"aws_vpc_security_group_ingress_rule":                {Noun: "security group ingress rule", Category: Firewall},
	"aws_vpc_security_group_egress_rule":                 {Noun: "security group egress rule", Category: Firewall},
	"aws_iam_role_policy_attachment":                     {Noun: "IAM policy attachment", Category: Identity},
	"aws_iam_policy_attachment":                          {Noun: "IAM policy attachment", Category: Identity},
	"aws_iam_user_policy_attachment":                     {Noun: "IAM policy attachment", Category: Identity},
	"aws_iam_group":                                      {Noun: "IAM group", Category: Identity},
	"aws_lambda_permission":                              {Noun: "Lambda permission", Category: Identity},
	"aws_lambda_alias":                                   {Noun: "Lambda alias", Category: Serverless},
	"aws_lambda_event_source_mapping":                    {Noun: "Lambda event source mapping", Category: Serverless},
	"aws_api_gateway_rest_api":                           {Noun: "API Gateway REST API", Category: Edge},
	"aws_apigatewayv2_api":                               {Noun: "API Gateway HTTP API", Category: Edge},
	"aws_ecs_task_definition":                            {Noun: "ECS task definition", Category: Containers},
	"aws_sns_topic_subscription":                         {Noun: "SNS subscription", Category: Messaging},
	"aws_sqs_queue_policy":                               {Noun: "SQS queue policy", Category: Identity},
	"aws_wafv2_web_acl":                                  {Noun: "WAF web ACL", Category: Firewall},
	"aws_network_acl":                                    {Noun: "network ACL", Category: Firewall},
	"aws_ec2_transit_gateway":                            {Noun: "transit gateway", Category: Network},
	"aws_vpc_peering_connection":                         {Noun: "VPC peering connection", Category: Network},
	"aws_route53_zone":                                   {Noun: "Route 53 hosted zone", Category: DNS, Stateful: true, Loss: "every record in the zone, and the zone's name servers (delegations to them break)", ForceDestroy: "force_destroy"},
	"aws_route53_record":                                 {Noun: "DNS record", Category: DNS},
	// Google
	"google_sql_database_instance": {Noun: "Cloud SQL instance", Category: Database, Stateful: true, Loss: "every database on the instance",
		Protection: []string{"deletion_protection", "settings.deletion_protection_enabled"},
		Disruptive: map[string]string{"settings": "tier and flag changes restart the instance", "database_version": "the instance restarts for the upgrade"}},
	"google_sql_database":                 {Noun: "Cloud SQL database", Category: Database, Stateful: true, Loss: "all data in the database"},
	"google_spanner_instance":             {Noun: "Spanner instance", Category: Database, Stateful: true, Loss: "every database on the instance"},
	"google_spanner_database":             {Noun: "Spanner database", Category: Database, Stateful: true, Loss: "all data in the database", Protection: []string{"deletion_protection"}},
	"google_bigtable_instance":            {Noun: "Bigtable instance", Category: Database, Stateful: true, Loss: "every table", Protection: []string{"deletion_protection"}},
	"google_bigtable_table":               {Noun: "Bigtable table", Category: Database, Stateful: true, Loss: "every row", Protection: []string{"deletion_protection"}},
	"google_bigquery_dataset":             {Noun: "BigQuery dataset", Category: Database, Stateful: true, Loss: "every table in the dataset"},
	"google_bigquery_table":               {Noun: "BigQuery table", Category: Database, Stateful: true, Loss: "every row", Protection: []string{"deletion_protection"}},
	"google_firestore_database":           {Noun: "Firestore database", Category: Database, Stateful: true, Loss: "every document", Protection: []string{"delete_protection_state"}},
	"google_alloydb_cluster":              {Noun: "AlloyDB cluster", Category: Database, Stateful: true, Loss: "all data in the cluster"},
	"google_redis_instance":               {Noun: "Memorystore instance", Category: Database, Stateful: true, Loss: "all data in the instance"},
	"google_storage_bucket":               {Noun: "Cloud Storage bucket", Category: Storage, Stateful: true, Loss: "every object in the bucket", ForceDestroy: "force_destroy"},
	"google_compute_disk":                 {Noun: "persistent disk", Category: Storage, Stateful: true, Loss: "all data on the disk"},
	"google_compute_region_disk":          {Noun: "regional persistent disk", Category: Storage, Stateful: true, Loss: "all data on the disk"},
	"google_compute_snapshot":             {Noun: "disk snapshot", Category: Storage, Stateful: true, Loss: "the backup"},
	"google_filestore_instance":           {Noun: "Filestore instance", Category: Storage, Stateful: true, Loss: "every file"},
	"google_kms_crypto_key":               {Noun: "Cloud KMS key", Category: Secrets, Stateful: true, Loss: "every key version, and with them anything encrypted by the key"},
	"google_secret_manager_secret":        {Noun: "Secret Manager secret", Category: Secrets, Stateful: true, Loss: "the secret and its versions"},
	"google_pubsub_subscription":          {Noun: "Pub/Sub subscription", Category: Messaging, Stateful: true, Loss: "unacknowledged messages"},
	"google_pubsub_topic":                 {Noun: "Pub/Sub topic", Category: Messaging},
	"google_artifact_registry_repository": {Noun: "Artifact Registry repository", Category: Containers, Stateful: true, Loss: "every image and package"},
	"google_container_cluster": {Noun: "GKE cluster", Category: Containers, Stateful: true, Loss: "every workload running in the cluster", Protection: []string{"deletion_protection"},
		Disruptive: map[string]string{"min_master_version": controlPlaneUpg, "node_version": rollingNodes}},
	"google_container_node_pool": {Noun: "GKE node pool", Category: Containers, Disruptive: map[string]string{"version": rollingNodes, "node_count": "nodes are added or drained"}},
	"google_compute_instance": {Noun: "Compute Engine VM", Category: Compute, Protection: []string{"deletion_protection"},
		Disruptive: map[string]string{"machine_type": "the VM is stopped and started", "metadata_startup_script": "the VM is replaced or restarted"}},
	"google_project":            {Noun: "Google Cloud project", Category: Other, Stateful: true, Loss: "every resource in the project", Protection: []string{"deletion_policy"}},
	"google_folder":             {Noun: "Google Cloud folder", Category: Other, Protection: []string{"deletion_protection"}},
	"google_dns_managed_zone":   {Noun: "Cloud DNS zone", Category: DNS, Stateful: true, Loss: "every record in the zone, and its name servers", ForceDestroy: "force_destroy"},
	"google_dns_record_set":     {Noun: "DNS record", Category: DNS},
	"google_compute_firewall":   {Noun: "firewall rule", Category: Firewall},
	"google_compute_network":    {Noun: "VPC network", Category: Network},
	"google_compute_subnetwork": {Noun: "subnet", Category: Network},
	// Azure
	"azurerm_resource_group":    {Noun: "resource group", Category: Other, Stateful: true, Loss: "every resource in the group"},
	"azurerm_storage_account":   {Noun: "storage account", Category: Storage, Stateful: true, Loss: "every blob, file, queue and table in the account"},
	"azurerm_storage_container": {Noun: "storage container", Category: Storage, Stateful: true, Loss: "every blob in the container"},
	"azurerm_storage_share":     {Noun: "file share", Category: Storage, Stateful: true, Loss: "every file in the share"},
	"azurerm_managed_disk":      {Noun: "managed disk", Category: Storage, Stateful: true, Loss: "all data on the disk"},
	"azurerm_mssql_server":      {Noun: "SQL server", Category: Database, Stateful: true, Loss: "every database on the server"},
	"azurerm_mssql_database":    {Noun: "SQL database", Category: Database, Stateful: true, Loss: "all data in the database", Disruptive: map[string]string{"sku_name": "connections drop while the tier changes"}},
	"azurerm_postgresql_flexible_server": {Noun: "PostgreSQL flexible server", Category: Database, Stateful: true, Loss: "every database on the server",
		Disruptive: map[string]string{"sku_name": "the server restarts", "version": "the server restarts for a major upgrade"}},
	"azurerm_postgresql_server":     {Noun: "PostgreSQL server", Category: Database, Stateful: true, Loss: "every database on the server"},
	"azurerm_mysql_flexible_server": {Noun: "MySQL flexible server", Category: Database, Stateful: true, Loss: "every database on the server", Disruptive: map[string]string{"sku_name": "the server restarts"}},
	"azurerm_cosmosdb_account":      {Noun: "Cosmos DB account", Category: Database, Stateful: true, Loss: "every database in the account"},
	"azurerm_cosmosdb_sql_database": {Noun: "Cosmos DB database", Category: Database, Stateful: true, Loss: "every container in the database"},
	"azurerm_redis_cache":           {Noun: "Azure Cache for Redis", Category: Database, Stateful: true, Loss: "all cached data"},
	"azurerm_key_vault":             {Noun: "Key Vault", Category: Secrets, Stateful: true, Loss: "every key, secret and certificate (until purged)"},
	"azurerm_key_vault_key":         {Noun: "Key Vault key", Category: Secrets, Stateful: true, Loss: "the key, and the ability to decrypt data encrypted with it"},
	"azurerm_key_vault_secret":      {Noun: "Key Vault secret", Category: Secrets},
	"azurerm_kubernetes_cluster": {Noun: "AKS cluster", Category: Containers, Stateful: true, Loss: "every workload running in the cluster",
		Disruptive: map[string]string{"kubernetes_version": controlPlaneUpg}},
	"azurerm_container_registry":      {Noun: "container registry", Category: Containers, Stateful: true, Loss: "every image"},
	"azurerm_servicebus_namespace":    {Noun: "Service Bus namespace", Category: Messaging, Stateful: true, Loss: "every queue, topic and message"},
	"azurerm_eventhub_namespace":      {Noun: "Event Hubs namespace", Category: Messaging, Stateful: true, Loss: "every event hub and its events"},
	"azurerm_log_analytics_workspace": {Noun: "Log Analytics workspace", Category: Monitoring, Stateful: true, Loss: "all collected logs"},
	"azurerm_recovery_services_vault": {Noun: "Recovery Services vault", Category: Storage, Stateful: true, Loss: "every backup"},
	"azurerm_dns_zone":                {Noun: "DNS zone", Category: DNS, Stateful: true, Loss: "every record in the zone, and its name servers"},
	"azurerm_linux_virtual_machine":   {Noun: "Linux VM", Category: Compute, Disruptive: map[string]string{"size": "the VM restarts"}},
	"azurerm_windows_virtual_machine": {Noun: "Windows VM", Category: Compute, Disruptive: map[string]string{"size": "the VM restarts"}},
	"azurerm_network_security_group":  {Noun: "network security group", Category: Firewall},
	"azurerm_network_security_rule":   {Noun: "network security rule", Category: Firewall},
	// Kubernetes
	"kubernetes_namespace":                  {Noun: "namespace", Category: Containers, Stateful: true, Loss: "every object in the namespace"},
	"kubernetes_namespace_v1":               {Noun: "namespace", Category: Containers, Stateful: true, Loss: "every object in the namespace"},
	"kubernetes_persistent_volume_claim":    {Noun: "persistent volume claim", Category: Storage, Stateful: true, Loss: "the claimed volume, if its reclaim policy is Delete"},
	"kubernetes_persistent_volume_claim_v1": {Noun: "persistent volume claim", Category: Storage, Stateful: true, Loss: "the claimed volume, if its reclaim policy is Delete"},
	"kubernetes_persistent_volume":          {Noun: "persistent volume", Category: Storage, Stateful: true, Loss: "the volume, if its reclaim policy is Delete"},
	"kubernetes_persistent_volume_v1":       {Noun: "persistent volume", Category: Storage, Stateful: true, Loss: "the volume, if its reclaim policy is Delete"},
	"helm_release":                          {Noun: "Helm release", Category: Containers},
	// SaaS and others
	"github_repository":             {Noun: "GitHub repository", Category: Other, Stateful: true, Loss: "the repository, its history, issues and pull requests"},
	"gitlab_project":                {Noun: "GitLab project", Category: Other, Stateful: true, Loss: "the project, its history and issues"},
	"tfe_workspace":                 {Noun: "HCP Terraform workspace", Category: Other, Stateful: true, Loss: "the workspace's state and run history"},
	"cloudflare_zone":               {Noun: "Cloudflare zone", Category: DNS, Stateful: true, Loss: "every record and setting in the zone"},
	"cloudflare_r2_bucket":          {Noun: "R2 bucket", Category: Storage, Stateful: true, Loss: "every object in the bucket"},
	"cloudflare_record":             {Noun: "DNS record", Category: DNS},
	"cloudflare_dns_record":         {Noun: "DNS record", Category: DNS},
	"digitalocean_database_cluster": {Noun: "managed database", Category: Database, Stateful: true, Loss: "all data in the cluster"},
	"digitalocean_volume":           {Noun: "block storage volume", Category: Storage, Stateful: true, Loss: "all data on the volume"},
	"digitalocean_spaces_bucket":    {Noun: "Spaces bucket", Category: Storage, Stateful: true, Loss: "every object in the bucket", ForceDestroy: "force_destroy"},
	"mongodbatlas_cluster":          {Noun: "Atlas cluster", Category: Database, Stateful: true, Loss: "all data in the cluster"},
	"mongodbatlas_advanced_cluster": {Noun: "Atlas cluster", Category: Database, Stateful: true, Loss: "all data in the cluster"},
	"snowflake_database":            {Noun: "Snowflake database", Category: Database, Stateful: true, Loss: "every schema and table"},
	"snowflake_schema":              {Noun: "Snowflake schema", Category: Database, Stateful: true, Loss: "every table in the schema"},
	"snowflake_table":               {Noun: "Snowflake table", Category: Database, Stateful: true, Loss: "every row"},
	"postgresql_database":           {Noun: "PostgreSQL database", Category: Database, Stateful: true, Loss: "all data in the database"},
	"mysql_database":                {Noun: "MySQL database", Category: Database, Stateful: true, Loss: "all data in the database"},
	"vault_mount":                   {Noun: "Vault secrets engine", Category: Secrets, Stateful: true, Loss: "every secret stored in the mount"},
	"confluent_kafka_topic":         {Noun: "Kafka topic", Category: Messaging, Stateful: true, Loss: "every message in the topic"},
	"kafka_topic":                   {Noun: "Kafka topic", Category: Messaging, Stateful: true, Loss: "every message in the topic"},
	"hcloud_volume":                 {Noun: "Hetzner volume", Category: Storage, Stateful: true, Loss: "all data on the volume"},
	"linode_volume":                 {Noun: "Linode volume", Category: Storage, Stateful: true, Loss: "all data on the volume"},
	// Utilities
	"terraform_data":  {Noun: "terraform_data", Category: Utility},
	"null_resource":   {Noun: "null_resource", Category: Utility},
	"random_password": {Noun: "random password", Category: Utility},
	"random_string":   {Noun: "random string", Category: Utility},
	"random_id":       {Noun: "random ID", Category: Utility},
	"tls_private_key": {Noun: "TLS private key", Category: Utility},
	"local_file":      {Noun: "local file", Category: Utility},
}
