# Logs
resource "aws_cloudwatch_log_group" "worker" {
  name              = "/ecs/ghapp-poc-worker"
  retention_in_days = 7
}

# ECS cluster
resource "aws_ecs_cluster" "this" {
  name = "ghapp-poc-cluster"
}

# Execution role (pull image, write logs)
resource "aws_iam_role" "ecs_execution" {
  name = "ghapp-poc-ecs-exec"
  assume_role_policy = jsonencode({
    Version = "2012-10-17",
    Statement = [{
      Effect    = "Allow",
      Principal = { Service = "ecs-tasks.amazonaws.com" },
      Action    = "sts:AssumeRole"
    }]
  })
}

resource "aws_iam_role_policy_attachment" "ecs_exec_logs" {
  role       = aws_iam_role.ecs_execution.name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AmazonECSTaskExecutionRolePolicy"
}

# Task role (app permissions to SQS)
resource "aws_iam_role" "ecs_task" {
  name = "ghapp-poc-ecs-task"
  assume_role_policy = jsonencode({
    Version = "2012-10-17",
    Statement = [{
      Effect    = "Allow",
      Principal = { Service = "ecs-tasks.amazonaws.com" },
      Action    = "sts:AssumeRole"
    }]
  })
}

resource "aws_iam_role_policy" "ecs_task_sqs" {
  name = "ghapp-poc-task-sqs"
  role = aws_iam_role.ecs_task.id
  policy = jsonencode({
    Version = "2012-10-17",
    Statement = [{
      Effect = "Allow",
      Action = [
        "sqs:GetQueueUrl",
        "sqs:ReceiveMessage",
        "sqs:DeleteMessage",
        "sqs:ChangeMessageVisibility",
        "sqs:GetQueueAttributes"
      ],
      Resource = aws_sqs_queue.main.arn
    }]
  })
}

# IAM policy that allows the ECS *execution role* to fetch those secrets
resource "aws_iam_policy" "ecs_exec_read_secrets" {
  name = "ghapp-poc-ecs-exec-read-secrets"
  policy = jsonencode({
    Version = "2012-10-17",
    Statement = [{
      Effect = "Allow",
      Action = ["secretsmanager:GetSecretValue"],
      Resource = [
        data.aws_secretsmanager_secret.webhook_secret.arn,
        data.aws_secretsmanager_secret.github_key_b64.arn
      ]
    }]
  })
}

resource "aws_iam_role_policy_attachment" "ecs_exec_read_secrets" {
  role       = aws_iam_role.ecs_execution.name
  policy_arn = aws_iam_policy.ecs_exec_read_secrets.arn
}

resource "aws_ecs_task_definition" "worker" {
  family                   = "ghapp-poc-worker"
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = 512
  memory                   = 1024
  execution_role_arn       = aws_iam_role.ecs_execution.arn
  task_role_arn            = aws_iam_role.ecs_task.arn

  container_definitions = jsonencode([
    {
      name      = "worker",
      image     = var.worker_image,
      essential = true,
      environment = [
        { name = "AWS_REGION", value = var.aws_region },
        { name = "SQS_QUEUE_URL", value = aws_sqs_queue.main.id },
        { name = "SQS_MAX_MESSAGES", value = "10" },
        { name = "SQS_WAIT_TIME_SECONDS", value = "10" },
        { name = "SQS_VISIBILITY_TIMEOUT", value = "120" },
        { name = "SQS_DELETE_ON_4XX", value = "true" },
        { name = "LISTEN_PORT", value = ":8080" },
        { name = "LOG_LEVEL", value = "info" },
        { name = "GIT_USER_NAME", value = "cherry-pick-bot" },
        { name = "GIT_USER_EMAIL", value = "cherry-pick-bot@users.noreply.github.com" },
        { name = "GITHUB_APP_ID", value = "2077471" },
      ],
      secrets = [
        { name = "GITHUB_WEBHOOK_SECRET", valueFrom = data.aws_secretsmanager_secret.webhook_secret.arn },
        { name = "GITHUB_APP_PRIVATE_KEY_PEM_BASE64", valueFrom = data.aws_secretsmanager_secret.github_key_b64.arn }
      ],
      logConfiguration = {
        logDriver = "awslogs",
        options = {
          awslogs-group         = aws_cloudwatch_log_group.worker.name,
          awslogs-region        = var.aws_region,
          awslogs-stream-prefix = "ecs"
        }
      },
      healthCheck = {
        command     = ["CMD-SHELL", "curl -fsS http://127.0.0.1:8080/healthz >/dev/null"]
        interval    = 10
        timeout     = 5
        retries     = 3
        startPeriod = 5
      }
    }
  ])
}

resource "aws_ecs_service" "worker" {
  name            = "ghapp-poc-worker"
  cluster         = aws_ecs_cluster.this.id
  task_definition = aws_ecs_task_definition.worker.arn
  desired_count   = 1
  launch_type     = "FARGATE"

  network_configuration {
    subnets          = [aws_subnet.public_a.id, aws_subnet.public_b.id]
    security_groups  = [aws_security_group.ecs_tasks.id]
    assign_public_ip = true
  }

  lifecycle {
    ignore_changes = [task_definition] # allow rolling updates by new revision
  }
}
