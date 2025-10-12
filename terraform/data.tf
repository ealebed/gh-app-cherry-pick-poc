data "aws_caller_identity" "current" {}
data "aws_region" "current" {}
data "aws_availability_zones" "available" {}
data "aws_secretsmanager_secret" "webhook_secret" {
  name = "ghapp/webhook_secret"
}
data "aws_secretsmanager_secret" "github_key_b64" {
  name = "ghapp/private_key_pem_b64"
}
