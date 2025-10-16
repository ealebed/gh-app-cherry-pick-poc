resource "aws_sns_topic" "ghapp" {
  name = "ghapp-poc-topic"
}

# Subscribe SQS to SNS; enable raw message delivery (payload is exactly what Lambda publishes).
resource "aws_sns_topic_subscription" "sqs_sub" {
  topic_arn            = aws_sns_topic.ghapp.arn
  protocol             = "sqs"
  endpoint             = aws_sqs_queue.main.arn
  raw_message_delivery = true

  # Allow SNS to publish to the SQS queue (queue policy)
  depends_on = [aws_sqs_queue_policy.allow_sns_to_sqs]
}

# SQS queue policy that allows SNS topic to send messages
resource "aws_sqs_queue_policy" "allow_sns_to_sqs" {
  queue_url = aws_sqs_queue.main.id
  policy = jsonencode({
    Version = "2012-10-17",
    Statement = [
      {
        Effect    = "Allow",
        Principal = { Service = "sns.amazonaws.com" },
        Action    = "sqs:SendMessage",
        Resource  = aws_sqs_queue.main.arn,
        Condition = {
          ArnEquals = {
            "aws:SourceArn" : aws_sns_topic.ghapp.arn
          }
        }
      }
    ]
  })
}
