# Configuration

interragate reads the nearest `.interragate.yaml`, or the file `--config` names. `interragate init` writes a starter one, with a first policy for what holds data. `interragate rules` checks the file: it fails, naming the problem, when the file is invalid, and lists your policies after interragate's own rules, then the [services outside Terraform](#services-outside-terraform) you declare.

```yaml
fail_on: high
disable: [provider-unpinned]
severity:
  dns-change: high
required_tags: [Owner, CostCenter]
stateful: [mycorp_database]   # extra types that hold data
scan: [trivy]                 # run these scanners every time
rules:
  mass-destroy: { threshold: 10 }
ignore:
  - rule: stateful-replace
    address: aws_db_instance.scratch*
    reason: throwaway database for load tests
```

Ignored findings are still reported as suppressed, with their reason. [rules.md](rules.md) lists every rule.

## Your own rules

Add your team's own plan-aware rules under `policies:`, without writing Go or learning OPA. A policy flags each resource change it matches as an ordinary finding: it shows up in every output, counts for `interragate check --fail-on`, and works with `disable`, `severity` and `ignore` like a built-in rule. `interragate rules` lists your policies after interragate's.

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

`id`, `title` and `severity` are required.

### match

`match` picks changes by:

- `type`, `address` and `module`, where `*` matches any run of characters, and a module covers the modules it calls
- `provider`: the short name, such as `aws`
- `action`: `create`, `update`, `replace`, `delete` or `destroy`, `read`, `forget`, `no-op`
- `category`: interragate's kind of resource, such as `database`
- `stateful`
- `origin`: `edit`, `follows` or `untied`, for changes your edits make, that follow from them, or that no edit explains (only when interragate compared the configuration with git and configuration files changed, and not in a destroy plan)

Every key given must match, and a list matches when any item does. Without `action`, a policy covers what the plan creates, updates, replaces or imports; destroys, forgets, data source reads and unchanged resources count only when `action` names them.

### when

`when` adds conditions on the planned object (for a destroy, the object as it is now), and all of them must hold. `attribute` is a dotted path such as `tags.Owner`. `*` stands for every list element or map key, as in `ingress.*.cidr_blocks`, and the condition holds if any of them satisfies it (for `changes: false`, if none of them changes); each condition is checked on its own. Nested blocks need no index (`root_block_device.encrypted`), brackets hold keys with dots (`tags["kubernetes.io/role/elb"]`), and paths can go into JSON held in a string (`policy.Statement.*.Action`). Add `changes: true` on an attribute to flag only what this plan changes, as interragate's own rules do. Each condition takes one operator:

| Operator | Holds when the attribute |
|---|---|
| `equals: V`, `not_equals: V` | is V, or is not V |
| `in: [A, B]` | is one of the values |
| `matches: REGEX` | is a string that the Go regular expression matches |
| `contains: V` | is a list with V in it, or a string with V in it |
| `present: true` or `false` | has a value, or is missing or null |
| `changes: true` or `false` | changes in this plan, or stays the same |

A value known only after apply never satisfies `equals`, `not_equals`, `in`, `matches` or `contains`; it counts as present, and as changing. Sensitive values never satisfy those operators either, so a policy cannot test a secret.

## Services outside Terraform

Terraform only knows what is in its state. A site on Vercel that holds your database's endpoint in an environment variable, an app on phones that calls your API, a partner whose callbacks come in on a port: each depends on what Terraform manages without it knowing. Declare them under `consumers:` and interragate checks every plan against them.

```yaml
consumers:
  - name: storefront-web
    platform: vercel
    owners: ["@acme/web"]
    link: https://vercel.com/acme/storefront-web/settings/environment-variables
    uses:
      - resource: aws_db_instance.orders
        attribute: endpoint
        as: DATABASE_URL
      - host: www.storefront.example.com
        as: NEXT_PUBLIC_API_URL
  - name: storefront-app
    platform: expo
    owners: ["@acme/mobile"]
    uses:
      - output: api_url
        as: EXPO_PUBLIC_API_URL
  - name: Acme Pay callbacks
    platform: partner
    owners: ["@acme/payments"]
    notify: true
    uses:
      - resource: aws_security_group.alb
        reach: 8443/tcp
        from: 198.51.100.0/24
```

Each use names one of these:

- `resource`: an address, in which `*` matches any run of characters. With `attribute`, a path such as `endpoint`, the service holds that value: interragate flags the plan changing it, or leaving it known only after apply. Without one, the service needs the resource itself: interragate flags destroying it, and replacing it, which gives it a new ID.
- `output`: a root module output the service reads, flagged when the plan removes or changes it.
- `host`: a host name the service calls, on its own or as a URL. interragate finds what answers for it in the plan (a DNS record, a CDN's aliases, a certificate), what the record points at, and, behind a load balancer, the targets its listeners forward to. A wildcard record such as `*.example.com` answers for the host only when the host has no records of its own, as in DNS. interragate flags destroying what answers for the host, no longer answering for it (when a record is renamed, say) and pointing it elsewhere, and says when any of it goes missing during the apply.
- `reach` with `resource`: a port the service comes in on through that firewall (an AWS security group or rule, or a Google Cloud firewall), such as `8443/tcp`, and optionally `from`, the range it comes from. It is flagged when the plan stops letting the service in.

`as` is what the service calls the value, such as an environment variable; what interragate says uses it.

How soon a service can take a new value decides what interragate tells you. `platform` sets that, and `update` overrides it:

| `update` | The service | Default for |
|---|---|---|
| `live` | reads the value as it runs, from DNS or a parameter store, and picks up changes by itself | |
| `redeploy` | takes the value on its next deployment | `vercel`, `netlify`, `cloudflare`, `render`, `fly`, `heroku`, `railway` |
| `rebuild` | has the value built in, so needs a new build | the same, when `as` starts with `NEXT_PUBLIC_`, `VITE_`, `REACT_APP_`, `NUXT_PUBLIC_`, `GATSBY_`, `PUBLIC_` or `EXPO_PUBLIC_` |
| `ota` | gets it from an over-the-air update, on installed apps with a compatible build when they next open; the rest keep the old value | `expo`, when `as` starts with `EXPO_PUBLIC_` |
| `store` | ships it in a store release; copies already installed keep the old value until people update | `expo` otherwise, `ios`, `android` |
| `manual` | is changed by someone outside your team | `partner` |

What interragate finds is medium, so it shows under What matters and in the pull request comment without failing `interragate check` at its default `--fail-on high`. Four rules report it, and `disable`, `severity` and `ignore` work on them as on any other rule:

| Rule | Flags |
|---|---|
| `consumer-loses` | destroying what a service uses, removing an output it reads, or no longer answering for its host |
| `consumer-value` | changing a value a service holds or leaving it known only after apply, and pointing its host elsewhere |
| `consumer-reach` | closing the port a service comes in on |
| `consumer-outage` | taking away what a service relies on while the apply runs: a database while it is replaced, or the targets behind a host's load balancer |

`severity:` on one service sets the level of everything interragate finds about it. `consumer-outage` reads interragate's simulation of the apply, which every command runs when the file declares services.

The text and Markdown summaries and the HTML report also get an **Outside Terraform** section with what the plan does to each service it affects, from the service's side and worst first. After those it notes the services the plan leaves alone, those whose findings `disable` or `ignore` leaves out, and anything a service is declared to use that the plan does not have, so a stale declaration does not quietly check nothing. The JSON report has the same under `consumers`.

Owners are written so as not to notify anyone; `notify: true` mentions a service's owners in the pull request comment. `link` is where the service's settings are changed.

interragate never contacts the services, so it cannot see whether they still use what you declared, or how. The [aws sample](../testdata/scenarios/aws-webapp/v2/.interragate.yaml) declares three services; `interragate demo` shows what its plan does to them.
