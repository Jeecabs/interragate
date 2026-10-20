// Package consumers checks a plan against the services outside Terraform
// that .interragate.yaml declares: a site on Vercel that holds a
// database's endpoint, an app on phones that calls an API, a partner
// whose callbacks come in on a port. Terraform cannot see them, so the
// check is against what the file says each one uses, and everything it
// reports says so.
//
// It works in steps, a file each:
//
//   - assess.go is the way in: Assess takes the declarations and the plan,
//     and the Assessment it returns answers for the rest;
//   - match.go finds what each declared use names in the plan;
//   - impact.go works out what the plan does to it: destroys it (Loss),
//     changes a value the service holds (Shift), points its host somewhere
//     else (Repoint) or closes its way in (Closure);
//   - outage.go finds, once the apply is simulated, when what a service
//     relies on goes missing, following a host through path.go to the
//     load balancer and targets behind it.
//
// words.go says all of it: from the plan's side for findings, and from each
// service's side for its own list. platform.go knows how quickly a change
// reaches a service where it runs, which decides what interragate tells
// you. Package rules turns impacts and outages into findings, so disable,
// severity and ignore work on them, and package analysis adds each
// service's list to the report.
package consumers
