# Third-party components

These components retain their own licences and copyright notices.

| Component | Licence | Where it is used |
|---|---|---|
| [Geist, Geist Mono and Geist Pixel](https://github.com/vercel/geist-font) | [SIL Open Font License 1.1](geist/LICENSE.txt) | Subset WOFF2 fonts in `internal/render/html/assets/fonts`, embedded in reports and copied into the website |
| [Phosphor Icons](https://github.com/phosphor-icons/core) | [MIT](phosphor/LICENSE) | SVG paths in the report and website |

Standalone HTML pages carry the full project, font and icon notices in
`template#interragate-licences`, which can be read in the page source.
The generated website also ships its notices in `licences.txt`.

Go dependency versions are pinned in [go.mod](../go.mod) and
[go.sum](../go.sum). They include HashiCorp HCL (MPL-2.0), go-cty and
several supporting packages (MIT), the Go supplementary libraries
(BSD-3-Clause), and go-yaml (MIT and Apache-2.0 notices). Their upstream
licence and notice files are retained in the vendored source archive and
copied into each binary archive under `third_party/go/`. `vendor/modules.txt`
in the source archive records the exact module versions.
The binary archives also carry the Go standard library's `LICENSE` and
`PATENTS` notices under `third_party/go/stdlib/`.

Terraform, OpenTofu, optional scanners and development tools are separate
programs; they are not bundled in interragate's release archives.
