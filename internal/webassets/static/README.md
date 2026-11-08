# Vendored stylesheets

`bulma-1.0.4.min.css` is `css/bulma.min.css` from Bulma 1.0.4, unmodified
(https://bulma.io, npm package `bulma@1.0.4`, tarball sha1
`942dc017a3a201fa9f0e0c8db3dd52f3cff86712`; the file's sha256 is
`67fa26df1ca9e95d8f2adc7c04fa1b15fa3d24257470ebc10cc68b9aab914bee`). It is
MIT licensed; the license is in `BULMA-LICENSE`. Only the CSS is vendored:
no JavaScript, no Sass build.

`app.css` holds Exemplar's few overrides.

To upgrade, replace the file with the new release's `css/bulma.min.css`,
rename it to the new version, update `Bulma` in `../webassets.go` and the
version and checksums here, and check `/setup` and `/ui/` in light and
dark mode at phone and desktop widths.
