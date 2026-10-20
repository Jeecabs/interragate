resource "random_string" "token" {
  length  = 8
  special = false
}

resource "null_resource" "beacon" {
  triggers = {
    token = random_string.token.result
  }
}

output "endpoint" {
  value = "legacy-${random_string.token.result}.internal"
}
