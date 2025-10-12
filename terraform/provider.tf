provider "aws" {
  region = var.aws_region

  # Authenticating via environment variables:
  #   AWS_ACCESS_KEY_ID
  #   AWS_SECRET_ACCESS_KEY
  #   AWS_REGION (optional; we set via var above)
  #
  # No profile is used; no backend is configured (local state).

  default_tags {
    tags = var.common_tags
  }
}
