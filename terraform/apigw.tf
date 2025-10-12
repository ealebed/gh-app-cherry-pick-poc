# Role that API Gateway will assume to call SQS:SendMessage
resource "aws_iam_role" "apigw_to_sqs" {
  name = "ghapp-poc-apigw-to-sqs"

  assume_role_policy = jsonencode({
    Version = "2012-10-17",
    Statement = [{
      Effect    = "Allow",
      Principal = { Service = "apigateway.amazonaws.com" },
      Action    = "sts:AssumeRole"
    }]
  })
}

resource "aws_iam_role_policy" "apigw_to_sqs" {
  name = "ghapp-poc-apigw-to-sqs-policy"
  role = aws_iam_role.apigw_to_sqs.id

  policy = jsonencode({
    Version = "2012-10-17",
    Statement = [{
      Effect   = "Allow",
      Action   = ["sqs:SendMessage"],
      Resource = aws_sqs_queue.main.arn
    }]
  })
}

# REST API (v1) because SQS service integration is straightforward here
resource "aws_api_gateway_rest_api" "webhook" {
  name        = "ghapp-poc-api"
  description = "GitHub webhook ingress -> SQS"
}

resource "aws_api_gateway_resource" "webhook" {
  rest_api_id = aws_api_gateway_rest_api.webhook.id
  parent_id   = aws_api_gateway_rest_api.webhook.root_resource_id
  path_part   = "webhook"
}

resource "aws_api_gateway_method" "post_webhook" {
  rest_api_id   = aws_api_gateway_rest_api.webhook.id
  resource_id   = aws_api_gateway_resource.webhook.id
  http_method   = "POST"
  authorization = "NONE"
}

# Integration to SQS: transform JSON body into SendMessage payload
resource "aws_api_gateway_integration" "sqs" {
  rest_api_id = aws_api_gateway_rest_api.webhook.id
  resource_id = aws_api_gateway_resource.webhook.id
  http_method = aws_api_gateway_method.post_webhook.http_method

  integration_http_method = "POST"
  type                    = "AWS"
  uri                     = "arn:aws:apigateway:${var.aws_region}:sqs:path/${data.aws_caller_identity.current.account_id}/${aws_sqs_queue.main.name}"
  credentials             = aws_iam_role.apigw_to_sqs.arn

  request_parameters = {
    "integration.request.header.Content-Type" = "'application/x-www-form-urlencoded'"
  }

  # NOTE: body is passed as-is; if GitHub sends JSON, we place it in MessageBody
  request_templates = {
    "application/json" = <<-EOT
      Action=SendMessage&MessageBody=$input.body
    EOT
  }

  passthrough_behavior = "NEVER"
}

# Method response (200)
resource "aws_api_gateway_method_response" "webhook_200" {
  rest_api_id = aws_api_gateway_rest_api.webhook.id
  resource_id = aws_api_gateway_resource.webhook.id
  http_method = aws_api_gateway_method.post_webhook.http_method
  status_code = "200"
}

# Integration response (maps SQS 200 to method 200)
resource "aws_api_gateway_integration_response" "webhook_200" {
  rest_api_id = aws_api_gateway_rest_api.webhook.id
  resource_id = aws_api_gateway_resource.webhook.id
  http_method = aws_api_gateway_method.post_webhook.http_method
  status_code = aws_api_gateway_method_response.webhook_200.status_code

  # Minimal template to satisfy API GW; we don't transform SQS response here
  response_templates = {
    "application/json" = ""
  }

  depends_on = [
    aws_api_gateway_integration.sqs
  ]
}

# Deploy after method + integration + responses are ready
resource "aws_api_gateway_deployment" "webhook" {
  rest_api_id = aws_api_gateway_rest_api.webhook.id

  triggers = {
    # force new deployment when any of these change
    redeploy = sha1(jsonencode({
      method      = aws_api_gateway_method.post_webhook.id
      integration = aws_api_gateway_integration.sqs.id
      meth_resp   = aws_api_gateway_method_response.webhook_200.id
      int_resp    = aws_api_gateway_integration_response.webhook_200.id
    }))
  }

  lifecycle {
    create_before_destroy = true
  }

  depends_on = [
    aws_api_gateway_integration_response.webhook_200
  ]
}

resource "aws_api_gateway_stage" "dev" {
  rest_api_id   = aws_api_gateway_rest_api.webhook.id
  deployment_id = aws_api_gateway_deployment.webhook.id
  stage_name    = "dev"
}
