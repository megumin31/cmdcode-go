# Model-source fixtures

Captured from public sources on 2026-10-16, without credentials or inference:

- `commandcode-go-page.html`: the original model-bearing Next.js script from
  https://commandcode.ai/docs/plans/go. The surrounding document was omitted;
  the script text was not synthesized from rendered table cells. It contains
  94 website models and the explicit 56-ID `Go plan` scope.
- `modelsdev-catalog.sample.json`: a deliberately filtered and reserialized
  sample of https://models.dev/catalog.json, containing representative canonical
  models and their original-provider offerings. This is a test fixture, not a
  complete or byte-identical HTTP response. models.dev is MIT licensed:
  https://github.com/anomalyco/models.dev/blob/dev/LICENSE.

Source response SHA-256 values:
fc923f3582d756bf1eb1ef4a927795a270fbe08118aa237fd89f904b299a7037  build/model-sources/commandcode-go-response.html
97940e5e01e46a70d1da4ecc607d088d3a18f7fd9394ec9abb3568634a11c30e  build/model-sources/modelsdev-catalog.json
