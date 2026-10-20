# Rules

interragate's rules look at what a plan does, not only at configuration, so they flag what a change introduces.
Disable one with `disable: [rule-id]` in `.interragate.yaml`, or change its severity with `severity: {rule-id: low}`.
Add rules of your own under `policies:` in the same file, as [Your own rules](https://github.com/Jeecabs/interragate/blob/main/docs/configuration.md#your-own-rules) describes; `interragate rules` lists them after these.

| Rule | Default | Category |
|---|---|---|
| [`deletion-protection-disabled`](#deletion-protection-disabled) | medium | data-loss |
| [`final-snapshot-disabled`](#final-snapshot-disabled) | medium | data-loss |
| [`force-destroy-enabled`](#force-destroy-enabled) | medium | data-loss |
| [`mass-destroy`](#mass-destroy) | high | data-loss |
| [`stateful-destroy`](#stateful-destroy) | critical | data-loss |
| [`stateful-replace`](#stateful-replace) | critical | data-loss |
| [`check-failed`](#check-failed) | high | correctness |
| [`eks-version-skip`](#eks-version-skip) | high | correctness |
| [`engine-downgrade`](#engine-downgrade) | high | correctness |
| [`import-replace`](#import-replace) | high | correctness |
| [`major-upgrade-not-allowed`](#major-upgrade-not-allowed) | high | correctness |
| [`name-taken`](#name-taken) | high | correctness |
| [`plan-errored`](#plan-errored) | critical | correctness |
| [`plan-incomplete`](#plan-incomplete) | medium | correctness |
| [`protected-delete`](#protected-delete) | high | correctness |
| [`replace-in-use`](#replace-in-use) | high | correctness |
| [`role-delete-conflict`](#role-delete-conflict) | high | correctness |
| [`rules-fight`](#rules-fight) | medium | correctness |
| [`storage-shrink`](#storage-shrink) | high | correctness |
| [`untied-destroy`](#untied-destroy) | medium | correctness |
| [`admin-access`](#admin-access) | high | security |
| [`cross-account-access`](#cross-account-access) | medium | security |
| [`encryption-disabled`](#encryption-disabled) | medium | security |
| [`iam-wildcard`](#iam-wildcard) | high | security |
| [`plaintext-parameter`](#plaintext-parameter) | medium | security |
| [`public-bucket`](#public-bucket) | high | security |
| [`public-database`](#public-database) | high | security |
| [`public-ingress`](#public-ingress) | high | security |
| [`public-principal`](#public-principal) | high | security |
| [`runs-command`](#runs-command) | low | security |
| [`secret-output`](#secret-output) | medium | security |
| [`disruptive-update`](#disruptive-update) | medium | availability |
| [`dns-change`](#dns-change) | medium | availability |
| [`downtime-replace`](#downtime-replace) | medium | availability |
| [`large-blast-radius`](#large-blast-radius) | medium | availability |
| [`network-disruption`](#network-disruption) | high | availability |
| [`no-rollout`](#no-rollout) | low | availability |
| [`role-recreated`](#role-recreated) | high | availability |
| [`consumer-loses`](#consumer-loses) | medium | consumers |
| [`consumer-outage`](#consumer-outage) | medium | consumers |
| [`consumer-reach`](#consumer-reach) | medium | consumers |
| [`consumer-value`](#consumer-value) | medium | consumers |
| [`count-index-shift`](#count-index-shift) | medium | refactoring |
| [`missing-moved-block`](#missing-moved-block) | medium | refactoring |
| [`drift-deleted`](#drift-deleted) | medium | drift |
| [`drift-revert`](#drift-revert) | medium | drift |
| [`deprecated-runtime`](#deprecated-runtime) | medium | hygiene |
| [`module-unpinned`](#module-unpinned) | low | hygiene |
| [`output-removed`](#output-removed) | low | hygiene |
| [`provider-unpinned`](#provider-unpinned) | low | hygiene |
| [`required-tags`](#required-tags) | medium | hygiene |
| [`checkov`](#checkov) | medium | scanner |
| [`tflint`](#tflint) | medium | scanner |
| [`trivy`](#trivy) | medium | scanner |

## deletion-protection-disabled

**Deletion protection turned off** · default severity medium · data-loss

Flags turning deletion protection off, which is often the first step before an accidental destroy.

## final-snapshot-disabled

**Final snapshot disabled** · default severity medium · data-loss

Flags setting skip_final_snapshot = true, which means a future destroy deletes the data with no backup.

## force-destroy-enabled

**force_destroy turned on** · default severity medium · data-loss

Flags turning on force_destroy (or force_delete), which lets a destroy delete every object or image inside instead of failing.

## mass-destroy

**Destroys many resources** · default severity high · data-loss

Flags plans that destroy many resources at once (20 by default, or half of everything managed). This is how a wrong workspace, a missing variable file or a refactor without moved blocks usually shows up. Configure with rules.mass-destroy.threshold.

## stateful-destroy

**Destroys a resource that holds data** · default severity critical · data-loss

Flags destroying databases, buckets, volumes, keys, queues, DNS zones and other resources whose contents cannot be recreated from configuration. The message says what is lost and whether a final snapshot, recovery window or force_destroy changes the picture.

## stateful-replace

**Replaces a resource that holds data** · default severity critical · data-loss

Flags replacing a resource that holds data. Replacement destroys the existing object and creates a new, empty one, which is easy to miss in a long plan because it reads like an update.

## check-failed

**A check or condition failed** · default severity high · correctness

Flags check blocks, preconditions and postconditions that failed or errored during planning.

## eks-version-skip

**Apply will fail: EKS upgrades one version at a time** · default severity high · correctness

Flags an EKS cluster version change that skips a Kubernetes minor version or goes back one. EKS upgrades the control plane one minor version at a time and never downgrades it.

## engine-downgrade

**Apply will fail: the engine version goes down** · default severity high · correctness

Flags an in-place change to an older database or cache engine version, which the service refuses. It usually means the service upgraded the minor version itself (RDS and ElastiCache do, with automatic minor version upgrades) and the configuration still pins the old one.

## import-replace

**Imports a resource and immediately replaces it** · default severity high · correctness

Flags an import block whose configuration differs from the real object in an attribute that cannot change in place, so the plan destroys the very object being imported.

## major-upgrade-not-allowed

**Apply will fail: a major upgrade is not allowed** · default severity high · correctness

Flags an RDS or Aurora engine upgrade to a new major version without allow_major_version_upgrade, which RDS rejects.

## name-taken

**Apply will fail: the name is taken** · default severity high · correctness

Flags a replacement whose new object keeps a name that must be unique. With create_before_destroy the old object still holds the name when the new one is created; some services (Secrets Manager, Cloud SQL) keep a deleted object's name for a while, so even destroying first does not free it.

## plan-errored

**The plan failed** · default severity critical · correctness

Flags plans that errored. They are incomplete and cannot be applied.

## plan-incomplete

**The plan is incomplete** · default severity medium · correctness

Flags plans with deferred changes, where another plan and apply is needed after this one.

## protected-delete

**Apply will fail: deletion protection is on** · default severity high · correctness

Flags destroying or replacing an object that still has deletion protection turned on. Terraform destroys the old object with its current settings, so turning protection off in the same plan does not help: the apply fails partway.

## replace-in-use

**Apply will fail: it is deleted while still in use** · default severity high · correctness

Flags replacing a security group, target group, certificate or database subnet or parameter group by deleting it first, while resources the plan keeps still use it. They only switch to the new one once it exists, and AWS refuses to delete one in use.

## role-delete-conflict

**Apply will fail: the role still has policies attached** · default severity high · correctness

Flags replacing an IAM role under the same name while policy attachments or inline policies the plan leaves alone still name it. The replacement deletes the role first, and AWS refuses to delete a role with policies attached unless force_detach_policies is set.

## rules-fight

**Inline rules and rule resources undo each other** · default severity medium · correctness

Flags a security group or route table whose plan removes an inline rule that a separate rule resource adds (aws_security_group_rule, aws_vpc_security_group_ingress_rule, aws_route). The two take turns: each apply puts one back and the next plan takes it out again, so the plan never comes up clean.

## storage-shrink

**Apply will fail: storage cannot shrink** · default severity high · correctness

Flags an in-place update that makes a database's, volume's, broker's or file system's storage smaller. The service refuses: storage only grows, and smaller storage means a new resource and moving the data.

## untied-destroy

**Destroys something no edit explains** · default severity medium · correctness

When interragate compares the configuration with git, flags a destroy or replacement that none of the edits explain: nothing in the resource's block, its module call or what it reads changed, and it does not follow from a change that did. It may come from a variable or .tfvars change, a provider upgrade or a change on the base; either way it is worth knowing before the apply. Only plans whose configuration files changed are checked, and not destroy plans, which -destroy explains.

## admin-access

**Grants administrator access** · default severity high · security

Flags attaching AdministratorAccess, granting roles/owner or roles/editor, Owner role assignments and cluster-admin bindings.

## cross-account-access

**Grants another AWS account access** · default severity medium · security

Flags policy statements this plan adds that name an AWS account other than the ones the configuration's resources live in: a role another account can assume, or a bucket, key, queue or topic it can use. Sharing across accounts is sometimes the point and sometimes a leak, so a new one deserves a look. Accounts that already had access are not flagged again.

## encryption-disabled

**Stores data unencrypted** · default severity medium · security

Flags new or changed storage with encryption at rest turned off.

## iam-wildcard

**Grants wildcard permissions** · default severity high · security

Reads IAM policy documents, including JSON held in strings, and flags statements this plan adds that allow every action ("*"), every action of a service ("s3:*") on every resource, NotAction, or iam:PassRole on any role.

## plaintext-parameter

**Secret stored as plain text** · default severity medium · security

Flags SSM parameters whose name suggests a secret but whose type is String rather than SecureString.

## public-bucket

**Makes storage public** · default severity high · security

Flags turning off S3 public access blocks, public ACLs, allUsers bindings on buckets and public blob containers.

## public-database

**Exposes a database publicly** · default severity high · security

Flags databases made publicly accessible and database firewall rules that allow every address.

## public-ingress

**Opens a port to the internet** · default severity high · security

Flags security group, firewall and network security rules that this plan adds or widens to accept traffic from anywhere (0.0.0.0/0 or ::/0). Remote access and database ports are critical; HTTP and HTTPS are not reported.

## public-principal

**Lets anyone use a resource** · default severity high · security

Flags policy statements this plan adds that allow Principal "*" without a Condition: a public bucket, queue, topic or key, or a role any AWS account can assume.

## runs-command

**Runs a command during the apply** · default severity low · security

Flags a change that runs a provisioner: local-exec runs a command on the machine that applies the plan, remote-exec on the new host, and file copies files to it. A plan shows none of this, so the command is worth reading before the apply. Creating a resource runs its creation-time provisioners; destroying it runs its destroy-time ones.

## secret-output

**Secret-looking output is not sensitive** · default severity medium · security

Flags root outputs whose name suggests a secret but which are not marked sensitive, so the value is printed in plan and apply output.

## disruptive-update

**In-place update still causes downtime** · default severity medium · availability

Flags in-place updates that still restart or resize something: instance types, database classes and engine versions, cluster versions and similar. They read like harmless updates in a plan.

## dns-change

**Changes DNS** · default severity medium · availability

Flags DNS records whose target changes or that are destroyed. Resolvers keep old answers for the TTL, so mistakes linger.

## downtime-replace

**Replacement leaves a gap** · default severity medium · availability

Flags replacements that destroy the old object before creating the new one, for resources that serve traffic. create_before_destroy avoids the gap.

## large-blast-radius

**Many resources depend on what is replaced** · default severity medium · availability

Flags destroying or replacing a resource that many others depend on, directly or through modules. Configure with rules.large-blast-radius.threshold (default 10).

## network-disruption

**Destroys or replaces core networking** · default severity high · availability

Flags destroying or replacing VPCs, subnets, route tables, gateways, load balancers, listeners and CDN distributions, which interrupts everything that sends traffic through them.

## no-rollout

**The new launch template does not reach running instances** · default severity low · availability

Flags a launch template change that an Auto Scaling group using it will not roll out. Without an instance_refresh block, only instances launched later get the new template; and a group that launches $Default or a fixed version does not get it at all.

## role-recreated

**Recreating a role breaks what refers to it** · default severity high · availability

Flags replacing an IAM role while resources the plan leaves alone keep referring to the old one: instance profiles, which AWS empties when it deletes the role; policies the provider detaches and nothing attaches again; and policies that trust the role by ARN, which AWS ties to the old role's unique ID.

## consumer-loses

**Destroys what a service outside Terraform uses** · default severity medium · consumers

Flags a resource, output or host that .interragate.yaml says a service outside Terraform uses, such as a site on Vercel or an app on phones, when this plan destroys or removes it, or stops answering for the host.

## consumer-outage

**Takes away what a service outside Terraform relies on during the apply** · default severity medium · consumers

Flags a stretch of the apply in which something a service outside Terraform relies on is missing: a resource it uses while it is replaced, or the targets of the load balancer behind a host it calls. It follows interragate's simulation of the apply, including where it is expected to fail.

## consumer-reach

**Closes a way in a service outside Terraform uses** · default severity medium · consumers

Flags a firewall that .interragate.yaml says a service outside Terraform comes in through, such as a partner's callbacks on a port, when this plan stops letting it in. It reads AWS security groups and their rules and Google Cloud firewalls.

## consumer-value

**Changes a value a service outside Terraform holds** · default severity medium · consumers

Flags a value that .interragate.yaml says a service outside Terraform holds, such as an endpoint in an environment variable, when this plan changes it or leaves it known only after apply. How soon the service can take the new value depends on where it runs: a site takes it on its next deployment, while copies of an app already on phones keep the old one until people update.

## count-index-shift

**Removing a list item shifts count indexes** · default severity medium · refactoring

Detects the classic count pitfall: removing an item from the middle of a list shifts every later instance down one index, so they are updated or replaced to become their neighbour and the last one is destroyed. for_each keyed by a stable value avoids it.

## missing-moved-block

**Looks like a rename without a moved block** · default severity medium · refactoring

Pairs each destroyed resource with a created resource of the same type and near-identical settings. That is almost always a rename or a move into a module, and a moved block keeps the real object instead of destroying and recreating it. Whole modules that moved are suggested as one moved block.

## drift-deleted

**Recreates a resource deleted outside Terraform** · default severity medium · drift

Flags resources that were deleted outside Terraform and that this plan creates again, new and empty.

## drift-revert

**Reverts a change made outside Terraform** · default severity medium · drift

Compares the state Terraform had with what it found on refresh and flags differences this plan will undo, such as a firewall rule or tag added by hand. That is often an emergency fix nobody wrote back into the code. Provider noise (default tag re-attribution, timestamps, null versus empty) is ignored.

## deprecated-runtime

**Lambda runtime is deprecated** · default severity medium · hygiene

Flags a Lambda function created or updated on a runtime AWS has deprecated. AWS no longer patches a deprecated runtime, and after its deprecation date it blocks creating functions on it and then updating them, so applies start to fail.

## module-unpinned

**Module source is not pinned** · default severity low · hygiene

Flags registry modules without a version constraint and Git modules without a ref, which change under you.

## output-removed

**Removes an output** · default severity low · hygiene

Flags removed root outputs. Anything reading them through terraform_remote_state, tfe_outputs or scripts breaks.

## provider-unpinned

**Provider has no version constraint** · default severity low · hygiene

Flags providers without a version constraint in required_providers, so a new major version can arrive on the next init.

## required-tags

**Missing required tags** · default severity medium · hygiene

When required_tags is set in .interragate.yaml, flags created or updated resources that will not carry every required tag.

## checkov

**Flagged by checkov** · default severity medium · scanner

Shows what checkov reports on resources this plan creates or replaces, or on lines you edited, when interragate runs it (--scan checkov). Issues on what the plan leaves as it was are counted, not listed.

## tflint

**Flagged by tflint** · default severity medium · scanner

Shows what tflint reports on resources this plan creates or replaces, or on lines you edited, when interragate runs it (--scan tflint). Issues on what the plan leaves as it was are counted, not listed.

## trivy

**Flagged by trivy** · default severity medium · scanner

Shows what trivy reports on resources this plan creates or replaces, or on lines you edited, when interragate runs it (--scan trivy). Issues on what the plan leaves as it was are counted, not listed.
