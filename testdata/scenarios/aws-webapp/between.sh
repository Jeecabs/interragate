# Runs after v1 is applied: make changes behind Terraform's back (drift).
set -euo pipefail
"$TF" show -json > state.json
uv run --quiet --python 3.12 --with boto3 python3 - <<'PY'
import json, boto3

state = json.load(open("state.json"))
def walk(module):
    yield from module.get("resources", [])
    for child in module.get("child_modules", []):
        yield from walk(child)
by_addr = {r["address"]: r["values"] for r in walk(state["values"]["root_module"])}

ec2 = boto3.client("ec2")
ec2.authorize_security_group_ingress(
    GroupId=by_addr["aws_security_group.alb"]["id"],
    IpPermissions=[{
        "IpProtocol": "tcp", "FromPort": 8443, "ToPort": 8443,
        "IpRanges": [{"CidrIp": "0.0.0.0/0", "Description": "hotfix: partner callback"}],
    }],
)
boto3.client("elbv2").add_tags(
    ResourceArns=[by_addr["aws_lb.web"]["arn"]],
    Tags=[{"Key": "Owner", "Value": "alice"}],
)
PY
rm -f state.json
