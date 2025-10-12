resource "aws_sqs_queue" "dlq" {
  name                      = "ghapp-poc-dlq"
  message_retention_seconds = 1209600 # 14 days
}

resource "aws_sqs_queue" "main" {
  name                      = "ghapp-poc-queue"
  visibility_timeout_seconds = 60
  message_retention_seconds  = 345600  # 4 days
  receive_wait_time_seconds  = 10

  redrive_policy = jsonencode({
    deadLetterTargetArn = aws_sqs_queue.dlq.arn
    maxReceiveCount     = 5
  })
}
