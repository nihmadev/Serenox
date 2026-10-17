# Convert Go plugin

This example registers the owner-only `.convert source-target` command.

Supported conversions are `png-jpg`, `jpg-png`, and the `jpeg` alias. The
plugin uses the bounded Serenox media client and standard-library PNG/JPEG
codecs; it does not call a remote API or accept filesystem paths from a user.

The daemon supplies the negotiated Go media client in the dispatch context.
The handler downloads the source, converts it locally, uploads the result, and
returns a `MediaAttachment` action for Telegram.

From this repository:

```sh
go test ./examples/plugins/convert
go build -o bin/convert ./examples/plugins/convert
```
