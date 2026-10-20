package rules

import "testing"

func TestArticle(t *testing.T) {
	for noun, want := range map[string]string{
		"S3 bucket":                  "an S3 bucket",
		"RDS database instance":      "an RDS database instance",
		"KMS key":                    "a KMS key",
		"EC2 instance":               "an EC2 instance",
		"NAT gateway":                "a NAT gateway",
		"VPC":                        "a VPC",
		"instance profile":           "an instance profile",
		"security group":             "a security group",
		"ElastiCache cluster":        "an ElastiCache cluster",
		"user pool":                  "a user pool",
		"FSx for Lustre file system": "an FSx for Lustre file system",
	} {
		if got := Article(noun); got != want {
			t.Errorf("Article(%q) = %q, want %q", noun, got, want)
		}
	}
}

func TestList(t *testing.T) {
	if got := List([]string{"a", "b", "c", "d"}, 2); got != "a, b and 2 more" {
		t.Errorf("got %q", got)
	}
	if got := List([]string{"a", "b"}, 5); got != "a and b" {
		t.Errorf("got %q", got)
	}
}

func TestTTLWords(t *testing.T) {
	for in, want := range map[int]string{30: "30 seconds", 60: "a minute", 90: "90 seconds", 300: "5 minutes", 3600: "an hour", 7200: "2 hours",
		5400: "about 2 hours", 86400: "a day", 172800: "2 days", 100000: "about a day", 604800: "7 days"} {
		if got := ttlWords(in); got != want {
			t.Errorf("ttlWords(%d) = %q, want %q", in, got, want)
		}
	}
}
