# Scenario "aws-webapp", version 2: the proposed change to the storefront.

terraform {
  required_version = ">= 1.6"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.0"
    }
  }
}

provider "aws" {
  region = "us-east-1"

  default_tags {
    tags = {
      Project   = "storefront"
      ManagedBy = "terraform"
      Team      = "commerce"
    }
  }
}

variable "environment" {
  description = "Deployment environment name."
  type        = string
  default     = "prod"
}

variable "azs" {
  description = "Availability zones for the VPC."
  type        = list(string)
  default     = ["us-east-1a", "us-east-1c"]
}

variable "db_password" {
  description = "Master password for the orders database."
  type        = string
  sensitive   = true
  default     = "fixture-password-123"
}

locals {
  name = "storefront-${var.environment}"
}

module "network" {
  source = "./modules/network"
  name   = local.name
  cidr   = "10.20.0.0/16"
  azs    = var.azs
}

# Security groups

resource "aws_security_group" "alb" {
  name        = "${local.name}-alb"
  description = "Public web traffic into the load balancer"
  vpc_id      = module.network.vpc_id

  ingress {
    description = "HTTPS"
    from_port   = 443
    to_port     = 443
    protocol    = "tcp"
    cidr_blocks = ["0.0.0.0/0"]
  }

  ingress {
    description = "HTTP, redirected to HTTPS"
    from_port   = 80
    to_port     = 80
    protocol    = "tcp"
    cidr_blocks = ["0.0.0.0/0"]
  }

  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }
}

resource "aws_security_group" "app" {
  name        = "${local.name}-app"
  description = "App servers behind the load balancer"
  vpc_id      = module.network.vpc_id

  ingress {
    description     = "From the load balancer"
    from_port       = 8080
    to_port         = 8080
    protocol        = "tcp"
    security_groups = [aws_security_group.alb.id]
  }

  ingress {
    description = "TEMP: debug access"
    from_port   = 22
    to_port     = 22
    protocol    = "tcp"
    cidr_blocks = ["0.0.0.0/0"]
  }

  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }
}

resource "aws_security_group" "db" {
  name        = "${local.name}-db"
  description = "Postgres from the app servers"
  vpc_id      = module.network.vpc_id

  ingress {
    description     = "Postgres"
    from_port       = 5432
    to_port         = 5432
    protocol        = "tcp"
    security_groups = [aws_security_group.app.id]
  }
}

# Load balancer

resource "aws_lb" "web" {
  name               = "${local.name}-web"
  load_balancer_type = "application"
  security_groups    = [aws_security_group.alb.id]
  subnets            = module.network.public_subnet_ids
}

resource "aws_lb_target_group" "app" {
  name     = "${local.name}-app"
  port     = 8080
  protocol = "HTTP"
  vpc_id   = module.network.vpc_id

  health_check {
    path = "/healthz"
  }
}

resource "aws_lb_listener" "http" {
  load_balancer_arn = aws_lb.web.arn
  port              = 80
  protocol          = "HTTP"

  default_action {
    type             = "forward"
    target_group_arn = aws_lb_target_group.app.arn
  }
}

resource "aws_lb_target_group_attachment" "web" {
  target_group_arn = aws_lb_target_group.app.arn
  target_id        = aws_instance.app.id
  port             = 8080
}

# Compute

resource "aws_iam_role" "app" {
  name = "${local.name}-app"
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "ec2.amazonaws.com" }
      Action    = "sts:AssumeRole"
    }]
  })
}

resource "aws_iam_role_policy" "app" {
  name = "app-access"
  role = aws_iam_role.app.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Sid      = "ReadAssets"
        Effect   = "Allow"
        Action   = ["s3:GetObject", "s3:ListBucket"]
        Resource = [aws_s3_bucket.assets.arn, "${aws_s3_bucket.assets.arn}/*"]
      },
      {
        Sid      = "PublishOrders"
        Effect   = "Allow"
        Action   = ["sqs:SendMessage"]
        Resource = [aws_sqs_queue.orders.arn]
      },
      {
        Sid      = "DebugEverything"
        Effect   = "Allow"
        Action   = ["s3:*"]
        Resource = ["*"]
      },
    ]
  })
}

resource "aws_iam_instance_profile" "app" {
  name = "${local.name}-app"
  role = aws_iam_role.app.name
}

resource "aws_instance" "app" {
  ami                    = "ami-1e749f67"
  instance_type          = "t3.small"
  subnet_id              = module.network.private_subnet_ids[0]
  vpc_security_group_ids = [aws_security_group.app.id]
  iam_instance_profile   = aws_iam_instance_profile.app.name

  tags = { Name = "${local.name}-web" }
}

# Data

resource "aws_kms_key" "data" {
  description             = "Encrypts storefront data at rest"
  deletion_window_in_days = 30
  enable_key_rotation     = true
}

resource "aws_kms_alias" "data" {
  name          = "alias/${local.name}-data"
  target_key_id = aws_kms_key.data.key_id
}

resource "aws_db_subnet_group" "main" {
  name       = "${local.name}-db"
  subnet_ids = module.network.private_subnet_ids
}

resource "aws_db_instance" "orders" {
  identifier                = "${local.name}-orders"
  engine                    = "postgres"
  engine_version            = "16.4"
  instance_class            = "db.t4g.medium"
  allocated_storage         = 50
  db_name                   = "orders"
  username                  = "storefront"
  password                  = var.db_password
  db_subnet_group_name      = aws_db_subnet_group.main.name
  vpc_security_group_ids    = [aws_security_group.db.id]
  backup_retention_period   = 7
  deletion_protection       = false
  skip_final_snapshot       = true
  storage_encrypted         = true
  kms_key_id                = aws_kms_key.data.arn
}

resource "aws_ssm_parameter" "db_endpoint" {
  name  = "/${local.name}/db/endpoint"
  type  = "String"
  value = aws_db_instance.orders.address
}

resource "aws_s3_bucket" "assets" {
  bucket = "${local.name}-assets"
}

resource "aws_s3_bucket_public_access_block" "assets" {
  bucket                  = aws_s3_bucket.assets.id
  block_public_acls       = true
  block_public_policy     = false
  ignore_public_acls      = true
  restrict_public_buckets = false
}

resource "aws_s3_bucket_versioning" "assets" {
  bucket = aws_s3_bucket.assets.id

  versioning_configuration {
    status = "Enabled"
  }
}

resource "aws_sqs_queue" "orders" {
  name                       = "${local.name}-orders"
  visibility_timeout_seconds = 60
  message_retention_seconds  = 345600
}

resource "aws_cloudwatch_log_group" "app" {
  name              = "/storefront/${var.environment}/app"
  retention_in_days = 14
}

# Alerting

resource "aws_sns_topic" "alerts" {
  name = "${local.name}-alerts"
}

resource "aws_cloudwatch_metric_alarm" "db_cpu" {
  alarm_name          = "${local.name}-db-cpu"
  namespace           = "AWS/RDS"
  metric_name         = "CPUUtilization"
  statistic           = "Average"
  period              = 300
  evaluation_periods  = 3
  threshold           = 80
  comparison_operator = "GreaterThanThreshold"
  dimensions          = { DBInstanceIdentifier = aws_db_instance.orders.identifier }
  alarm_actions       = [aws_sns_topic.alerts.arn]
}

# DNS

resource "aws_route53_zone" "main" {
  name = "storefront.example.com"
}

resource "aws_route53_record" "www" {
  zone_id = aws_route53_zone.main.zone_id
  name    = "www.storefront.example.com"
  type    = "CNAME"
  ttl     = 60
  records = [aws_lb.web.dns_name]
}

output "url" {
  description = "Public URL of the storefront."
  value       = "https://${aws_route53_record.www.fqdn}"
}

output "db_endpoint" {
  description = "Orders database hostname."
  value       = aws_db_instance.orders.address
}
