# Fixture generation only: point the AWS provider at a local moto server
# (see ../env.sh). Override files merge into the provider block in main.tf.
provider "aws" {
  s3_use_path_style           = true
  skip_credentials_validation = true
  skip_requesting_account_id  = true
}
