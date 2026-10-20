package rules

import (
	"fmt"

	"github.com/Jeecabs/interragate/internal/model"
)

// runtimeRules flag functions on a deprecated runtime.
func runtimeRules() []*Rule {
	return []*Rule{
		{
			ID: "deprecated-runtime", Title: "Lambda runtime is deprecated", Severity: model.Medium, Category: Hygiene,
			Doc: "Flags a Lambda function created or updated on a runtime AWS has deprecated. AWS no longer patches a deprecated runtime, " +
				"and after its deprecation date it blocks creating functions on it and then updating them, so applies start to fail.",
			Check: checkDeprecatedRuntime,
		},
	}
}

// deprecatedRuntimes are Lambda runtimes past their deprecation date, with
// a supported runtime to move to, from AWS's table of runtimes as of
// October 2026 (docs.aws.amazon.com/lambda/latest/dg/lambda-runtimes.html).
var deprecatedRuntimes = map[string]string{
	"nodejs": "nodejs24.x", "nodejs4.3": "nodejs24.x", "nodejs4.3-edge": "nodejs24.x", "nodejs6.10": "nodejs24.x", "nodejs8.10": "nodejs24.x",
	"nodejs10.x": "nodejs24.x", "nodejs12.x": "nodejs24.x", "nodejs14.x": "nodejs24.x", "nodejs16.x": "nodejs24.x", "nodejs18.x": "nodejs24.x",
	"nodejs20.x": "nodejs24.x",
	"python2.7":  "python3.13", "python3.6": "python3.13", "python3.7": "python3.13", "python3.8": "python3.13", "python3.9": "python3.13",
	"ruby2.5": "ruby3.4", "ruby2.7": "ruby3.4", "ruby3.2": "ruby3.4",
	"java8":    "java21",
	"go1.x":    "provided.al2023",
	"provided": "provided.al2023", "provided.al2": "provided.al2023",
	"dotnetcore1.0": "dotnet10", "dotnetcore2.0": "dotnet10", "dotnetcore2.1": "dotnet10", "dotnetcore3.1": "dotnet10",
	"dotnet5.0": "dotnet10", "dotnet6": "dotnet10", "dotnet7": "dotnet10",
}

func checkDeprecatedRuntime(c *Context) []*model.Finding {
	var out []*model.Finding
	for _, ch := range c.Changes(model.Create, model.Update, model.Replace) {
		if ch.Type != "aws_lambda_function" || ch.Cosmetic != "" {
			continue
		}
		runtime := String(c.After(ch), "runtime")
		next, ok := deprecatedRuntimes[runtime]
		if !ok {
			continue
		}
		f := finding(ch, "", fmt.Sprintf("%s runs on %s, which AWS has deprecated: it no longer gets security patches, and AWS blocks creating functions on it and then updating them, so applies start to fail.",
			Code(ch.Address), Code(runtime)))
		f.Short = "uses a deprecated Lambda runtime"
		f.Paths = []string{"runtime"}
		f.Remediation = fmt.Sprintf("Move to a supported runtime such as %s, and test the function on it first.", Code(next))
		out = append(out, f)
	}
	return out
}
