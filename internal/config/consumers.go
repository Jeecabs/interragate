package config

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/Jeecabs/interragate/internal/access"
	"github.com/Jeecabs/interragate/internal/diff"
	"github.com/Jeecabs/interragate/internal/model"
)

// Consumers are the services outside Terraform that use what it manages,
// listed under consumers: in .interragate.yaml. Terraform cannot see them,
// so interragate checks each plan against what the file says they use: a
// site on Vercel that holds a database's endpoint, an app on phones that
// calls an API, a partner whose callbacks come in on a port.
//
// The file is parsed into these types once, so what interragate checks
// against is always well formed: a use names exactly one thing, a host is
// a host name, a port is a port.
type Consumers []Consumer

// Consumer is one service outside Terraform.
type Consumer struct {
	Name model.ServiceName
	// Platform is where it runs: vercel, netlify, cloudflare, render, fly,
	// heroku, railway, expo, ios, android or partner, or any other word.
	Platform string
	// Owners are who to tell, such as @acme/web.
	Owners []string
	// Notify mentions the owners in the pull request comment, which
	// notifies them.
	Notify bool
	// Link is where the service's settings are changed: a web address.
	Link string
	// Update is how a changed value reaches the service. Unknown means
	// interragate works it out from the platform.
	Update model.UpdatePath
	// Severity is the severity of what interragate finds about the
	// service. None means medium.
	Severity model.Severity
	Uses     []Use
}

// Use is one thing a service depends on.
type Use struct {
	Target Target
	// As is what the service calls the value: an environment variable or
	// a setting, such as DATABASE_URL.
	As string
}

// Target is what a use names in the plan: a ResourceTarget, a ReachTarget,
// an OutputTarget or a HostTarget.
type Target interface {
	fmt.Stringer
	target()
}

// ResourceTarget names resources by address. With an attribute, the
// service holds that value; without one, it needs the resource itself.
type ResourceTarget struct {
	Address   AddressPattern
	Attribute AttributePath
}

// ReachTarget names a firewall the service comes in through, on a port.
type ReachTarget struct {
	Firewall AddressPattern
	Port     access.Port
	// From is the range the service comes in from. The zero Source stands
	// for wherever the firewall let the port in from.
	From access.Source
}

// OutputTarget names a root module output the service reads.
type OutputTarget struct {
	Name string
}

// HostTarget names a host name the service calls.
type HostTarget struct {
	Host Host
}

func (ResourceTarget) target() {}
func (ReachTarget) target()    {}
func (OutputTarget) target()   {}
func (HostTarget) target()     {}

func (t ResourceTarget) String() string {
	if t.Attribute.IsZero() {
		return string(t.Address)
	}
	return string(t.Address) + "." + t.Attribute.String()
}

func (t ReachTarget) String() string { return string(t.Firewall) + " " + t.Port.String() }

func (t OutputTarget) String() string { return "output." + t.Name }

func (t HostTarget) String() string { return string(t.Host) }

// AddressPattern is a resource address in which * matches any run of
// characters.
type AddressPattern string

// Matches reports whether an address matches the pattern.
func (p AddressPattern) Matches(address string) bool { return Match(string(p), address) }

// AttributePath is an attribute path as written in the configuration
// file, such as endpoint or ingress[0].cidr_blocks.
type AttributePath struct {
	text  string
	steps []diff.Step
}

// ParseAttributePath reads an attribute path.
func ParseAttributePath(s string) (AttributePath, error) {
	steps, err := ParsePath(s)
	if err != nil {
		return AttributePath{}, err
	}
	return AttributePath{text: s, steps: steps}, nil
}

func (p AttributePath) String() string { return p.text }

// Steps are the path's steps, for diff.Select.
func (p AttributePath) Steps() []diff.Step { return p.steps }

// IsZero reports whether the path is unset.
func (p AttributePath) IsZero() bool { return p.text == "" }

// Host is a host name in lower case, without a trailing dot.
type Host string

var hostName = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]*[a-z0-9])?\.)+[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// ParseHost reads a host name given on its own or as part of a URL, such
// as https://api.example.com/v1.
func ParseHost(s string) (Host, error) {
	h := strings.TrimSpace(s)
	if strings.Contains(h, "://") {
		u, err := url.Parse(h)
		if err != nil || u.Hostname() == "" {
			return "", fmt.Errorf("host %q is not a host name or URL", s)
		}
		h = u.Hostname()
	} else if i := strings.IndexAny(h, "/:"); i >= 0 {
		h = h[:i]
	}
	h = strings.TrimSuffix(strings.ToLower(h), ".")
	if !hostName.MatchString(h) {
		return "", fmt.Errorf("host %q is not a host name; use one such as api.example.com", s)
	}
	return Host(h), nil
}

// Is reports whether a name that a DNS record, certificate or
// distribution carries is the host's own, not a wildcard that covers it.
func (h Host) Is(name string) bool { return canonical(name) == string(h) }

// AnsweredBy reports whether a name that a DNS record, certificate or
// distribution carries covers the host: the same name, or a wildcard one
// level up, so *.example.com answers for api.example.com.
func (h Host) AnsweredBy(name string) bool {
	if h.Is(name) {
		return true
	}
	parent, ok := strings.CutPrefix(canonical(name), "*.")
	if !ok {
		return false
	}
	label, rest, found := strings.Cut(string(h), ".")
	return found && label != "" && rest == parent
}

// canonical writes a name as a Host is written. Route 53 escapes the * of
// a wildcard as \052.
func canonical(name string) string {
	return strings.TrimSuffix(strings.ReplaceAll(strings.ToLower(name), `\052`, "*"), ".")
}

var (
	consumerKeys = []string{"name", "platform", "owners", "notify", "link", "update", "severity", "uses"}
	useKeys      = []string{"resource", "attribute", "output", "host", "reach", "from", "as"}
	outputName   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)
)

// UnmarshalYAML reads the consumers list. Every error names the service
// it is about, by position and name, and the field at fault.
func (cs *Consumers) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.SequenceNode {
		return errors.New("consumers must be a list, with each service starting with -")
	}
	for i, item := range n.Content {
		node := item
		for node.Kind == yaml.AliasNode {
			node = node.Alias
		}
		if node.Kind != yaml.MappingNode {
			return fmt.Errorf("%s: a service must be a mapping with name and uses", consumerName(i, ""))
		}
		v, err := value(node)
		if err != nil {
			return fmt.Errorf("%s: %w", consumerName(i, ""), err)
		}
		raw := v.(map[string]any)
		c, err := parseConsumer(raw)
		if err != nil {
			name, _ := text(raw["name"], "name")
			return fmt.Errorf("%s: %w", consumerName(i, name), err)
		}
		*cs = append(*cs, c)
	}
	return nil
}

// consumerName says which service an error is about: consumers[1] (storefront-web).
func consumerName(i int, name string) string {
	if name == "" {
		return fmt.Sprintf("consumers[%d]", i)
	}
	return fmt.Sprintf("consumers[%d] (%s)", i, name)
}

func parseConsumer(raw map[string]any) (Consumer, error) {
	if err := onlyKeys(raw, "key", consumerKeys); err != nil {
		return Consumer{}, err
	}
	var err error
	str := func(key string) string {
		s, e := text(raw[key], key)
		if err == nil {
			err = e
		}
		return s
	}
	name, platform, link, update, severity := str("name"), str("platform"), str("link"), str("update"), str("severity")
	if err != nil {
		return Consumer{}, err
	}
	c := Consumer{Name: model.ServiceName(strings.TrimSpace(name)), Platform: platform}
	if c.Name == "" {
		return c, errors.New("name is required")
	}
	if c.Owners, err = strs(raw["owners"], "owners"); err != nil {
		return c, err
	}
	for _, o := range c.Owners {
		if strings.TrimSpace(o) == "" {
			return c, errors.New("owners has an empty name")
		}
	}
	if v := raw["notify"]; v != nil {
		b, ok := v.(bool)
		if !ok {
			return c, errors.New("notify must be true or false")
		}
		c.Notify = b
	}
	if link != "" {
		if u, err := url.Parse(link); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return c, fmt.Errorf("link %q is not a web address; use one that starts with https://", link)
		}
		c.Link = link
	}
	if update != "" {
		if c.Update, err = model.ParseUpdatePath(strings.ToLower(update)); err != nil {
			names := make([]string, 0, len(model.UpdatePaths()))
			for _, u := range model.UpdatePaths() {
				names = append(names, u.String())
			}
			return c, fmt.Errorf("%w; use %s", err, either(names))
		}
	}
	if severity != "" {
		if c.Severity, err = model.ParseSeverity(severity); err != nil || c.Severity == model.None {
			return c, fmt.Errorf("unknown severity %q; use %s", severity, either(severityNames))
		}
	}
	items, ok := raw["uses"].([]any)
	if !ok && raw["uses"] != nil {
		return c, errors.New("uses must be a list, with each use starting with -")
	}
	if len(items) == 0 {
		return c, errors.New("uses is required: list the resources, outputs or host names the service depends on")
	}
	for i, item := range items {
		u, err := parseUse(item)
		if err != nil {
			return c, fmt.Errorf("uses[%d]: %w", i, err)
		}
		c.Uses = append(c.Uses, u)
	}
	return c, nil
}

// parseUse reads one use into the target it names.
func parseUse(v any) (Use, error) {
	raw, ok := v.(map[string]any)
	if !ok {
		return Use{}, errors.New("a use must be a mapping, such as {resource: aws_db_instance.orders, attribute: endpoint}")
	}
	if err := onlyKeys(raw, "key", useKeys); err != nil {
		return Use{}, err
	}
	var err error
	str := func(key string) string {
		s, e := text(raw[key], key)
		if err == nil {
			err = e
		}
		return s
	}
	resource, attribute, output, host, reach, from, as := str("resource"), str("attribute"), str("output"), str("host"), str("reach"), str("from"), str("as")
	if err != nil {
		return Use{}, err
	}
	var named []string
	for _, k := range []struct{ key, value string }{{"resource", resource}, {"output", output}, {"host", host}} {
		if k.value != "" {
			named = append(named, k.key)
		}
	}
	switch {
	case len(named) == 0:
		return Use{}, errors.New("name a resource, an output or a host")
	case len(named) > 1:
		return Use{}, fmt.Errorf("names %s; a use names one of resource, output or host", either(named))
	case attribute != "" && resource == "":
		return Use{}, errors.New("attribute goes with resource")
	case reach != "" && resource == "":
		return Use{}, errors.New("reach goes with resource, the firewall the service comes in through")
	case from != "" && reach == "":
		return Use{}, errors.New("from goes with reach")
	case attribute != "" && reach != "":
		return Use{}, errors.New("a use has an attribute or a reach, not both")
	}
	u := Use{As: as}
	switch {
	case output != "":
		if !outputName.MatchString(output) {
			return Use{}, fmt.Errorf("output %q is not an output name; use the name alone, as in api_url", output)
		}
		u.Target = OutputTarget{Name: output}
	case host != "":
		h, err := ParseHost(host)
		if err != nil {
			return Use{}, err
		}
		u.Target = HostTarget{Host: h}
	case reach != "":
		t := ReachTarget{Firewall: AddressPattern(resource)}
		if t.Port, err = access.ParsePort(reach); err != nil {
			return Use{}, fmt.Errorf("reach %w", err)
		}
		if from != "" {
			if t.From, err = access.ParseRange(from); err != nil {
				return Use{}, fmt.Errorf("from %q %w", from, err)
			}
		}
		u.Target = t
	default:
		t := ResourceTarget{Address: AddressPattern(resource)}
		if attribute != "" {
			if t.Attribute, err = ParseAttributePath(attribute); err != nil {
				return Use{}, err
			}
		}
		u.Target = t
	}
	if r, ok := u.Target.(ResourceTarget); ok && strings.ContainsAny(string(r.Address), " \t\n") {
		return Use{}, fmt.Errorf("resource %q is not an address", r.Address)
	}
	if r, ok := u.Target.(ReachTarget); ok && strings.ContainsAny(string(r.Firewall), " \t\n") {
		return Use{}, fmt.Errorf("resource %q is not an address", r.Firewall)
	}
	return u, nil
}
