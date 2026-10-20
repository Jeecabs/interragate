variable "name" {
  description = "Cluster name used to prefix cache nodes."
  type        = string
}

resource "random_id" "node" {
  count       = 2
  byte_length = 2
  prefix      = "${var.name}-"
}

resource "null_resource" "warmup" {
  triggers = {
    nodes = join(",", random_id.node[*].hex)
  }
}

output "nodes" {
  value = random_id.node[*].hex
}
