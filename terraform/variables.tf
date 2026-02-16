variable "aws_region" {
  description = "AWS region to deploy into"
  type        = string
  default     = "eu-north-1"
}

variable "common_tags" {
  description = "Tags applied to all resources"
  type        = map(string)
  default = {
    project = "ghapp-poc"
    env     = "dev"
    owner   = "ealebed"
  }
}

variable "worker_image" {
  description = "Container image for the worker (e.g. public.ecr.aws/xyz/ghapp:latest)"
  type        = string
  default     = "docker.io/ealebed/cherrypicker:2025.11.03-08.06"
}

variable "secrets_available" {
  description = "Set to true when ghapp/webhook_secret and ghapp/private_key_pem_b64 exist in AWS Secrets Manager. When false, placeholder ARNs are used (plan succeeds, apply will fail)."
  type        = bool
  default     = false
}
