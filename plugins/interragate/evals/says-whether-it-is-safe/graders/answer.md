---
type: llm
---
PASS if the reply says the plan is not safe to apply as it is, and gives at least two concrete reasons from the plan, such as: replacing the RDS database aws_db_instance.orders destroys its data; the apply will fail because deletion protection is on; the S3 bucket aws_s3_bucket.logs is destroyed; SSH is opened to the internet.
FAIL if it says the plan is safe, or gives fewer than two concrete reasons.
