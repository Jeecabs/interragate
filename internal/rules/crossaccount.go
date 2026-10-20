package rules

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/Jeecabs/interragate/internal/model"
	"github.com/Jeecabs/interragate/internal/plan"
)

// crossAccountRules flag access granted to other accounts.
func crossAccountRules() []*Rule {
	return []*Rule{
		{
			ID: "cross-account-access", Title: "Grants another AWS account access", Severity: model.Medium, Category: Security,
			Doc: "Flags policy statements this plan adds that name an AWS account other than the ones the configuration's resources live in: " +
				"a role another account can assume, or a bucket, key, queue or topic it can use. Sharing across accounts is sometimes the point " +
				"and sometimes a leak, so a new one deserves a look. Accounts that already had access are not flagged again.",
			Check: checkCrossAccount,
		},
	}
}

var (
	accountInARN = regexp.MustCompile(`^arn:aws[a-z-]*:[a-z0-9-]*:[a-z0-9-]*:(\d{12}):`)
	bareAccount  = regexp.MustCompile(`^\d{12}$`)
)

// accountOf reads the AWS account an ARN or a bare account ID names.
func accountOf(s string) string {
	if m := accountInARN.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	if bareAccount.MatchString(s) {
		return s
	}
	return ""
}

// homeAccounts collects the accounts the configuration's resources live in:
// from their own ARNs, and from aws_caller_identity.
func homeAccounts(c *Context) map[string]bool {
	home := map[string]bool{}
	add := func(obj map[string]any, typ string) {
		if a := accountOf(String(obj, "arn")); a != "" {
			home[a] = true
		}
		if typ == "aws_caller_identity" {
			if a := accountOf(String(obj, "account_id")); a != "" {
				home[a] = true
			}
		}
	}
	for _, ch := range c.Report.Changes {
		add(c.Before(ch), ch.Type)
		add(c.After(ch), ch.Type)
	}
	if st := c.Plan.PriorState; st != nil && st.Values != nil {
		var walk func(m *plan.StateModule)
		walk = func(m *plan.StateModule) {
			if m == nil {
				return
			}
			for _, r := range m.Resources {
				add(r.Values, r.Type)
			}
			for _, child := range m.ChildModules {
				walk(child)
			}
		}
		walk(st.Values.RootModule)
	}
	return home
}

// awsPrincipals lists the accounts a statement's Principal names.
func awsPrincipals(p any) []string {
	m, ok := p.(map[string]any)
	if !ok {
		return nil
	}
	var out []string
	for _, s := range Strings(m["AWS"]) {
		if a := accountOf(s); a != "" {
			out = append(out, a)
		}
	}
	return out
}

// awsService are accounts AWS itself uses to deliver Elastic Load
// Balancing access logs, one per region, which a log bucket's policy has
// to let in: not someone else's account.
var awsService = map[string]bool{
	"127311923021": true, "033677994240": true, "027434742980": true, "797873946194": true, // us-east-1, us-east-2, us-west-1, us-west-2
	"098369216593": true, "754344448648": true, "589379963580": true, "718504428378": true, // af-south-1, ap-east-1, ap-southeast-3, ap-south-1
	"383597477331": true, "600734575887": true, "114774131450": true, "783225319266": true, // ap-northeast-3, ap-northeast-2, ap-southeast-1, ap-southeast-2
	"582318560864": true, "985666609251": true, "054676820928": true, "156460612806": true, // ap-northeast-1, ca-central-1, eu-central-1, eu-west-1
	"652711504416": true, "635631232127": true, "009996457667": true, "897822967062": true, // eu-west-2, eu-south-1, eu-west-3, eu-north-1
	"076674570225": true, "507241528517": true, "048591011584": true, "190560391635": true, // me-south-1, sa-east-1, us-gov-west-1, us-gov-east-1
	"638102146993": true, "037604701340": true, // cn-north-1, cn-northwest-1
}

func checkCrossAccount(c *Context) []*model.Finding {
	var home map[string]bool
	var out []*model.Finding
	for _, ch := range c.Changes(model.Create, model.Update, model.Replace) {
		if !Managed(ch) || !strings.HasPrefix(ch.Type, "aws_") {
			continue
		}
		added := c.newStatements(ch)
		if len(added) == 0 {
			continue
		}
		if home == nil {
			home = homeAccounts(c)
		}
		if len(home) == 0 {
			return nil // with no account of its own to compare with, every account looks foreign
		}
		// Accounts the policy already let in are not news.
		had := map[string]bool{}
		if ch.Action != model.Create {
			for _, ss := range policyStatements(c.Before(ch)) {
				for _, s := range ss {
					for _, a := range awsPrincipals(s.raw["Principal"]) {
						had[a] = true
					}
				}
			}
		}
		for _, s := range added {
			if !strings.EqualFold(s.effect, "Allow") {
				continue
			}
			var foreign []string
			for _, a := range awsPrincipals(s.raw["Principal"]) {
				if !home[a] && !had[a] && !awsService[a] && !slices.Contains(foreign, a) {
					foreign = append(foreign, a)
				}
			}
			if len(foreign) == 0 {
				continue
			}
			sort.Strings(foreign)
			who := "AWS account " + foreign[0]
			if len(foreign) > 1 {
				who = "AWS accounts " + List(foreign, 3)
			}
			cond, _ := s.raw["Condition"].(map[string]any)
			guarded := strings.Contains(fmt.Sprint(cond), "sts:ExternalId") || strings.Contains(fmt.Sprint(cond), "aws:PrincipalOrgID")
			var f *model.Finding
			if strings.HasSuffix(s.path, "assume_role_policy") || strings.Contains(strings.Join(s.list("Action"), ","), "sts:AssumeRole") {
				f = finding(ch, "", fmt.Sprintf("Lets %s assume %s.", who, Code(ch.Address)))
				f.Short = "lets another AWS account assume a role"
				f.Severity = model.High
			} else {
				what := codeList(s.list("Action"))
				if what == "" {
					what = "access"
				}
				f = finding(ch, "", fmt.Sprintf("Gives %s %s on %s.", who, what, Code(ch.Address)))
				f.Short = "grants another AWS account access"
			}
			if guarded {
				f.Message += " A condition limits it (an external ID or an organisation)."
				f.Severity = model.Low
			}
			f.Paths = []string{s.path}
			f.Remediation = "Check the account is one you expect. Limit it with a Condition: aws:PrincipalOrgID for your own organisation, or sts:ExternalId for a third party."
			out = append(out, f)
		}
	}
	return out
}
