# Commands

## Install

```sh
go install github.com/Jeecabs/interragate/cmd/interragate@latest
```

Go 1.26 or later builds interragate. Go 1.21 or later can fetch the required toolchain automatically when `GOTOOLCHAIN=auto` (the default).

For a binary without a Go installation, download the archive for your system from [GitHub Releases](https://github.com/Jeecabs/interragate/releases). Releases provide Linux and macOS builds for amd64 and arm64, matching source with vendored dependencies, and `checksums.txt`. Verify the archive with `sha256sum -c checksums.txt --ignore-missing` on Linux, or `shasum -a 256 --check --ignore-missing checksums.txt` on macOS, then extract it and put `interragate` on your `PATH`. Until the first release is published, install from source with the command above.

From a clone, `make install` (or `scripts/install.sh DIR`) builds interragate and installs it into the first of `~/.local/bin`, `~/bin` and Go's install directory that is on your `PATH`, telling you if none is.

Tab completion covers commands, flags, plan files and, for `interragate explain`, the addresses in the plan:

```sh
source <(interragate completion bash)                                   # in ~/.bashrc
source <(interragate completion zsh)                                    # in ~/.zshrc
interragate completion fish > ~/.config/fish/completions/interragate.fish
```

## In a Terraform or OpenTofu directory

```sh
interragate                          # plans and explains (running init first when the directory needs it)
interragate --open                   # ...and opens the interactive report
interragate open                     # the report of the last plan here, without planning again
interragate --save-plan tfplan       # keep the plan, so you apply exactly what you reviewed
interragate -- -var-file=prod.tfvars # everything after -- goes to terraform plan
interragate watch                    # a live report that plans again every time you save
interragate apply                    # plan, explain, and apply exactly that plan once you confirm
interragate review                   # go through what needs a look; Markdown notes for the PR
interragate doctor                   # if something is not working: checks the binary, init, settings
interragate init                     # a starter .interragate.yaml, with a first policy for what holds data
interragate all                      # every Terraform root below here, worst first, with a report each
```

`interragate apply` applies the plan you just read, from a saved plan file, so nothing can change between reading and applying. It asks for "yes", or for the directory's name when a finding reaches `--fail-on` (high by default); `--auto-approve` skips the question for scripts but still refuses such a plan.

`interragate watch` keeps the report open while you edit. Save a `.tf` or `.tfvars` file (or a local module's) and it plans again, then the page reloads where you were and leads with what your edit did: "Now replaces `random_pet.cluster` and 7 more that follow from it", "`terraform_data.pool` now sets `input.size` to 99", "Fixed: …", "The verdict fell from High risk to Low risk". The terminal prints the same. When a plan fails, the page keeps the last good report and shows Terraform's error until you fix it, with what to do about it when the cause is a familiar one: a held state lock, expired cloud credentials, a `required_version` the binary does not meet. A save that takes the plan to high or critical risk also raises a desktop notification (`--no-notify` turns that off), and the page keeps the session's last twelve plans, each a click away. The page is served on 127.0.0.1 behind a random token, and stops with Ctrl-C.

## With a plan you already have

```sh
terraform plan -out tfplan
interragate show tfplan                            # the summary
interragate report tfplan --open                   # the interactive report
interragate explain tfplan aws_db_instance.orders  # one resource in depth, with its diff
interragate check tfplan --fail-on high            # exits 2 if any finding is high or worse
interragate diff reviewed.json tfplan              # what changed since the plan you reviewed
terraform show -json tfplan | interragate show -   # plan JSON on stdin works too
```

## terraform or tofu

interragate picks `terraform` or `tofu` for you, and stays with the tool a directory belongs to: `.tofu` files, a lock file or installed providers from one registry tie it to that tool, so interragate never rewrites your lock file by planning with the other. If that tool's binary is broken (say, a version manager's shim with no version chosen), interragate stops and passes on the shim's advice; only a directory tied to neither tool falls back to whichever runs. `--bin` or `INTERRAGATE_TF_BIN` chooses for you, and interragate checks the binary runs and refuses it when its init would rewrite the lock file.
