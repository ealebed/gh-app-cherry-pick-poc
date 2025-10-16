# Package the Python lambda from a local file
data "archive_file" "webhook_validator_zip" {
  type        = "zip"
  source_file = "${path.module}/files/github-webhook-validator.py"
  output_path = "${path.module}/files/github-webhook-validator.zip"
}

resource "aws_iam_role" "lambda_exec" {
  name = "ghapp-poc-lambda-exec"
  assume_role_policy = jsonencode({
    Version = "2012-10-17",
    Statement = [{
      Effect    = "Allow",
      Principal = { Service = "lambda.amazonaws.com" },
      Action    = "sts:AssumeRole"
    }]
  })
}

# Basic logging
resource "aws_iam_role_policy_attachment" "lambda_logs" {
  role       = aws_iam_role.lambda_exec.name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole"
}

# Allow Lambda to read the webhook secret and publish to SNS
resource "aws_iam_policy" "lambda_extra" {
  name = "ghapp-poc-lambda-extra"
  policy = jsonencode({
    Version = "2012-10-17",
    Statement = [
      {
        Effect   = "Allow",
        Action   = ["secretsmanager:GetSecretValue"],
        Resource = data.aws_secretsmanager_secret.webhook_secret.arn
      },
      {
        Effect   = "Allow",
        Action   = ["sns:Publish"],
        Resource = aws_sns_topic.ghapp.arn
      }
    ]
  })
}

resource "aws_iam_role_policy_attachment" "lambda_extra_attach" {
  role       = aws_iam_role.lambda_exec.name
  policy_arn = aws_iam_policy.lambda_extra.arn
}

resource "aws_lambda_function" "webhook_validator" {
  function_name = "ghapp-poc-webhook-validator"
  role          = aws_iam_role.lambda_exec.arn
  handler       = "github-webhook-validator.lambda_handler"
  runtime       = "python3.12"
  timeout       = 10

  filename         = data.archive_file.webhook_validator_zip.output_path
  source_code_hash = data.archive_file.webhook_validator_zip.output_base64sha256

  environment {
    variables = {
      # We'll pass the *name* of the secret, Lambda fetches it at runtime (kept warm across invocations).
      GITHUB_WEBHOOK_SECRET_NAME = data.aws_secretsmanager_secret.webhook_secret.name
      SNS_TOPIC_ARN              = aws_sns_topic.ghapp.arn
      # Optional: limit inbound body size (defense-in-depth)
      MAX_BODY_BYTES             = "1048576"
    }
  }
}
