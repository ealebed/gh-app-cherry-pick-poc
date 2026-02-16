data "aws_caller_identity" "current" {}
data "aws_region" "current" {}
data "aws_availability_zones" "available" {}

data "aws_secretsmanager_secret" "webhook_secret" {
  count  = var.secrets_available ? 1 : 0
  name   = "ghapp/webhook_secret"
}

data "aws_secretsmanager_secret" "github_key_b64" {
  count  = var.secrets_available ? 1 : 0
  name   = "ghapp/private_key_pem_b64"
}

locals {
  webhook_secret_arn    = var.secrets_available ? data.aws_secretsmanager_secret.webhook_secret[0].arn : "arn:aws:secretsmanager:${data.aws_region.current.id}:${data.aws_caller_identity.current.account_id}:secret:ghapp/webhook_secret-placeholder"
  github_key_secret_arn = var.secrets_available ? data.aws_secretsmanager_secret.github_key_b64[0].arn : "arn:aws:secretsmanager:${data.aws_region.current.id}:${data.aws_caller_identity.current.account_id}:secret:ghapp/private_key_pem_b64-placeholder"
  webhook_secret_name   = var.secrets_available ? data.aws_secretsmanager_secret.webhook_secret[0].name : "ghapp/webhook_secret"
}
