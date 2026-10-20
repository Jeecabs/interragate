---
max_turns: 30
timeout_seconds: 600
allowed_tools: [Read, Glob, Grep, Skill]
---
We use interragate to review Terraform plans. Add a rule to .interragate.yaml in this directory so that any plan replacing or destroying an RDS database instance tagged Environment = production is flagged as critical.
