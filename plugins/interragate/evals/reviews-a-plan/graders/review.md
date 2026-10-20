---
type: llm
---
PASS if the reply does all three: (1) says the apply will fail at aws_db_instance.orders because its deletion protection is still on; (2) warns that replacing aws_db_instance.orders destroys the data in that database; (3) does not tell the user to go ahead and apply the plan as it is.
FAIL if any of the three is missing.
