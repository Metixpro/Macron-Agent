# Terminal text rendering dependencies

Persian/Arabic shaping and bidirectional layout use the unmodified FriBidi Go port
in github.com/benoitkugler/textprocessing v0.0.6, under LGPL 2.1. Its source is at
https://github.com/benoitkugler/textprocessing/tree/v0.0.6/fribidi.
Unicode data comes from github.com/benoitkugler/textlayout v0.3.2 (MIT).
The license texts are included here.

Macron's complete source and build files are available from the release tag at
https://github.com/Metixpro/Macron-Agent. To rebuild or relink with a modified
library, check out that tag, run `go mod download`, use `go mod edit -replace`
to point at your library source, then run `make build` or `make release`.
