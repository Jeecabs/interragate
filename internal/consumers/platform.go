package consumers

import (
	"strings"

	"github.com/Jeecabs/interragate/internal/config"
	"github.com/Jeecabs/interragate/internal/model"
)

// platform is what interragate knows about where a service runs.
type platform struct {
	name   string           // for prose: Vercel, iOS, partner
	update model.UpdatePath // how a changed value reaches a service there
	site   bool             // it serves a site, which may build values in
}

// platformOf says what interragate knows about a platform, if anything.
func platformOf(name string) (platform, bool) {
	switch strings.ToLower(name) {
	case "vercel":
		return platform{name: "Vercel", update: model.UpdateRedeploy, site: true}, true
	case "netlify":
		return platform{name: "Netlify", update: model.UpdateRedeploy, site: true}, true
	case "cloudflare":
		return platform{name: "Cloudflare", update: model.UpdateRedeploy, site: true}, true
	case "render":
		return platform{name: "Render", update: model.UpdateRedeploy, site: true}, true
	case "fly":
		return platform{name: "Fly.io", update: model.UpdateRedeploy, site: true}, true
	case "heroku":
		return platform{name: "Heroku", update: model.UpdateRedeploy, site: true}, true
	case "railway":
		return platform{name: "Railway", update: model.UpdateRedeploy, site: true}, true
	case "expo":
		return platform{name: "Expo", update: model.UpdateStore}, true
	case "ios":
		return platform{name: "iOS", update: model.UpdateStore}, true
	case "android":
		return platform{name: "Android", update: model.UpdateStore}, true
	case "partner":
		return platform{name: "partner", update: model.UpdateManual}, true
	}
	return platform{}, false
}

// PlatformName gives a platform for prose: Vercel, iOS, partner. One
// interragate does not know is given as written.
func PlatformName(name string) string {
	if p, ok := platformOf(name); ok {
		return p.name
	}
	return name
}

// builtIn reports whether a value's name carries a prefix that web
// frameworks build into a site's JavaScript, so the site needs a new build,
// not only a new deployment, to take a new value.
func builtIn(name string) bool {
	upper := strings.ToUpper(name)
	for _, prefix := range []string{"NEXT_PUBLIC_", "VITE_", "REACT_APP_", "NUXT_PUBLIC_", "GATSBY_", "PUBLIC_", "EXPO_PUBLIC_"} {
		if strings.HasPrefix(upper, prefix) {
			return true
		}
	}
	return false
}

// UpdatePathOf says how a changed value reaches a service through a use:
// the way the service declares, or what its platform and the name it holds
// the value under imply. Expo builds EXPO_PUBLIC_ values into the
// JavaScript bundle, which an EAS Update can replace; anything else in an
// app ships with a store release. It is unknown when nothing says.
func UpdatePathOf(svc *config.Consumer, use config.Use) model.UpdatePath {
	if svc.Update != model.UpdateUnknown {
		return svc.Update
	}
	p, ok := platformOf(svc.Platform)
	switch {
	case !ok:
		return model.UpdateUnknown
	case strings.EqualFold(svc.Platform, "expo") && strings.HasPrefix(strings.ToUpper(use.As), "EXPO_PUBLIC_"):
		return model.UpdateOTA
	case p.site && builtIn(use.As):
		return model.UpdateRebuild
	}
	return p.update
}

// levelOf is the severity of what interragate finds about a service:
// medium unless it declares another.
func levelOf(svc *config.Consumer) model.Severity {
	if svc.Severity != model.None {
		return svc.Severity
	}
	return model.Medium
}
