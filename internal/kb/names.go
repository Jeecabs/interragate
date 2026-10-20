package kb

import "strings"

// Name describes the argument that names an object, for types whose create
// fails when another object already has the name.
type Name struct {
	// Attr is the naming argument, such as "name" or "identifier".
	Attr string
	// Scope is how widely the name must be unique: "within a VPC".
	Scope string
	// Within lists arguments that narrow the scope, such as a security
	// group's vpc_id: a different VPC frees the name.
	Within []string
	// Prefix is the argument that generates a fresh name on every create,
	// where the type has one.
	Prefix string
	// Reserved says how long a deleted object keeps its name, for types
	// whose names are not free again at once.
	Reserved string
}

var names = map[string]Name{
	"aws_security_group":                {Attr: "name", Scope: "within a VPC", Within: []string{"vpc_id"}, Prefix: "name_prefix"},
	"aws_iam_role":                      {Attr: "name", Scope: "within an account", Prefix: "name_prefix"},
	"aws_iam_policy":                    {Attr: "name", Scope: "within an account", Prefix: "name_prefix"},
	"aws_iam_user":                      {Attr: "name", Scope: "within an account"},
	"aws_iam_group":                     {Attr: "name", Scope: "within an account"},
	"aws_iam_instance_profile":          {Attr: "name", Scope: "within an account", Prefix: "name_prefix"},
	"aws_lb":                            {Attr: "name", Scope: "within a region", Prefix: "name_prefix"},
	"aws_alb":                           {Attr: "name", Scope: "within a region", Prefix: "name_prefix"},
	"aws_lb_target_group":               {Attr: "name", Scope: "within a region", Prefix: "name_prefix"},
	"aws_launch_template":               {Attr: "name", Scope: "within a region", Prefix: "name_prefix"},
	"aws_autoscaling_group":             {Attr: "name", Scope: "within a region", Prefix: "name_prefix"},
	"aws_db_instance":                   {Attr: "identifier", Scope: "within a region", Prefix: "identifier_prefix"},
	"aws_rds_cluster":                   {Attr: "cluster_identifier", Scope: "within a region", Prefix: "cluster_identifier_prefix"},
	"aws_db_subnet_group":               {Attr: "name", Scope: "within a region", Prefix: "name_prefix"},
	"aws_db_parameter_group":            {Attr: "name", Scope: "within a region", Prefix: "name_prefix"},
	"aws_elasticache_replication_group": {Attr: "replication_group_id", Scope: "within a region"},
	"aws_elasticache_cluster":           {Attr: "cluster_id", Scope: "within a region"},
	"aws_s3_bucket":                     {Attr: "bucket", Scope: "across all of AWS", Prefix: "bucket_prefix"},
	"aws_lambda_function":               {Attr: "function_name", Scope: "within a region"},
	"aws_ecr_repository":                {Attr: "name", Scope: "within a region"},
	"aws_cloudwatch_log_group":          {Attr: "name", Scope: "within a region", Prefix: "name_prefix"},
	"aws_kms_alias":                     {Attr: "name", Scope: "within a region", Prefix: "name_prefix"},
	"aws_dynamodb_table":                {Attr: "name", Scope: "within a region"},
	"aws_opensearch_domain":             {Attr: "domain_name", Scope: "within a region"},
	"aws_eks_cluster":                   {Attr: "name", Scope: "within a region"},
	"aws_eks_node_group":                {Attr: "node_group_name", Scope: "within a cluster", Within: []string{"cluster_name"}, Prefix: "node_group_name_prefix"},
	"aws_ssm_parameter":                 {Attr: "name", Scope: "within a region"},
	"aws_secretsmanager_secret": {Attr: "name", Scope: "within a region", Prefix: "name_prefix",
		Reserved: "for its recovery window after it is deleted"},
	"google_compute_instance":          {Attr: "name", Scope: "within a zone", Within: []string{"zone"}},
	"google_compute_instance_template": {Attr: "name", Scope: "within a project", Prefix: "name_prefix"},
	"google_compute_network":           {Attr: "name", Scope: "within a project"},
	"google_compute_subnetwork":        {Attr: "name", Scope: "within a region", Within: []string{"region"}},
	"google_compute_firewall":          {Attr: "name", Scope: "within a project"},
	"google_container_cluster":         {Attr: "name", Scope: "within a location", Within: []string{"location"}},
	"google_storage_bucket":            {Attr: "name", Scope: "across all of Google Cloud"},
	"google_service_account":           {Attr: "account_id", Scope: "within a project"},
	"google_pubsub_topic":              {Attr: "name", Scope: "within a project"},
	"google_sql_database_instance": {Attr: "name", Scope: "within a project",
		Reserved: "for up to a week after it is deleted"},
}

// azureGlobal are Azure resources whose name is a DNS name, so unique
// across all of Azure rather than within a resource group.
var azureGlobal = map[string]bool{
	"azurerm_storage_account": true, "azurerm_key_vault": true, "azurerm_container_registry": true, "azurerm_cosmosdb_account": true,
	"azurerm_mssql_server": true, "azurerm_postgresql_flexible_server": true, "azurerm_mysql_flexible_server": true,
	"azurerm_linux_web_app": true, "azurerm_windows_web_app": true, "azurerm_linux_function_app": true, "azurerm_windows_function_app": true,
	"azurerm_redis_cache": true, "azurerm_servicebus_namespace": true, "azurerm_eventhub_namespace": true, "azurerm_search_service": true,
}

// NameOf returns how a type is named, for types whose names must be unique.
// Every other azurerm resource with a name is unique within its resource
// group; a resource group, within its subscription.
func NameOf(typ string) (Name, bool) {
	if n, ok := names[typ]; ok {
		return n, true
	}
	switch {
	case typ == "azurerm_resource_group":
		return Name{Attr: "name", Scope: "within a subscription"}, true
	case azureGlobal[typ]:
		return Name{Attr: "name", Scope: "across all of Azure"}, true
	case strings.HasPrefix(typ, "azurerm_"):
		return Name{Attr: "name", Scope: "within a resource group", Within: []string{"resource_group_name"}}, true
	}
	return Name{}, false
}
