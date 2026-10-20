#!/usr/bin/env python3
"""Write a large synthetic plan JSON for performance testing.

Usage: scripts/gen-large-plan.py [modules] [resources-per-module] > big.json
"""
import json
import random
import sys

random.seed(7)
modules = int(sys.argv[1]) if len(sys.argv) > 1 else 25
per = int(sys.argv[2]) if len(sys.argv) > 2 else 80
types = ["aws_instance", "aws_security_group", "aws_s3_bucket", "aws_iam_role", "aws_lambda_function",
         "aws_sqs_queue", "aws_cloudwatch_log_group", "aws_route53_record", "aws_subnet", "aws_ecs_service"]
actions = [["no-op"]] * 6 + [["update"]] * 3 + [["create"]] * 2 + [["delete", "create"]] + [["delete"]]

changes, root_calls = [], {}
for m in range(modules):
    mod = f"module.svc{m:02d}"
    resources = []
    for i in range(per):
        typ = types[i % len(types)]
        name = f"r{i:03d}"
        act = random.choice(actions)
        before = None if act == ["create"] else {"name": f"{name}-{m}", "size": i, "tags": {"Team": "a"}}
        after = None if act == ["delete"] else {"name": f"{name}-{m}", "size": i + (1 if act != ["no-op"] else 0), "tags": {"Team": "a"}}
        changes.append({
            "address": f"{mod}.{typ}.{name}", "module_address": mod, "mode": "managed", "type": typ, "name": name,
            "provider_name": "registry.terraform.io/hashicorp/aws",
            "change": {"actions": act, "before": before, "after": after, "after_unknown": {"id": True} if after else {}},
        })
        refs = []
        if i > 0:
            for j in random.sample(range(i), min(i, random.randint(0, 2))):
                refs.append(f"{types[j % len(types)]}.r{j:03d}.id")
        resources.append({"address": f"{typ}.{name}", "mode": "managed", "type": typ, "name": name,
                          "provider_config_key": "aws", "expressions": {"name": {"references": refs}} if refs else {}})
    root_calls[f"svc{m:02d}"] = {"source": f"./modules/svc{m:02d}", "module": {"resources": resources}}

json.dump({
    "format_version": "1.2", "terraform_version": "1.16.6",
    "planned_values": {"root_module": {}}, "resource_changes": changes,
    "configuration": {"provider_config": {"aws": {"name": "aws", "full_name": "registry.terraform.io/hashicorp/aws", "version_constraint": "~> 6.0"}},
                      "root_module": {"module_calls": root_calls}},
}, sys.stdout)
