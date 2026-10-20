# .interragate.yaml

interragate reads the nearest `.interragate.yaml`, or the file `--config` names. `interragate init` writes a starter one, with a first policy for what holds data. After any edit, run `interragate rules`: it exits 1 and names the problem when the file is invalid, and otherwise lists the team's policies after interragate's own rules.

## Contents

- Settings for the built-in rules
- Policies: the team's own rules
- match
- when

## Settings for the built-in rules

```yaml
fail_on: high                  # what interragate check fails on
disable: [provider-unpinned]
severity:
  dns-change: high
required_tags: [Owner, CostCenter]
stateful: [mycorp_database]    # more types that hold data
rules:
  mass-destroy: { threshold: 10 }
scan: [trivy]                  # run these scanners every time
ignore:
  - rule: stateful-replace     # "*" for every rule
    address: aws_db_instance.scratch*
    reason: throwaway database for load tests
```

Ignored findings are still reported, as suppressed, with their reason. `interragate rules` lists the rule IDs.

## Policies: the team's own rules

A policy flags each resource change it matches as an ordinary finding: it appears in every output, counts for `--fail-on`, and works with `disable`, `severity` and `ignore`. `id`, `title` and `severity` are required.

```yaml
policies:
  - id: prod-db-kept
    title: Production databases are never replaced or destroyed
    severity: critical
    message: Talk to the platform team before replacing a production database.
    remediation: Restore from the nightly snapshot instead, or add lifecycle { prevent_destroy = true }.
    match:
      type: [aws_db_instance, aws_rds_cluster]
      action: [delete, replace]
    when:
      - attribute: tags.Environment
        equals: production
```

## match

Every key given must match, and a list matches when any of its items does.

- `type`, `address`, `module`: `*` matches any run of characters, and a module covers the modules it calls.
- `provider`: the short name, such as `aws`.
- `action`: `create`, `update`, `replace`, `delete` (or `destroy`), `read`, `forget`, `no-op`. Without `action`, a policy covers creates, updates, replaces and imports.
- `category`: `network`, `firewall`, `identity`, `secrets`, `dns`, `edge`, `compute`, `containers`, `serverless`, `database`, `storage`, `messaging`, `monitoring`, `utility` or `other`.
- `stateful`: `true` or `false`.
- `origin`: `edit`, `follows` or `untied`, for changes the branch's edits make, that follow from them, or that no edit explains. It applies only when interragate compared the configuration with git, configuration files changed, and the plan is not a destroy plan.

## when

Conditions on the planned object (for a destroy, the object as it is now). All must hold. `attribute` is a dotted path such as `tags.Owner`:

- `*` stands for every list element or map key (`ingress.*.cidr_blocks`), and the condition holds if any of them satisfies it.
- Nested blocks need no index (`root_block_device.encrypted`).
- Brackets hold keys with dots (`tags["kubernetes.io/role/elb"]`).
- Paths go into JSON held in a string (`policy.Statement.*.Action`).

Each condition takes one operator:

| Operator | Holds when the attribute |
|---|---|
| `equals: V`, `not_equals: V` | is V, or is not V |
| `in: [A, B]` | is one of the values |
| `matches: REGEX` | is a string the Go regular expression matches |
| `contains: V` | is a list with V in it, or a string with V in it |
| `present: true` or `false` | has a value, or is missing or null |
| `changes: true` or `false` | changes in this plan, or stays the same |

A value known only after apply, or a sensitive one, never satisfies `equals`, `not_equals`, `in`, `matches` or `contains`.

## consumers

Services outside Terraform that use what it manages: a site on Vercel, an app on phones, a partner's webhook. Terraform cannot see them, so interragate checks each plan against what this list says they use.

```yaml
consumers:
  - name: storefront-web
    platform: vercel          # vercel, netlify, cloudflare, render, fly, heroku, railway, expo, ios, android, partner or any word
    owners: ["@acme/web"]
    notify: false             # true mentions the owners in the pull request comment
    link: https://vercel.com/acme/storefront-web/settings
    update: redeploy          # optional: live, redeploy, rebuild, ota, store or manual
    severity: medium          # optional: the severity of its findings
    uses:
      - resource: aws_db_instance.orders   # * matches any run of characters
        attribute: endpoint                # the value it holds; without one it needs the resource itself
        as: DATABASE_URL                   # what the service calls it
      - output: api_url
      - host: www.storefront.example.com   # or a URL; followed through DNS records and load balancers
      - resource: aws_security_group.alb
        reach: 8443/tcp                    # a port it comes in on through that firewall
        from: 198.51.100.0/24              # optional
```

Each use names one of `resource`, `output` or `host`. `update` defaults from `platform`, and from `as`: values with `NEXT_PUBLIC_`, `VITE_` and similar prefixes are built into a site, so need a rebuild; Expo's `EXPO_PUBLIC_` values go out by over-the-air update; anything else in an app needs a store release, and copies already installed keep the old value.

The rules `consumer-loses`, `consumer-value`, `consumer-reach` and `consumer-outage` report what the plan does to them, at medium, so they show without failing `check` at its default `--fail-on high`; `disable`, `severity` and `ignore` work on them as on any rule. `consumer-outage` covers what goes missing while the apply runs, such as a database while it is replaced, and the apply's narrative says it too. Each report's `consumers[]` lists every service with its effects, and `missing` for uses the plan does not have.
