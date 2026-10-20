package tfexec

import (
	"errors"
	"strings"
	"testing"
)

func TestHints(t *testing.T) {
	lock := `
Error: Error acquiring the state lock

Error message: resource temporarily unavailable
Lock Info:
  ID:        6f7e1a6b-3c2d-4b6e-9d4e-1b2c3d4e5f60
  Path:      terraform.tfstate
  Operation: OperationTypeApply
  Who:       alice@laptop
  Version:   1.16.6
  Created:   2026-10-20 01:00:00.123456 +0000 UTC
  Info:

Terraform acquires a state lock to protect the state from being written
by multiple users at the same time.`
	for out, want := range map[string]string{
		lock: "Another run holds the state lock (alice@laptop, since 2026-10-20 01:00:00.123456). Wait for it to finish; if it has died, `terraform force-unlock 6f7e1a6b-3c2d-4b6e-9d4e-1b2c3d4e5f60` releases the lock.",
		"Error: No valid credential sources found\n\nPlease see https://registry.terraform.io/providers/hashicorp/aws": "Your AWS credentials are missing or have expired.",
		"api error ExpiredToken: The security token included in the request is expired":                                "Your AWS credentials are missing or have expired.",
		"Error: Attempted to load application default credentials since neither `credentials` nor `access_token` was set in the provider block.  No credentials loaded. To use your gcloud credentials, run 'gcloud auth application-default login'.  Original error: google: could not find default credentials.": "`gcloud auth application-default login`",
		"Error: building account: could not acquire access token to parse claims: running Azure CLI: exit status 1: ERROR: Please run 'az login' to setup account.":                                                                                                                                                "`az login` renews it.",
		"Error: Unsupported Terraform Core version\n\n  on versions.tf line 2, in terraform:\n   2:   required_version = \">= 1.20\"\n\nThis configuration does not support Terraform version 1.16.6.":                                                                                                             "asks for `>= 1.20`",
		"Error: Failed to query available provider packages\n\nCould not retrieve the list of available versions for provider hashicorp/aws: could not connect to registry.terraform.io":                                                                                                                           "could not reach the provider registry",
		"Error: Kubernetes cluster unreachable: invalid configuration: no configuration has been provided":                                                                                                                                                                                                         "cannot reach a cluster",
		"Error: Invalid reference": "",
	} {
		got := Hint(out, "terraform")
		if want == "" && got != "" || !strings.Contains(got, want) {
			t.Errorf("hint for %.60q:\n%s\nwant it to contain %q", out, got, want)
		}
	}
}

// The hint follows Terraform's output in the error interragate prints.
func TestCommandErrorHint(t *testing.T) {
	e := &CommandError{Err: errors.New("exit status 1"), Output: "Error: No valid credential sources found\n", Command: "tofu plan"}
	if got := e.Error(); !strings.HasSuffix(got, "found\n\nYour AWS credentials are missing or have expired. With SSO, `aws sso login` (with --profile if you use one) renews them; `aws sts get-caller-identity` shows who you are signed in as.") {
		t.Errorf("error:\n%s", got)
	}
}
