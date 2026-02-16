output "account_id" {
  value = data.aws_caller_identity.current.account_id
}

output "caller_arn" {
  value = data.aws_caller_identity.current.arn
}

output "active_region" {
  value = data.aws_region.current.id
}

output "sqs_queue_url" {
  value       = aws_sqs_queue.main.id
  description = "Main queue URL (the worker reads this)"
}

output "dlq_queue_url" {
  value = aws_sqs_queue.dlq.id
}

output "webhook_url" {
  value       = "https://${aws_api_gateway_rest_api.webhook.id}.execute-api.${var.aws_region}.amazonaws.com/${aws_api_gateway_stage.dev.stage_name}/webhook"
  description = "Use this as the GitHub Webhook URL"
}
