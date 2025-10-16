resource "aws_api_gateway_rest_api" "webhook" {
  name        = "ghapp-poc-api"
  description = "GitHub webhook ingress -> Lambda validator -> SNS -> SQS"
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

resource "aws_lambda_permission" "apigw_invoke" {
  statement_id  = "AllowAPIGwInvoke"
  action        = "lambda:InvokeFunction"
  function_name = aws_lambda_function.webhook_validator.function_name
  principal     = "apigateway.amazonaws.com"
  # Allow any stage/method on this API for this resource
  source_arn    = "${aws_api_gateway_rest_api.webhook.execution_arn}/*/*${aws_api_gateway_resource.webhook.path}"
}

resource "aws_api_gateway_integration" "lambda_proxy" {
  rest_api_id = aws_api_gateway_rest_api.webhook.id
  resource_id = aws_api_gateway_resource.webhook.id
  http_method = aws_api_gateway_method.post_webhook.http_method

  integration_http_method = "POST"
  type                    = "AWS_PROXY"
  uri                     = aws_lambda_function.webhook_validator.invoke_arn
}

resource "aws_api_gateway_method_response" "webhook_200" {
  rest_api_id = aws_api_gateway_rest_api.webhook.id
  resource_id = aws_api_gateway_resource.webhook.id
  http_method = aws_api_gateway_method.post_webhook.http_method
  status_code = "200"
}

resource "aws_api_gateway_deployment" "webhook" {
  rest_api_id = aws_api_gateway_rest_api.webhook.id

  # Re-deploy on changes
  triggers = {
    redeploy = sha1(jsonencode({
      rest_api_id = aws_api_gateway_rest_api.webhook.id
      resource_id = aws_api_gateway_resource.webhook.id
      method_id   = aws_api_gateway_method.post_webhook.id
      integ_id    = aws_api_gateway_integration.lambda_proxy.id
      method_200  = aws_api_gateway_method_response.webhook_200.id
    }))
  }

  lifecycle {
    create_before_destroy = true
  }

  depends_on = [
    aws_api_gateway_integration.lambda_proxy
  ]
}

resource "aws_api_gateway_stage" "dev" {
  rest_api_id   = aws_api_gateway_rest_api.webhook.id
  deployment_id = aws_api_gateway_deployment.webhook.id
  stage_name    = "dev"
}
