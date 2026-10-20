# What interragate tells you

`interragate demo` prints this for its sample plan, before the list of every change and finding:

```text
Critical risk. Destroys an S3 bucket, replaces an RDS database instance and opens SSH to the
internet.

+3 create   ~23 update   -/+5 replace   -5 destroy     13 cosmetic · 2 drifted · 2 output changes

What matters
  ● Destroying aws_s3_bucket.logs (an S3 bucket) deletes every object in the bucket.
  ● Replacing aws_db_instance.orders (an RDS database instance) destroys it, along with all data in
    the database, and creates a new, empty one.
  ● Opens SSH (22/tcp) to the internet on aws_security_group.app.
  ● The apply will fail when it tries to replace aws_db_instance.orders: deletion_protection is
    still on for the existing RDS database instance.
  ● Adds a statement ("DebugEverything") to policy on aws_iam_role_policy.app that allows s3:* on
    every resource.
  ● Turns off block_public_policy and restrict_public_buckets on
    aws_s3_bucket_public_access_block.assets, so a bucket policy or ACL can make the bucket public.
  ● Removing 1 item from the middle of the list behind count shifts
    module.network.aws_subnet.private and module.network.aws_subnet.public the same way: [1] is
    changed to become what [2] was, and [2] is destroyed.
  ● aws_instance.web is destroyed and aws_instance.app is created with the same settings.
  ● aws_security_group.alb was changed outside Terraform (ingress gained 8443/tcp from 0.0.0.0/0
    ("hotfix: partner callback")), and this plan reverts it.
  ● Sets skip_final_snapshot = true on aws_db_instance.orders: if it is ever destroyed, no final
    snapshot is taken.
  · Your edits since main touch 32 resources; 3 more change because of them and 1 other change is
    not tied to an edit.
  · This plan changes module.network.aws_subnet.private[2] and 5 more, which CODEOWNERS assigns to
    @acme/network, who owns none of the files you edited, so will not be asked to review it.
  · tags_all.Team changes "platform" → "commerce" on 19 resources (provider default_tags).
  · 13 updates only change tags or formatting.
  · 5 changes only happen because something they depend on changes.
  · Creates a CloudWatch alarm, an EC2 instance and an SNS topic.
  ● Plus 3 minor findings.

When you apply
  ● The apply should fail at aws_db_instance.orders, where deletion protection is on. The 22
    resources that wait for it are skipped, which leaves module.network.aws_subnet.public[1],
    module.network.aws_route_table_association.public[1] and aws_lb_target_group_attachment.web
    destroyed with nothing in their place.
  · Once that is fixed, the apply should take about 16 minutes, most of it replacing
    aws_db_instance.orders.
  ● aws_db_instance.orders will not exist for about 15 minutes while aws_ssm_parameter.db_endpoint
    depends on it.
  ● If any of the 16 steps that recreating aws_db_instance.orders waits for fails, it is left
    destroyed with nothing in its place.
```

- **Why each resource changes.** Terraform's reasons are spelled out: "Changing `kms_key_id` and `storage_encrypted` cannot be done in place", tainted, `-replace`, `replace_triggered_by` (naming the trigger), a `count` or `for_each` key going away.
- **What a change follows from.** interragate traces values that become unknown back through references, module inputs and outputs, and locals: "Only changes because `aws_db_instance.orders` is replaced". You see the cause instead of 20 confusing ripples, and, on a branch, the edit the chain starts with: "It starts with your edit to `var.azs`, in `main.tf`, through `module.network.aws_subnet.private[1]`."
- **The edit behind each change**, on a branch: see [Your branch](#your-branch) below.
- **Noise folded away.** Tag-only updates, null-versus-empty flips and reformatted JSON are marked cosmetic. Repeated changes are counted once: "`tags_all.Team` changes on 19 resources (provider `default_tags`)".
- **Drift that matters.** Real changes made outside Terraform are separated from provider noise (re-attributed default tags, timestamps, computed attributes). interragate says whether the plan keeps or reverts each one.
- **How the apply unfolds.** interragate walks the changes in the order Terraform's apply graph does, ten at a time, with typical times for slow resources such as databases and clusters. It says roughly how long the apply takes and what most of that is, and how long a resource replaced destroy-first will not exist while others depend on it. When a rule expects the apply to fail, it says where, what is skipped because of it, and which replacements are left destroyed with nothing in their place. The JSON report has every step.
- **Deploys.** A change that ships a new version says so, in the list and the summary: "deploys api 1.3.2 → 1.4.0" for an ECS task definition rather than "container_definitions forces replacement", and likewise for Kubernetes workloads, Cloud Run, container apps, Helm charts and Lambda code.
- **Access in words.** A change to an IAM policy, a role's trust policy or a firewall says what it allows that it did not, and what it no longer allows: "+ Inbound TCP 22 (SSH) from `0.0.0.0/0` (anywhere)", "+ Lets account `111122223333` use `s3:GetObject` on `arn:aws:s3:::data/*` when `aws:SourceIp` is in `10.0.0.0/8`", "− Lets `ec2.amazonaws.com` assume the role". Reordered or split statements allow nothing new, so they say nothing; a security group's rule set shows the one path that changed rather than every rule removed and added again. AWS security groups and their rule resources, Google Cloud firewalls and Azure network security rules are covered. The pull request comment lists the same, open when something lets anyone in.
- **Diffs you can read.** JSON held in strings, like IAM policies and task definitions, is diffed as structure. Multi-line strings are diffed by line. A rule inserted into a list doesn't show every later rule as changed. Replace diffs lead with the attributes you changed and fold the 40 that are merely "known after apply". Sensitive values are masked everywhere.

## Your branch

In a git repository, interragate compares the configuration with where your branch started (on the default branch, with the last commit) and ties each change to the edit behind it: lines in its own block, the `module` call above it, a variable or local it reads (through module arguments and a module's `count` or `for_each`, and in the `.tfvars` file the plan used), or, for tags alone, the provider block's `default_tags`. Then it says how much of the plan your edits account for:

```text
· Your edits since main touch 32 resources; 3 more change because of them and 1 other change is not tied to an edit.
· The aws provider moves from 5.62.0 to 6.0.0 since main, which may explain the 7 changes to its resources that are not tied to an edit.
```

The part nobody edited is what a reviewer most needs to see: drift, someone else's change, a provider upgrade. `untied-destroy` flags a destroy or replacement no edit explains. In the report, the overview says how much is yours, the map's **Edits** mode (`e`) outlines your edits and fades everything else, the change list groups by origin, and each change shows the lines under "Your edit"; `interragate explain` shows them too. Lines that hold a secret are masked for what they are, not only for how they read: an argument the plan marks sensitive, a variable declared `sensitive` and the `.tfvars` lines that set it, and anything written out under a name like `password` or `token`. The pull request comment lists the changes not tied to an edit apart from yours, first. `--base REF` compares with another commit, `--base none` turns it off, and policies can match on `origin`.

## Who should know

When the repository has a CODEOWNERS file (`.github/`, the top or `docs/`), each change carries the owners of its block, and interragate names the owners your branch reaches without asking: "This plan changes `module.network.aws_subnet.private[2]` and 5 more, which CODEOWNERS assigns to @acme/network, who owns none of the files you edited, so will not be asked to review it." GitHub asks the owners of the files a pull request edits; a plan reaches further than that, through module calls, variables and dependencies. `interragate demo` shows it.

## Scanners you already run

`--scan trivy` (or `checkov`, `tflint`, several separated by commas) runs the scanner on the configuration and keeps what it reports on resources this plan creates or replaces, or on lines you edited. A scan that finds 140 issues across a repository becomes the three your change introduces; the rest are counted in one sentence ("trivy reports 24 issues: 3 on what this plan creates, replaces or edits, and 21 that were there before"). Scanner findings are rules named after the scanner, so `disable`, `severity`, `ignore` and `--fail-on` apply to them. `scan: [trivy]` in `.interragate.yaml` runs it every time.
