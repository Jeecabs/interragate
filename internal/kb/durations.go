package kb

// minute keeps the table below readable.
const minute = 60

// DefaultDuration is how many seconds an operation takes when interragate
// has no figure for its type. Most resources are one API call that returns
// within a few seconds.
const DefaultDuration = 5

// ReadDuration is how many seconds reading a data source during apply takes:
// one API call with nothing to wait for.
const ReadDuration = 2

// Timing is how long each operation on a resource type typically takes, in
// seconds. Zero means the default.
type Timing struct {
	Create, Update, Destroy int
}

// Duration returns how many seconds an operation ("create", "update",
// "destroy" or "read") on a resource type typically takes, and whether the
// figure comes from the table rather than the default.
func Duration(typ, op string) (seconds int, known bool) {
	t := durations[typ]
	switch op {
	case "create":
		seconds = t.Create
	case "update":
		seconds = t.Update
	case "destroy":
		seconds = t.Destroy
	case "read":
		return ReadDuration, false
	}
	if seconds == 0 {
		return DefaultDuration, false
	}
	return seconds, true
}

// durations are rough typical times for slow, common types: how long
// Terraform usually waits while the provider polls an object into its final
// state. They are rounded figures from experience, not measurements; size,
// engine, region and options all move them. Update figures are for changes
// the service has to roll out, not for tags. Where an operation is usually
// quick it is left at the default.
var durations = map[string]Timing{
	// AWS
	"aws_db_instance":                        {Create: 10 * minute, Update: 5 * minute, Destroy: 5 * minute},
	"aws_rds_cluster":                        {Create: 3 * minute, Update: 2 * minute, Destroy: 3 * minute},
	"aws_rds_cluster_instance":               {Create: 10 * minute, Update: 5 * minute, Destroy: 5 * minute},
	"aws_docdb_cluster_instance":             {Create: 10 * minute, Update: 5 * minute, Destroy: 5 * minute},
	"aws_neptune_cluster_instance":           {Create: 10 * minute, Update: 5 * minute, Destroy: 5 * minute},
	"aws_elasticache_replication_group":      {Create: 12 * minute, Update: 10 * minute, Destroy: 8 * minute},
	"aws_elasticache_cluster":                {Create: 8 * minute, Update: 5 * minute, Destroy: 6 * minute},
	"aws_memorydb_cluster":                   {Create: 15 * minute, Update: 10 * minute, Destroy: 10 * minute},
	"aws_opensearch_domain":                  {Create: 20 * minute, Update: 20 * minute, Destroy: 10 * minute},
	"aws_elasticsearch_domain":               {Create: 20 * minute, Update: 20 * minute, Destroy: 10 * minute},
	"aws_redshift_cluster":                   {Create: 15 * minute, Update: 15 * minute, Destroy: 10 * minute},
	"aws_msk_cluster":                        {Create: 30 * minute, Update: 20 * minute, Destroy: 15 * minute},
	"aws_mq_broker":                          {Create: 15 * minute, Update: 10 * minute, Destroy: 10 * minute},
	"aws_eks_cluster":                        {Create: 12 * minute, Update: 10 * minute, Destroy: 8 * minute},
	"aws_eks_node_group":                     {Create: 5 * minute, Update: 10 * minute, Destroy: 6 * minute},
	"aws_ecs_service":                        {Create: 1 * minute, Destroy: 3 * minute},
	"aws_instance":                           {Create: 1 * minute, Update: 1 * minute, Destroy: 1 * minute},
	"aws_lb":                                 {Create: 3 * minute, Destroy: 1 * minute},
	"aws_alb":                                {Create: 3 * minute, Destroy: 1 * minute},
	"aws_nat_gateway":                        {Create: 2 * minute, Destroy: 1 * minute},
	"aws_cloudfront_distribution":            {Create: 5 * minute, Update: 5 * minute, Destroy: 10 * minute},
	"aws_acm_certificate_validation":         {Create: 3 * minute},
	"aws_vpn_connection":                     {Create: 5 * minute, Update: 5 * minute, Destroy: 5 * minute},
	"aws_ec2_transit_gateway":                {Create: 3 * minute, Destroy: 3 * minute},
	"aws_ec2_transit_gateway_vpc_attachment": {Create: 2 * minute, Destroy: 2 * minute},
	// Google
	"google_sql_database_instance": {Create: 10 * minute, Update: 5 * minute, Destroy: 2 * minute},
	"google_container_cluster":     {Create: 10 * minute, Update: 10 * minute, Destroy: 6 * minute},
	"google_container_node_pool":   {Create: 5 * minute, Update: 10 * minute, Destroy: 5 * minute},
	"google_redis_instance":        {Create: 8 * minute, Update: 5 * minute, Destroy: 5 * minute},
	// Azure
	"azurerm_kubernetes_cluster":           {Create: 10 * minute, Update: 10 * minute, Destroy: 8 * minute},
	"azurerm_kubernetes_cluster_node_pool": {Create: 5 * minute, Update: 10 * minute, Destroy: 5 * minute},
	"azurerm_mssql_server":                 {Create: 3 * minute, Update: 2 * minute, Destroy: 2 * minute},
	"azurerm_mssql_database":               {Create: 3 * minute, Update: 5 * minute, Destroy: 2 * minute},
	"azurerm_postgresql_flexible_server":   {Create: 10 * minute, Update: 5 * minute, Destroy: 5 * minute},
	"azurerm_mysql_flexible_server":        {Create: 10 * minute, Update: 5 * minute, Destroy: 5 * minute},
	"azurerm_application_gateway":          {Create: 8 * minute, Update: 5 * minute, Destroy: 5 * minute},
	"azurerm_virtual_network_gateway":      {Create: 40 * minute, Update: 20 * minute, Destroy: 15 * minute},
	"azurerm_redis_cache":                  {Create: 20 * minute, Update: 15 * minute, Destroy: 10 * minute},
	"azurerm_api_management":               {Create: 40 * minute, Update: 10 * minute, Destroy: 10 * minute},
}
