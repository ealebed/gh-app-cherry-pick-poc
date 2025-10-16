# files/github-webhook-validator.py
import os
import hmac
import hashlib
import json
import base64
import boto3

secrets = boto3.client("secretsmanager")
sns = boto3.client("sns")

# Loaded at cold start
_SECRET_VALUE = None

SECRET_NAME = os.environ["GITHUB_WEBHOOK_SECRET_NAME"]
SNS_TOPIC_ARN = os.environ["SNS_TOPIC_ARN"]
MAX_BODY_BYTES = int(os.getenv("MAX_BODY_BYTES", "1048576"))  # 1 MiB default

def _get_secret_value():
    global _SECRET_VALUE
    if _SECRET_VALUE is None:
        resp = secrets.get_secret_value(SecretId=SECRET_NAME)
        # Support both SecretString and binary
        if "SecretString" in resp and resp["SecretString"]:
            _SECRET_VALUE = resp["SecretString"]
        else:
            _SECRET_VALUE = resp["SecretBinary"].decode("utf-8")
    return _SECRET_VALUE

def _safe_log_value(v: str) -> str:
    # Basic sanitization to avoid log injections: strip newlines and control chars
    # Keep it minimal (we only log event/delivery strings).
    return "".join(ch for ch in str(v) if 32 <= ord(ch) < 127)

def lambda_handler(event, context):
    # Accept API Gateway v1/v2 event shapes.
    headers = { (k.lower() if k else ""): (v if isinstance(v, str) else "")
                for k, v in (event.get("headers") or {}).items() }

    signature = headers.get("x-hub-signature-256", "")
    gh_event  = headers.get("x-github-event", "")
    delivery  = headers.get("x-github-delivery", "")

    # Body as raw bytes
    body = event.get("body", "")
    if isinstance(body, str):
        if event.get("isBase64Encoded"):
            try:
                raw = base64.b64decode(body)
            except Exception:
                return {"statusCode": 400, "body": "Invalid base64 body"}
        else:
            raw = body.encode("utf-8")
    elif isinstance(body, (bytes, bytearray)):
        raw = bytes(body)
    else:
        # Unrecognized; normalize via JSON
        raw = json.dumps(body or {}).encode("utf-8")

    if len(raw) > MAX_BODY_BYTES:
        return {"statusCode": 413, "body": "Payload too large"}  # 413 = Request Entity Too Large

    # Validate signature
    secret = _get_secret_value().encode("utf-8")
    expected = "sha256=" + hmac.new(secret, raw, hashlib.sha256).hexdigest()
    if not hmac.compare_digest(signature, expected):
        # Avoid logging user-controlled raw; log only safe values.
        print(f"Signature mismatch for event={_safe_log_value(gh_event)} delivery={_safe_log_value(delivery)}")
        return {"statusCode": 401, "body": "Invalid signature"}

    # Parse JSON body once so downstream gets JSON (not stringified); if it fails, keep as string
    parsed_body = None
    try:
        parsed_body = json.loads(raw.decode("utf-8"))
    except Exception:
        # Keep original raw text
        parsed_body = raw.decode("utf-8", errors="replace")

    # Build normalized message
    message = {
        "validated": True,
        "headers": {
            "X-GitHub-Event": gh_event,
            "X-GitHub-Delivery": delivery,
        },
        "body": parsed_body,
    }

    # Publish to SNS with attributes for routing/metrics. With raw delivery, these attributes
    # are forwarded to SQS as SQS MessageAttributes.
    sns.publish(
        TopicArn=SNS_TOPIC_ARN,
        Message=json.dumps(message, separators=(",", ":"), ensure_ascii=False),
        MessageAttributes={
            "X-GitHub-Event": {
                "DataType": "String",
                "StringValue": gh_event or "",
            },
            "X-GitHub-Delivery": {
                "DataType": "String",
                "StringValue": delivery or "",
            },
        },
    )

    return {"statusCode": 200, "body": "OK"}
